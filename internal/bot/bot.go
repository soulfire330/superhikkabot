// Package bot — роутер и обработчики сообщений.
package bot

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/soulfire330/superhikkabot/internal/config"
	"github.com/soulfire330/superhikkabot/internal/dialog"
	"github.com/soulfire330/superhikkabot/internal/digest"
	"github.com/soulfire330/superhikkabot/internal/harness"
	"github.com/soulfire330/superhikkabot/internal/llm"
	"github.com/soulfire330/superhikkabot/internal/media"
	"github.com/soulfire330/superhikkabot/internal/photo"
	"github.com/soulfire330/superhikkabot/internal/tg"
	"github.com/soulfire330/superhikkabot/internal/video"
)

var linkPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// Deps — всё, что нужно роутеру.
type Deps struct {
	TG      *tg.Client
	Config  *config.Config
	LLM     *llm.Client
	Digest  *digest.Client
	Media   *media.Service
	Video   *video.Service
	Photo   *photo.Service
	Dialog  *dialog.Store
	Harness *harness.Runner
	Logger  *log.Logger
}

// Bot маршрутизирует сообщения по командам.
type Bot struct {
	deps Deps

	// Альбом приходит отдельным сообщением на каждое фото: на один альбом
	// отвечаем один раз, остальные апдейты группы пропускаем.
	albumMu  sync.Mutex
	answered map[string]time.Time

	// ignored хранит чаты, которым бот уже сказал "молчу" в лог: один раз на чат.
	ignored sync.Map
}

// New собирает роутер.
func New(deps Deps) *Bot {
	return &Bot{deps: deps, answered: make(map[string]time.Time)}
}

// Handle разбирает одно сообщение.
func (b *Bot) Handle(ctx context.Context, msg *models.Message) {
	if msg == nil || msg.From == nil || msg.From.IsBot {
		return
	}
	if !b.allowed(msg) {
		return
	}

	// Фото запоминаем всегда: по нему потом можно задать вопрос реплаем.
	if b.deps.Photo != nil {
		b.deps.Photo.Observe(msg)
	}

	text := messageText(msg)

	mentioned := false
	if text != "" {
		text, mentioned = stripMention(text, b.deps.TG.Username())
	}

	b.deps.Dialog.Record(msg.Chat.ID, msg.ID, replyID(msg), text, false)

	if strings.HasPrefix(text, "/start") {
		b.start(ctx, msg)
		return
	}

	command, argument := splitCommand(text)
	if b.deps.Logger != nil {
		b.deps.Logger.Printf("сообщение: chat=%d from=%d command=%q len=%d", msg.Chat.ID, msg.From.ID, command, len(text))
	}
	switch command {
	case "dev":
		b.dev(ctx, msg, argument)
		return
	case "deploy":
		b.manualDeploy(ctx, msg)
		return
	case "tldr":
		b.digest(ctx, msg, argument)
		return
	case "open", "ask":
		if strings.TrimSpace(argument) == "" {
			b.send(ctx, msg, "Ask a question: <code>open what's new in Go 1.27</code>")
			return
		}
		b.answer(ctx, msg, argument)
		return
	}

	// Реплай на сообщение агента продолжает ту самую сессию, а не общую болталку.
	if b.continueSession(ctx, msg, text) {
		return
	}

	private := isPrivate(msg)

	if link := firstLink(text); link != "" {
		if media.Supported(link) {
			b.mediaLink(ctx, msg, link)
			return
		}
		if mentioned || private {
			b.linkHint(ctx, msg, link)
			return
		}
	}

	if b.deps.Photo != nil && photo.Supported(msg) && text == "" {
		if mentioned || private {
			if !b.firstOfAlbum(msg) {
				return
			}
			b.describePhoto(ctx, msg)
		}
		return
	}

	if text == "" {
		return
	}

	if isReplyToBot(msg, b.deps.Dialog, b.deps.TG.Username()) || mentioned || private {
		b.answer(ctx, msg, text)
	}
}

// allowed — первая и единственная проверка доступа. Всё, что её не прошло,
// игнорируется молча, даже /start.
func (b *Bot) allowed(msg *models.Message) bool {
	if b.deps.Config.Allows(msg.Chat.ID) {
		return true
	}
	// Админу разрешена личка: его id уже в конфиге, дублировать в allowed_chats незачем.
	if isPrivate(msg) && b.deps.Config.IsAdmin(msg.From.ID) {
		return true
	}
	// Один раз на чат: без этого непонятно, какой id добавлять в allowed_chats.
	if _, seen := b.ignored.LoadOrStore(msg.Chat.ID, true); !seen && b.deps.Logger != nil {
		b.deps.Logger.Printf("игнорирую чат %d (type=%s, title=%q, from=%d)", msg.Chat.ID, msg.Chat.Type, msg.Chat.Title, msg.From.ID)
	}
	return false
}

func messageText(msg *models.Message) string {
	if msg == nil {
		return ""
	}
	if text := strings.TrimSpace(msg.Text); text != "" {
		return text
	}
	return strings.TrimSpace(msg.Caption)
}

func isPrivate(msg *models.Message) bool {
	return string(msg.Chat.Type) == "private"
}

func (b *Bot) start(ctx context.Context, msg *models.Message) {
	_, _ = b.deps.TG.Send(ctx, msg.Chat.ID, msg.ID, welcome)
}

const welcome = `Hi. I can:
• <code>open &lt;question&gt;</code> — answer, searching the web when needed
• <code>tldr &lt;link&gt;</code> — summarize a page, video or repository
• <code>tldr</code> as a reply to a message or <code>tldr &lt;text&gt;</code> — key points summary
• reply to a message — continue the conversation with context
• a TikTok, Instagram or YouTube Shorts link — download the video or photos
• reply to a video/photo — answer based on its content
• <code>dev &lt;task&gt;</code> — call the agent (admins only)
• <code>deploy</code> — rebuild and restart the bot with current changes (admins only)

In a private chat you can write without a command.`

func (b *Bot) dev(ctx context.Context, msg *models.Message, task string) {
	if b.deps.Harness == nil || !b.deps.Config.IsAdmin(msg.From.ID) {
		return
	}
	if strings.TrimSpace(task) == "" {
		b.send(ctx, msg, "Describe the task: <code>dev add a ping command</code>")
		return
	}

	// Каждый dev — новая pi-сессия; реплай на ответ её продолжит.
	prompt := devPrompt(msg, task)
	if !b.agentRun(ctx, msg, func(progress harness.ProgressFunc) (harness.Reply, error) {
		return b.deps.Harness.RunWithProgress(ctx, msg.Chat.ID, prompt, progress)
	}) {
		return
	}

	if !b.deps.Config.Harness.Deploy {
		return
	}

	b.send(ctx, msg, "Building myself with the new changes…")
	go b.deploy(task)
}

// devPrompt добавляет к задаче текст сообщения, на которое ответили: агент
// не видит переписку Telegram, и без этого реплай терял бы весь контекст.
func devPrompt(msg *models.Message, task string) string {
	if msg == nil || msg.ReplyToMessage == nil {
		return task
	}

	quoted := messageText(msg.ReplyToMessage)
	if quoted == "" {
		return task
	}

	return "Задача:\n" + task + "\n\nСообщение, на которое ответили (его текст):\n" + quoted
}

// continueSession уводит реплай на сообщение агента в его сессию.
func (b *Bot) continueSession(ctx context.Context, msg *models.Message, text string) bool {
	if b.deps.Harness == nil || msg.ReplyToMessage == nil {
		return false
	}
	if !b.deps.Config.IsAdmin(msg.From.ID) {
		return false
	}

	prompt := strings.TrimSpace(text)
	if prompt == "" {
		return false
	}

	session, ok := b.deps.Harness.ReplySession(msg.Chat.ID, msg.ReplyToMessage.ID)
	if !ok {
		return false
	}

	if !b.agentRun(ctx, msg, func(progress harness.ProgressFunc) (harness.Reply, error) {
		return b.deps.Harness.ContinueWithProgress(ctx, msg.Chat.ID, session, prompt, progress)
	}) {
		return true
	}

	if b.deps.Config.Harness.Deploy {
		b.send(ctx, msg, "Building myself with the new changes…")
		go b.deploy(prompt)
	}
	return true
}

// agentRun показывает живой статус, гоняет агента и переписывает заглушку ответом.
// Статус обновляется по событиям pi: что агент читает, правит и запускает.
// Возвращает false, если прогон провалился.
func (b *Bot) agentRun(ctx context.Context, msg *models.Message, run func(harness.ProgressFunc) (harness.Reply, error)) bool {
	b.deps.TG.Typing(ctx, msg.Chat.ID)
	placeholder := b.sendStatus(ctx, msg, "⏳ Working…")

	status := newRunStatus(ctx, b, msg.Chat.ID, placeholder)
	reply, err := run(status.Report)
	status.Stop()
	if err != nil {
		b.replaceOrSend(ctx, msg, placeholder, "⚠️ "+tg.Escape(err.Error()))
		return false
	}

	answer := reply.Text
	if strings.TrimSpace(answer) == "" {
		answer = "Done, but the agent returned no text."
	}

	ids := b.replaceOrSend(ctx, msg, placeholder, tg.Markdown(answer))
	b.deps.Harness.RememberReply(msg.Chat.ID, ids, reply.SessionPath)
	return true
}

func (b *Bot) deploy(task string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// При успехе Deploy заменяет процесс и не возвращается: ответ придёт уже из нового.
	result, err := b.deps.Harness.Deploy(ctx, task)
	if err != nil {
		b.deps.Logger.Printf("deploy: %v", err)
		return
	}
	b.deps.Logger.Printf("deploy: %s", result)
}

// manualDeploy — команда deploy: собрать рабочую копию и перезапуститься,
// не дожидаясь правок агента. Только для админов.
func (b *Bot) manualDeploy(ctx context.Context, msg *models.Message) {
	if b.deps.Harness == nil || !b.deps.Config.IsAdmin(msg.From.ID) {
		return
	}

	b.deps.TG.Typing(ctx, msg.Chat.ID)

	ids, err := b.deps.TG.Send(ctx, msg.Chat.ID, msg.ID, "Building myself with the current changes…")
	placeholder := 0
	if err != nil {
		b.deps.Logger.Printf("telegram: отправка не удалась: %v", err)
	}
	if len(ids) > 0 {
		placeholder = ids[0]
	}
	if placeholder != 0 {
		if err := harness.SaveDeployNote(harness.DeployNote{ChatID: msg.Chat.ID, MessageID: placeholder}); err != nil {
			b.deps.Logger.Printf("deploy: записка: %v", err)
		}
	}

	go func(chatID int64, messageID int) {
		dctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		result, err := b.deps.Harness.Deploy(dctx, "manual deploy")
		if err != nil {
			b.deps.Logger.Printf("deploy: %v", err)
			harness.ClearDeployNote()
			text := "⚠️ Build failed: " + tg.Escape(err.Error())
			if messageID != 0 {
				_, _ = b.deps.TG.Replace(dctx, chatID, messageID, text)
			} else {
				_, _ = b.deps.TG.Send(dctx, chatID, 0, text)
			}
			return
		}
		b.deps.Logger.Printf("deploy: %s", result)
	}(msg.Chat.ID, placeholder)
}

func (b *Bot) digest(ctx context.Context, msg *models.Message, argument string) {
	source := firstLink(argument)
	if source == "" {
		source = firstLink(msg.Text)
	}
	if source != "" {
		b.digestLink(ctx, msg, source)
		return
	}

	// Ссылки нет — пересказываем текст: аргумент команды или сообщение, на которое ответили.
	text := strings.TrimSpace(argument)
	if text == "" && msg.ReplyToMessage != nil {
		text = messageText(msg.ReplyToMessage)
	}
	if text == "" {
		b.send(ctx, msg, "Give me a link or text: <code>tldr https://…</code>, <code>tldr &lt;text&gt;</code> or a reply to a message")
		return
	}

	b.deps.TG.Typing(ctx, msg.Chat.ID)

	summary, err := b.deps.Digest.TextSummary(ctx, text)
	if err != nil {
		b.send(ctx, msg, "⚠️ "+tg.Escape(err.Error()))
		return
	}

	b.send(ctx, msg, summary)
}

func (b *Bot) digestLink(ctx context.Context, msg *models.Message, source string) {
	b.deps.TG.Typing(ctx, msg.Chat.ID)

	result, err := b.deps.Digest.Summary(ctx, source)
	if err != nil {
		b.send(ctx, msg, "⚠️ "+tg.Escape(err.Error()))
		return
	}

	b.send(ctx, msg, fmt.Sprintf("<b>%s</b>\n\n%s", tg.Escape(result.Title), result.Summary))
}

func (b *Bot) mediaLink(ctx context.Context, msg *models.Message, link string) {
	if err := b.deps.Media.Submit(ctx, msg.Chat.ID, msg.ID, link); err != nil {
		b.deps.Logger.Printf("media: %v", err)
	}
}

// linkHint подсказывает про tldr, когда ссылка не медийная.
func (b *Bot) linkHint(ctx context.Context, msg *models.Message, link string) {
	b.send(ctx, msg, fmt.Sprintf(
		"I only download TikTok, Instagram and YouTube Shorts. For content: <code>tldr %s</code>",
		tg.Escape(link),
	))
}

// firstOfAlbum сообщает, первый ли это апдейт альбома за последние секунды.
func (b *Bot) firstOfAlbum(msg *models.Message) bool {
	if msg.MediaGroupID == "" {
		return true
	}

	b.albumMu.Lock()
	defer b.albumMu.Unlock()

	now := time.Now()
	for groupID, seen := range b.answered {
		if now.Sub(seen) > 30*time.Second {
			delete(b.answered, groupID)
		}
	}

	if seen, ok := b.answered[msg.MediaGroupID]; ok && now.Sub(seen) < 30*time.Second {
		return false
	}

	b.answered[msg.MediaGroupID] = now
	return true
}

func (b *Bot) describePhoto(ctx context.Context, msg *models.Message) {
	b.answer(ctx, msg, "Опиши, что на этих изображениях, и выдели главное.")
}

// answer отвечает на вопрос, подмешивая контекст ветки, видео и фото.
func (b *Bot) answer(ctx context.Context, msg *models.Message, question string) {
	question = strings.TrimSpace(question)
	if question == "" {
		question = "Ответь на последнее сообщение в этом разговоре."
	}

	b.deps.TG.Typing(ctx, msg.Chat.ID)

	if reply := msg.ReplyToMessage; reply != nil {
		if answer, ok := b.videoAnswer(ctx, msg, reply, question); ok {
			b.send(ctx, msg, answer)
			return
		}
		if answer, ok := b.photoAnswer(ctx, msg, reply, question); ok {
			b.send(ctx, msg, answer)
			return
		}
	}

	if b.deps.Photo != nil && photo.Supported(msg) {
		if answer, ok := b.photoAnswer(ctx, msg, msg, question); ok {
			b.send(ctx, msg, answer)
			return
		}
	}

	prompt := question
	if history := b.deps.Dialog.Context(msg.Chat.ID, replyID(msg)); history != "" {
		prompt = "Контекст разговора:\n" + history + "\n\nВопрос: " + question
	}

	answer, err := b.deps.LLM.Complete(ctx, prompt)
	if err != nil {
		b.send(ctx, msg, "⚠️ "+tg.Escape(err.Error()))
		return
	}
	b.send(ctx, msg, answer)
}

func (b *Bot) videoAnswer(ctx context.Context, msg, reply *models.Message, question string) (string, bool) {
	if b.deps.Video == nil {
		return "", false
	}

	videoContext, ok := b.deps.Video.Context(ctx, msg.Chat.ID, reply.ID)
	if !ok {
		return "", false
	}

	prompt := "Пользователь спрашивает про видео, которое ты скачал.\n"
	if videoContext.Transcript != "" {
		prompt += "\nРаспознанная речь:\n" + videoContext.Transcript + "\n"
	}
	if len(videoContext.Frames) > 0 {
		prompt += fmt.Sprintf("\nНиже %d кадров из видео.\n", len(videoContext.Frames))
	}
	prompt += "\nВопрос: " + question

	answer, err := b.deps.LLM.CompleteWithImages(ctx, prompt, videoContext.Frames)
	if err != nil {
		return "⚠️ " + err.Error(), true
	}
	return answer, true
}

func (b *Bot) photoAnswer(ctx context.Context, msg, reply *models.Message, question string) (string, bool) {
	if b.deps.Photo == nil {
		return "", false
	}

	photoContext, ok := b.deps.Photo.Context(ctx, msg.Chat.ID, reply.ID)
	if !ok {
		return "", false
	}

	prompt := fmt.Sprintf(
		"Пользователь спрашивает про изображения (%d шт). Если это коллаж — номера ячеек идут по порядку.\n\nВопрос: %s",
		photoContext.Count, question,
	)

	answer, err := b.deps.LLM.CompleteWithImages(ctx, prompt, photoContext.Images)
	if err != nil {
		return "⚠️ " + err.Error(), true
	}
	return answer, true
}

func (b *Bot) send(ctx context.Context, msg *models.Message, text string) {
	b.sendIDs(ctx, msg, text)
}

// sendIDs отправляет сообщение и возвращает id всех его чанков.
func (b *Bot) sendIDs(ctx context.Context, msg *models.Message, text string) []int {
	ids, err := b.deps.TG.Send(ctx, msg.Chat.ID, msg.ID, text)
	if err != nil {
		b.deps.Logger.Printf("telegram: отправка не удалась: %v", err)
	}
	return ids
}

// sendStatus отправляет сообщение-статус и возвращает его id для будущей правки.
func (b *Bot) sendStatus(ctx context.Context, msg *models.Message, text string) int {
	ids := b.sendIDs(ctx, msg, text)
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// replaceOrSend переписывает сообщение-заглушку, а если её нет — отправляет новое.
// Возвращает id всех сообщений ответа, чтобы привязать их к сессии.
func (b *Bot) replaceOrSend(ctx context.Context, msg *models.Message, messageID int, text string) []int {
	if messageID == 0 {
		return b.sendIDs(ctx, msg, text)
	}
	ids, err := b.deps.TG.Replace(ctx, msg.Chat.ID, messageID, text)
	if err != nil {
		b.deps.Logger.Printf("telegram: правка не удалась: %v", err)
		return b.sendIDs(ctx, msg, text)
	}
	return ids
}

func replyID(msg *models.Message) int {
	if msg.ReplyToMessage != nil {
		return msg.ReplyToMessage.ID
	}
	return 0
}

func isReplyToBot(msg *models.Message, store *dialog.Store, botUsername string) bool {
	if msg.ReplyToMessage == nil {
		return false
	}
	if store.IsBot(msg.Chat.ID, msg.ReplyToMessage.ID) {
		return true
	}
	// После перезапуска память диалогов пуста: узнаём себя по username в reply_to_message.
	from := msg.ReplyToMessage.From
	return from != nil && botUsername != "" && strings.EqualFold(from.Username, botUsername)
}

// stripMention убирает @username бота и сообщает, было ли упоминание.
func stripMention(text, username string) (string, bool) {
	if username == "" {
		return text, false
	}

	mention := "@" + strings.ToLower(username)
	lower := strings.ToLower(text)
	index := strings.Index(lower, mention)
	if index < 0 {
		return text, false
	}

	return strings.TrimSpace(text[:index] + text[index+len(mention):]), true
}

// splitCommand отделяет первую команду от аргумента.
func splitCommand(text string) (string, string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}

	command := strings.TrimPrefix(fields[0], "/")
	if index := strings.IndexByte(command, '@'); index > 0 {
		command = command[:index]
	}
	command = strings.ToLower(command)

	if len(fields) == 1 {
		return command, ""
	}

	rest := strings.Join(fields[1:], " ")
	return command, rest
}

func firstLink(text string) string {
	match := linkPattern.FindString(text)
	return strings.TrimRight(match, ".,;:!?)»")
}
