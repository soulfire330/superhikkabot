package digest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/markusmobius/go-trafilatura"
)

const (
	browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxPageBytes     = 5 << 20
	minWebContent    = 150
)

// webpage достаёт основной текст страницы: сначала trafilatura, потом Firecrawl.
func (c *Client) webpage(ctx context.Context, source string) (string, error) {
	content, localErr := c.trafilatura(ctx, source)
	if localErr == nil && len(strings.TrimSpace(content)) >= minWebContent {
		return content, nil
	}

	if c.firecrawl != nil && c.firecrawl.Enabled() {
		scraped, err := c.firecrawl.Scrape(ctx, source)
		if err == nil && len(strings.TrimSpace(scraped)) >= minWebContent {
			return scraped, nil
		}
		if err != nil {
			return "", fmt.Errorf("страница: trafilatura (%v), firecrawl (%v)", localErr, err)
		}
	}

	if localErr != nil {
		return "", fmt.Errorf("страница: %w", localErr)
	}
	return "", fmt.Errorf("страница почти без текста (меньше %d символов)", minWebContent)
}

func (c *Client) trafilatura(ctx context.Context, source string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("запрос страницы: %w", err)
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("запрос страницы: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("страница не найдена")
	case http.StatusForbidden, http.StatusUnauthorized:
		return "", fmt.Errorf("страница закрыта (HTTP %d)", resp.StatusCode)
	default:
		return "", fmt.Errorf("страница вернула HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", fmt.Errorf("чтение страницы: %w", err)
	}

	extracted, err := trafilatura.Extract(bytes.NewReader(body), trafilatura.Options{})
	if err != nil {
		return "", fmt.Errorf("trafilatura: %w", err)
	}

	return strings.TrimSpace(extracted.ContentText), nil
}
