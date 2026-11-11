// Package digest — краткое содержание по ссылке: youtube, github, обычная страница.
package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soulfire330/superhikkabot/internal/firecrawl"
	"github.com/soulfire330/superhikkabot/internal/llm"
)

// Result — готовый дайджест.
type Result struct {
	Title      string
	Summary    string
	SourceType string
	Source     string
}

// Client собирает содержимое страницы и просит модель его пересказать.
type Client struct {
	llm        *llm.Client
	firecrawl  *firecrawl.Client
	language   string
	httpClient *http.Client
}

// New собирает клиент; language — язык пересказа.
func New(summarizer *llm.Client, crawl *firecrawl.Client, language string) *Client {
	if strings.TrimSpace(language) == "" {
		language = "ru"
	}
	return &Client{
		llm:        summarizer,
		firecrawl:  crawl,
		language:   language,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// WithHTTPClient подменяет HTTP-клиент для загрузки страниц и API GitHub.
func (c *Client) WithHTTPClient(client *http.Client) *Client {
	if client != nil {
		c.httpClient = client
	}
	return c
}

// Tool отдаёт инструмент «сделай дайджест ссылки» для модели.
func (c *Client) Tool() llm.Tool {
	return llm.Tool{
		Name:        "link_digest",
		Description: "Краткое содержание по ссылке: YouTube-видео, GitHub-репозиторий или обычная страница. Вызывай, когда пользователь просит пересказать ссылку или ответить по её содержимому.",
		Parameters: json.RawMessage(
			`{"type":"object","properties":{"url":{"type":"string","description":"ссылка http(s)"}},"required":["url"]}`,
		),
		Execute: c.tool,
	}
}

func (c *Client) tool(ctx context.Context, args string) (string, error) {
	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	result, err := c.Summary(ctx, params.URL)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Заголовок: %s\n\n%s", result.Title, result.Summary), nil
}

// Summary достаёт текст источника и пересказывает его моделью.
func (c *Client) Summary(ctx context.Context, source string) (*Result, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("empty link")
	}

	kind, err := resolve(source)
	if err != nil {
		return nil, err
	}

	text, err := c.extract(ctx, kind, source)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("no text found in the source")
	}

	title, summary, err := c.summarize(ctx, kind, source, text)
	if err != nil {
		return nil, err
	}

	return &Result{Title: title, Summary: summary, SourceType: kind, Source: source}, nil
}

// resolve определяет тип источника по адресу.
func resolve(source string) (string, error) {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a link: %s", source)
	}

	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")

	switch {
	case host == "youtube.com" || host == "m.youtube.com" || host == "youtu.be":
		return "youtube", nil
	case host == "github.com":
		return "github", nil
	default:
		return "webpage", nil
	}
}

func (c *Client) extract(ctx context.Context, kind, source string) (string, error) {
	switch kind {
	case "youtube":
		return c.youtube(ctx, source)
	case "github":
		return c.github(ctx, source)
	default:
		return c.webpage(ctx, source)
	}
}

const summarySystem = `Ты — объективный суммаризатор. Тебе дают текст источника, ты возвращаешь
краткое содержание: 2-4 абзаца или 3-7 пунктов, только суть и факты, без воды.
Разметка — Telegram HTML: <b>, <i>, <a href="URL">, <code>. Markdown запрещён.
Ответ — строго JSON без markdown-обёртки: {"title": "...", "summary": "..."}.
title — короткий заголовок содержимого, summary — само содержание.`

const textSystem = `Ты сжимаешь текст в пересказ для Telegram. Верни только тезисы:
3-7 пунктов через "• ", каждый — одна мысль, без вступлений вроде "В тексте говорится"
и без выводов от себя. Уложись в %d символов и будь заметно короче исходного текста:
лучше выкинуть деталь, чем пересказать всё. Разметка — Telegram HTML:
<b>, <i>, <code>, <a href="URL">. Markdown запрещён.`

// TextSummary пересказывает текст тезисно и короче исходника.
func (c *Client) TextSummary(ctx context.Context, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("nothing to summarize")
	}

	system := fmt.Sprintf(textSystem, textLimit(len([]rune(text))))
	raw, err := c.llm.Raw(ctx, system, "Текст:\n"+truncateRunes(text, 8000), false)
	if err != nil {
		return "", fmt.Errorf("text summary: %w", err)
	}

	summary := strings.TrimSpace(raw)
	if summary == "" {
		return "", fmt.Errorf("model returned an empty summary")
	}
	return summary, nil
}

// textLimit — сколько символов разрешить в пересказе: половина исходника,
// но не меньше 120 и не больше 900.
func textLimit(runes int) int {
	limit := runes / 2
	if limit < 120 {
		limit = 120
	}
	if limit > 900 {
		limit = 900
	}
	return limit
}

func (c *Client) summarize(ctx context.Context, kind, source, text string) (string, string, error) {
	prompt := fmt.Sprintf(
		"Тип источника: %s\nСсылка: %s\nЯзык ответа: %s\n\nТекст:\n%s",
		kind, source, c.language, truncateRunes(text, 60_000),
	)

	raw, err := c.llm.Raw(ctx, summarySystem, prompt, true)
	if err != nil {
		// Провайдер мог не принять response_format — переспрашиваем без него.
		raw, err = c.llm.Raw(ctx, summarySystem, prompt, false)
		if err != nil {
			return "", "", fmt.Errorf("model summary: %w", err)
		}
	}

	var parsed struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stripFences(raw)), &parsed); err != nil {
		// Модель иногда отдаёт текст вместо JSON — отдаём как есть.
		return "Digest", strings.TrimSpace(raw), nil
	}

	if strings.TrimSpace(parsed.Summary) == "" {
		return "", "", fmt.Errorf("model returned an empty summary")
	}
	if strings.TrimSpace(parsed.Title) == "" {
		parsed.Title = "Digest"
	}

	return strings.TrimSpace(parsed.Title), strings.TrimSpace(parsed.Summary), nil
}

// stripFences убирает markdown-обёртку, если модель её всё же добавила.
func stripFences(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}

	if index := strings.Index(raw, "\n"); index >= 0 {
		raw = raw[index+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "```"))
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "\n…(обрезано)"
}
