// Package tg — тонкая обвязка над Bot API: чанки, HTML-фолбэк, хуки на отправленное.
package tg

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	chunkLimit        = 3800
	maxDownloadBytes  = 20 << 20
	maxAlbumPhotos    = 10
	parseErrorSnippet = "can't parse entities"
	pollTimeout       = time.Minute
	downloadTimeout   = 2 * time.Minute
)

var htmlTags = regexp.MustCompile(`<[^>]*>`)

// Options — параметры клиента.
type Options struct {
	Token     string
	Transport http.RoundTripper
	Logger    *log.Logger
	OnMessage func(context.Context, *models.Message)
}

// Client отправляет сообщения и знает, что именно он отправил.
type Client struct {
	api        *bot.Bot
	httpClient *http.Client
	username   string
	id         int64
	logger     *log.Logger

	mu       sync.RWMutex
	onSent   func(chatID int64, ids []int, text string, replyTo int)
	onEdited func(chatID int64, messageID int, text string)
}

// New авторизуется, запоминает username бота и вешает обработчик сообщений.
func New(ctx context.Context, opts Options) (*Client, error) {
	options := []bot.Option{
		bot.WithErrorsHandler(func(err error) {
			opts.Logger.Printf("telegram: %v", err)
		}),
		bot.WithDefaultHandler(func(ctx context.Context, _ *bot.Bot, update *models.Update) {
			if update == nil || update.Message == nil || opts.OnMessage == nil {
				return
			}
			opts.OnMessage(ctx, update.Message)
		}),
	}
	if opts.Transport != nil {
		// Транспорт с прокси нужен и поллингу, и отправке файлов.
		options = append(options, bot.WithHTTPClient(pollTimeout, &http.Client{
			Transport: opts.Transport,
			Timeout:   pollTimeout,
		}))
	}

	api, err := bot.New(opts.Token, options...)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	me, err := api.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram getMe: %w", err)
	}

	return &Client{
		api:        api,
		httpClient: &http.Client{Transport: opts.Transport, Timeout: downloadTimeout},
		username:   me.Username,
		id:         me.ID,
		logger:     opts.Logger,
	}, nil
}

// Username возвращает username без @.
func (c *Client) Username() string { return c.username }

// ID возвращает id бота.
func (c *Client) ID() int64 { return c.id }

// API отдаёт клиент Bot API: он нужен там, где нужны методы, которых нет в обвязке.
func (c *Client) API() *bot.Bot { return c.api }

// SetHooks задаёт обработчики отправленных и отредактированных сообщений.
func (c *Client) SetHooks(onSent func(chatID int64, ids []int, text string, replyTo int), onEdited func(chatID int64, messageID int, text string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSent = onSent
	c.onEdited = onEdited
}

func (c *Client) sent(chatID int64, ids []int, text string, replyTo int) {
	c.mu.RLock()
	hook := c.onSent
	c.mu.RUnlock()
	if hook != nil {
		hook(chatID, ids, text, replyTo)
	}
}

func (c *Client) edited(chatID int64, messageID int, text string) {
	c.mu.RLock()
	hook := c.onEdited
	c.mu.RUnlock()
	if hook != nil {
		hook(chatID, messageID, text)
	}
}

// Send отправляет текст чанками и возвращает id отправленных сообщений.
func (c *Client) Send(ctx context.Context, chatID int64, replyTo int, text string) ([]int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	var ids []int
	for i, chunk := range split(text) {
		reply := 0
		if i == 0 {
			reply = replyTo
		}

		msg, err := c.sendChunk(ctx, chatID, reply, chunk)
		if err != nil {
			return ids, err
		}
		ids = append(ids, msg.ID)
	}

	c.sent(chatID, ids, text, replyTo)
	return ids, nil
}

func (c *Client) sendChunk(ctx context.Context, chatID int64, replyTo int, chunk string) (*models.Message, error) {
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      chunk,
		ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: bot.True(),
		},
	}
	if replyTo > 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}

	msg, err := c.api.SendMessage(ctx, params)
	if err == nil {
		return msg, nil
	}
	if !strings.Contains(err.Error(), parseErrorSnippet) {
		return nil, err
	}

	params.Text = stripHTML(chunk)
	params.ParseMode = ""
	return c.api.SendMessage(ctx, params)
}

// Edit меняет текст сообщения, откатываясь на plain text при разметке-ошибке.
func (c *Client) Edit(ctx context.Context, chatID int64, messageID int, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len([]rune(text)) > chunkLimit {
		text = string([]rune(text)[:chunkLimit])
	}

	params := &bot.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}

	_, err := c.api.EditMessageText(ctx, params)
	if err != nil && strings.Contains(err.Error(), parseErrorSnippet) {
		params.Text = stripHTML(text)
		params.ParseMode = ""
		_, err = c.api.EditMessageText(ctx, params)
	}
	if err != nil {
		return err
	}

	c.edited(chatID, messageID, text)
	return nil
}

// Replace переписывает сообщение целиком: первый чанк уходит правкой,
// остальные — новыми сообщениями. Возвращает id всех частей.
func (c *Client) Replace(ctx context.Context, chatID int64, messageID int, text string) ([]int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	chunks := split(text)
	if err := c.Edit(ctx, chatID, messageID, chunks[0]); err != nil {
		return nil, err
	}

	ids := []int{messageID}
	var tail []int
	for _, chunk := range chunks[1:] {
		msg, err := c.sendChunk(ctx, chatID, 0, chunk)
		if err != nil {
			return ids, err
		}
		tail = append(tail, msg.ID)
	}
	ids = append(ids, tail...)

	if len(tail) > 0 {
		c.sent(chatID, tail, strings.Join(chunks[1:], "\n"), 0)
	}
	return ids, nil
}

// Delete удаляет сообщение. Ошибка не фатальна для вызывающего.
func (c *Client) Delete(ctx context.Context, chatID int64, messageID int) error {
	_, err := c.api.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID})
	return err
}

// Typing показывает индикатор набора; ошибка не важна.
func (c *Client) Typing(ctx context.Context, chatID int64) {
	_, _ = c.api.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
}

// Download скачивает файл по file_id в память.
func (c *Client) Download(ctx context.Context, fileID string) ([]byte, error) {
	file, err := c.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("getFile: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.api.FileDownloadLink(file), nil)
	if err != nil {
		return nil, fmt.Errorf("запрос на скачивание: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачивание файла: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("скачивание файла: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return nil, fmt.Errorf("чтение файла: %w", err)
	}
	return data, nil
}

// SendVideo отправляет видео файлом; подпись не нужна.
func (c *Client) SendVideo(ctx context.Context, chatID int64, replyTo int, path string, width, height, duration int) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("открытие видео: %w", err)
	}
	defer file.Close()

	params := &bot.SendVideoParams{
		ChatID:            chatID,
		Video:             &models.InputFileUpload{Filename: filepath.Base(path), Data: file},
		Width:             width,
		Height:            height,
		Duration:          duration,
		SupportsStreaming: true,
	}
	if replyTo > 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}

	msg, err := c.api.SendVideo(ctx, params)
	if err != nil {
		return 0, err
	}

	c.sent(chatID, []int{msg.ID}, "[видео]", replyTo)
	return msg.ID, nil
}

// SendPhotos отправляет фото альбомами до 10 штук и возвращает все id.
func (c *Client) SendPhotos(ctx context.Context, chatID int64, replyTo int, paths []string) ([]int, error) {
	var ids []int

	for start := 0; start < len(paths); start += maxAlbumPhotos {
		end := min(start+maxAlbumPhotos, len(paths))
		batch := paths[start:end]

		// Альбом Telegram принимает только от 2 фото: одну картинку шлём отдельно.
		if len(batch) == 1 {
			id, err := c.sendSinglePhoto(ctx, chatID, replyTo, batch[0])
			if err != nil {
				return ids, err
			}
			ids = append(ids, id)
			continue
		}

		media, files, err := photoBatch(batch)
		if err != nil {
			return ids, err
		}

		params := &bot.SendMediaGroupParams{ChatID: chatID, Media: media}
		if replyTo > 0 && len(ids) == 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
		}

		messages, err := c.api.SendMediaGroup(ctx, params)
		closeFiles(files)
		if err != nil {
			return ids, err
		}

		for _, msg := range messages {
			ids = append(ids, msg.ID)
		}
	}

	c.sent(chatID, ids, "[фото]", replyTo)
	return ids, nil
}

func (c *Client) sendSinglePhoto(ctx context.Context, chatID int64, replyTo int, path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("открытие фото: %w", err)
	}
	defer file.Close()

	params := &bot.SendPhotoParams{
		ChatID: chatID,
		Photo:  &models.InputFileUpload{Filename: filepath.Base(path), Data: file},
	}
	if replyTo > 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}

	msg, err := c.api.SendPhoto(ctx, params)
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

// photoBatch открывает файлы и собирает элементы альбома.
func photoBatch(paths []string) ([]models.InputMedia, []*os.File, error) {
	media := make([]models.InputMedia, 0, len(paths))
	files := make([]*os.File, 0, len(paths))

	for i, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			closeFiles(files)
			return nil, nil, fmt.Errorf("открытие фото %s: %w", path, err)
		}
		files = append(files, file)

		media = append(media, &models.InputMediaPhoto{
			Media:           fmt.Sprintf("attach://photo%d", i),
			MediaAttachment: file,
		})
	}

	return media, files, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

// stripHTML убирает теги: запасной путь, когда Telegram не принял разметку.
func stripHTML(text string) string {
	return strings.TrimSpace(htmlTags.ReplaceAllString(text, ""))
}

// split режет текст на куски по абзацам, не разрывая слова.
func split(text string) []string {
	runes := []rune(text)
	if len(runes) <= chunkLimit {
		return []string{text}
	}

	var chunks []string
	var current strings.Builder

	flush := func() {
		if chunk := strings.TrimSpace(current.String()); chunk != "" {
			chunks = append(chunks, chunk)
		}
		current.Reset()
	}

	for _, paragraph := range strings.Split(text, "\n") {
		line := []rune(paragraph)
		if len(line) > chunkLimit {
			flush()
			for len(line) > chunkLimit {
				chunks = append(chunks, string(line[:chunkLimit]))
				line = line[chunkLimit:]
			}
			current.WriteString(string(line))
			current.WriteString("\n")
			continue
		}

		if current.Len() > 0 && len([]rune(current.String()))+len(line)+1 > chunkLimit {
			flush()
		}
		current.WriteString(paragraph)
		current.WriteString("\n")
	}
	flush()

	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}

// Escape экранирует символы, которые Telegram считает разметкой.
func Escape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}
