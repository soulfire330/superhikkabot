// Package firecrawl — веб-поиск и вытаскивание текста страницы через Firecrawl.
package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"superhikkabot/internal/llm"
)

const (
	searchTimeout        = 15 * time.Second
	scrapeTimeout        = 45 * time.Second
	maxResponseBodyBytes = 10 << 20
	maxResults           = 5
	maxDescriptionRunes  = 500
)

// Client — клиент Firecrawl; пустой ключ выключает его методы.
type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// New собирает клиент; пустой baseURL заменяется продовым адресом.
func New(baseURL, apiKey string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.firecrawl.dev"
	}

	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: scrapeTimeout},
	}
}

// WithHTTPClient подменяет HTTP-клиент: нужен для прокси.
func (c *Client) WithHTTPClient(client *http.Client) *Client {
	if client != nil {
		c.client = client
	}
	return c
}

// Enabled сообщает, настроен ли ключ.
func (c *Client) Enabled() bool { return c.apiKey != "" }

// Tool отдаёт инструмент веб-поиска для модели.
func (c *Client) Tool() llm.Tool {
	return llm.Tool{
		Name:        "web_search",
		Description: "Поиск в интернете. Вызывай, когда для ответа нужны свежие или фактические данные, которых нет в сообщении пользователя.",
		Parameters: json.RawMessage(
			`{"type":"object","properties":{"query":{"type":"string","description":"поисковый запрос"}},"required":["query"]}`,
		),
		Execute: c.searchTool,
	}
}

func (c *Client) searchTool(ctx context.Context, args string) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("некорректные аргументы поиска: %w", err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return "", fmt.Errorf("пустой поисковый запрос")
	}

	results, err := c.search(ctx, params.Query)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	for i, item := range results {
		fmt.Fprintf(&builder, "%d. %s — %s\n%s\n\n", i+1, item.Title, item.URL, truncateRunes(item.Description, maxDescriptionRunes))
	}
	if builder.Len() == 0 {
		return "По запросу ничего не найдено.", nil
	}
	return strings.TrimSpace(builder.String()), nil
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func (c *Client) search(ctx context.Context, query string) ([]searchResult, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("веб-поиск не настроен: нет FIRECRAWL_API_KEY")
	}

	payload, _ := json.Marshal(map[string]any{"query": query, "limit": maxResults})

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/search", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("создание поискового запроса: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	var response struct {
		Data struct {
			Web []searchResult `json:"web"`
		} `json:"data"`
	}
	if err := c.do(req, &response); err != nil {
		return nil, fmt.Errorf("веб-поиск: %w", err)
	}

	return response.Data.Web, nil
}

// Scrape возвращает markdown страницы. Нужен как fallback, когда trafilatura
// не справился (SPA, антибот).
func (c *Client) Scrape(ctx context.Context, targetURL string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("Firecrawl не настроен")
	}

	payload, _ := json.Marshal(map[string]any{
		"url":             targetURL,
		"formats":         []string{"markdown"},
		"onlyMainContent": true,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/scrape", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("создание запроса scrape: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Markdown string `json:"markdown"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := c.do(req, &response); err != nil {
		return "", fmt.Errorf("scrape: %w", err)
	}
	if !response.Success {
		return "", fmt.Errorf("scrape: %s", response.Error)
	}

	return strings.TrimSpace(response.Data.Markdown), nil
}

func (c *Client) do(req *http.Request, target any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return fmt.Errorf("чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateRunes(string(raw), 300))
	}

	return json.Unmarshal(raw, target)
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
