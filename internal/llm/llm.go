// Package llm — клиент chat completions: ответы с инструментами, vision и сырые запросы.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout       = 45 * time.Second
	attemptTimeout       = 60 * time.Second
	visionTimeout        = 120 * time.Second
	maxResponseBodyBytes = 10 << 20
	maxVisionImages      = 8
	visionMaxTokens      = 1200
	answerMaxTokens      = 800
	userAgent            = "github.com/soulfire330/superhikkabot/1.0"
)

const systemPrompt = `Ты полезный ассистент в Telegram. Пиши сжато: 3-6 предложений или
2-5 пунктов, не больше 700 символов, если в вопросе не просят подробный разбор.
Без вступлений вроде "Конечно" и "Отличный вопрос", без итогового резюме и повторов.
Ответ уходит с parse_mode=HTML, поэтому разметка — только эти теги:
<b>, <i>, <u>, <s>, <a href="URL">текст</a>, <code>, <pre>, <blockquote>.
Markdown запрещён: никаких **, #, бектиков и [текст](url). Абзацы и списки — обычными
переносами строк и символами "• " или "- ".
Символы <, >, & вне разрешённых тегов экранируй как &lt;, &gt;, &amp;.
Если ответ опирается на веб-поиск, дай ссылки на источники через <a href="...">.
Отвечай по-русски, коротко и по делу.`

// Tool — инструмент, который модель может вызвать.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Execute     func(ctx context.Context, args string) (string, error)
}

// Client — клиент OpenAI-совместимого chat completions.
type Client struct {
	url        string
	apiKey     string
	model      string
	reasoning  string
	maxRounds  int
	tools      []Tool
	httpClient *http.Client
	logger     *log.Logger
}

// New собирает клиент.
func New(url, apiKey, model string, tools ...Tool) *Client {
	return &Client{
		url:        url,
		apiKey:     apiKey,
		model:      model,
		maxRounds:  len(tools) + 1,
		tools:      tools,
		httpClient: &http.Client{Timeout: visionTimeout},
	}
}

// WithReasoning задаёт reasoning_effort; пусто — уровень провайдера по умолчанию.
func (c *Client) WithReasoning(effort string) *Client {
	c.reasoning = effort
	return c
}

// WithMaxRounds ограничивает число раундов вызова инструментов.
func (c *Client) WithMaxRounds(rounds int) *Client {
	if rounds > 0 {
		c.maxRounds = rounds
	}
	return c
}

// WithHTTPClient подменяет HTTP-клиент: нужен для прокси и своих таймаутов.
func (c *Client) WithHTTPClient(client *http.Client) *Client {
	if client != nil {
		c.httpClient = client
	}
	return c
}

// WithLogger подключает лог раундов и вызовов инструментов.
func (c *Client) WithLogger(logger *log.Logger) *Client {
	c.logger = logger
	return c
}

func (c *Client) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete отвечает на вопрос, используя инструменты, и откатывается на ответ без них.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	if len(c.tools) == 0 {
		return c.Raw(ctx, systemPrompt, prompt, false)
	}

	answer, err := c.withTools(ctx, prompt)
	if err == nil {
		return answer, nil
	}

	c.logf("llm: инструменты не сработали (%v), отвечаю без них", err)
	return c.Raw(ctx, systemPrompt, prompt, false)
}

func (c *Client) withTools(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	messages := []message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}

	for round := 0; ; round++ {
		last := round >= c.maxRounds
		resp, err := c.call(ctx, messages, c.toolSpecs(last))
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("пустой список choices")
		}

		choice := resp.Choices[0].Message
		if len(choice.ToolCalls) == 0 {
			text, _ := choice.Content.(string)
			if strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("модель не вернула текст")
			}
			return text, nil
		}

		messages = append(messages, message{Role: "assistant", ToolCalls: choice.ToolCalls})
		for _, call := range choice.ToolCalls {
			result := c.runTool(ctx, call)
			messages = append(messages, message{
				Role:       "tool",
				Content:    result,
				ToolCallID: call.ID,
			})
		}
	}
}

func (c *Client) runTool(ctx context.Context, call toolCall) string {
	for _, tool := range c.tools {
		if tool.Name != call.Function.Name {
			continue
		}

		c.logf("llm: инструмент %s(%s)", tool.Name, truncate(call.Function.Arguments, 200))
		result, err := tool.Execute(ctx, call.Function.Arguments)
		if err != nil {
			return "ошибка: " + err.Error()
		}
		return result
	}

	return "ошибка: неизвестный инструмент " + call.Function.Name
}

func (c *Client) toolSpecs(last bool) []toolSpec {
	if last {
		return nil
	}

	specs := make([]toolSpec, 0, len(c.tools))
	for _, tool := range c.tools {
		var spec toolSpec
		spec.Type = "function"
		spec.Function.Name = tool.Name
		spec.Function.Description = tool.Description
		spec.Function.Parameters = tool.Parameters
		specs = append(specs, spec)
	}
	return specs
}

// Raw отправляет один запрос без инструментов. jsonMode просит ответ строго в JSON.
func (c *Client) Raw(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	messages := []message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	body := map[string]any{
		"model":    c.model,
		"messages": messages,
		"stream":   false,
	}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	} else {
		body["max_tokens"] = answerMaxTokens
	}
	if c.reasoning != "" {
		body["reasoning_effort"] = c.reasoning
	}

	resp, err := c.post(ctx, body)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("пустой список choices")
	}

	text, _ := resp.Choices[0].Message.Content.(string)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("модель не вернула текст")
	}
	return text, nil
}

// CompleteWithImages отправляет текст и JPEG-кадры — для ответов по видео и фото.
func (c *Client) CompleteWithImages(ctx context.Context, prompt string, images [][]byte) (string, error) {
	if len(images) > maxVisionImages {
		images = images[:maxVisionImages]
	}
	if len(images) == 0 {
		return c.Raw(ctx, systemPrompt, prompt, false)
	}

	parts := make([]any, 0, len(images)+1)
	parts = append(parts, map[string]any{"type": "text", "text": prompt})
	for _, image := range images {
		if len(image) == 0 {
			continue
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image),
			},
		})
	}

	body := map[string]any{
		"model": c.model,
		"messages": []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: parts},
		},
		"stream":     false,
		"max_tokens": visionMaxTokens,
	}
	// Vision отвечает быстро и без рассуждений: уровень провайдера тут только мешает.
	body["reasoning_effort"] = "none"

	resp, err := c.post(ctx, body)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("пустой список choices")
	}

	text, _ := resp.Choices[0].Message.Content.(string)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("модель не вернула текст")
	}
	return text, nil
}

func (c *Client) call(ctx context.Context, messages []message, tools []toolSpec) (*chatResponse, error) {
	body := map[string]any{
		"model":    c.model,
		"messages": messages,
		"stream":   false,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	} else {
		body["max_tokens"] = answerMaxTokens
	}
	if c.reasoning != "" {
		body["reasoning_effort"] = c.reasoning
	}

	return c.post(ctx, body)
}

func (c *Client) post(ctx context.Context, body map[string]any) (*chatResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("маршалинг запроса: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("создание запроса: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос к LLM: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("чтение ответа LLM: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM вернул HTTP %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}

	var result chatResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("разбор ответа LLM: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("ошибка LLM: %s", result.Error.Message)
	}
	return &result, nil
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
