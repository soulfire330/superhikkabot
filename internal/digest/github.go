package digest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	githubAPI      = "https://api.github.com"
	githubRaw      = "https://raw.githubusercontent.com"
	githubMaxTree  = 400
	githubMaxDepth = 3
	githubMaxChars = 20_000
)

var agentFiles = []string{"AGENTS.md", "CONTEXT.md", "CLAUDE.md"}

// github собирает метаданные репозитория, дерево файлов, README и инструкции агента.
func (c *Client) github(ctx context.Context, source string) (string, error) {
	owner, repo, err := repoInfo(source)
	if err != nil {
		return "", err
	}

	var meta struct {
		FullName        string   `json:"full_name"`
		Description     string   `json:"description"`
		Language        string   `json:"language"`
		Topics          []string `json:"topics"`
		StargazersCount int      `json:"stargazers_count"`
		DefaultBranch   string   `json:"default_branch"`
	}
	metaURL := fmt.Sprintf("%s/repos/%s/%s", githubAPI, owner, repo)
	if err := c.getJSON(ctx, metaURL, &meta); err != nil {
		return "", fmt.Errorf("repository %s/%s: %w", owner, repo, err)
	}

	branch := meta.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "Репозиторий: %s\n", meta.FullName)
	if meta.Description != "" {
		fmt.Fprintf(&builder, "Описание: %s\n", meta.Description)
	}
	if meta.Language != "" {
		fmt.Fprintf(&builder, "Основной язык: %s\n", meta.Language)
	}
	if len(meta.Topics) > 0 {
		fmt.Fprintf(&builder, "Темы: %s\n", strings.Join(meta.Topics, ", "))
	}
	fmt.Fprintf(&builder, "Звёзды: %d\n", meta.StargazersCount)

	if readme := c.readme(ctx, owner, repo); readme != "" {
		fmt.Fprintf(&builder, "\nREADME:\n%s\n", truncateRunes(readme, githubMaxChars))
	}

	tree, err := c.tree(ctx, owner, repo, branch)
	if err == nil && tree != "" {
		fmt.Fprintf(&builder, "\nФайлы:\n%s\n", tree)
	}

	for _, name := range agentFiles {
		if content := c.file(ctx, owner, repo, branch, name); content != "" {
			fmt.Fprintf(&builder, "\n%s:\n%s\n", name, truncateRunes(content, githubMaxChars))
			break
		}
	}

	return builder.String(), nil
}

func (c *Client) readme(ctx context.Context, owner, repo string) string {
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}

	url := fmt.Sprintf("%s/repos/%s/%s/readme", githubAPI, owner, repo)
	if err := c.getJSON(ctx, url, &payload); err != nil {
		return ""
	}
	if payload.Encoding != "base64" {
		return ""
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return ""
	}
	return string(decoded)
}

func (c *Client) tree(ctx context.Context, owner, repo, branch string) (string, error) {
	var payload struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}

	url := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", githubAPI, owner, repo, branch)
	if err := c.getJSON(ctx, url, &payload); err != nil {
		return "", err
	}

	var builder strings.Builder
	for i, entry := range payload.Tree {
		if i >= githubMaxTree {
			builder.WriteString("…\n")
			break
		}

		depth := strings.Count(entry.Path, "/") + 1
		if depth > githubMaxDepth {
			continue
		}

		indent := strings.Repeat("  ", depth-1)
		name := path.Base(entry.Path)
		if entry.Type == "tree" {
			name += "/"
		}
		fmt.Fprintf(&builder, "%s%s\n", indent, name)
	}

	return strings.TrimSpace(builder.String()), nil
}

func (c *Client) file(ctx context.Context, owner, repo, branch, name string) string {
	url := fmt.Sprintf("%s/%s/%s/%s/%s", githubRaw, owner, repo, branch, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	return string(body)
}

func (c *Client) getJSON(ctx context.Context, target string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dst)
}

// repoInfo достаёт owner и repo из ссылки на github.com.
func repoInfo(source string) (string, string, error) {
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		source = "https://" + source
	}

	u, err := url.Parse(source)
	if err != nil {
		return "", "", fmt.Errorf("invalid GitHub link: %w", err)
	}

	if host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."); host != "github.com" {
		return "", "", fmt.Errorf("this is not github.com")
	}

	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("link has no owner/repo")
	}

	return parts[0], parts[1], nil
}
