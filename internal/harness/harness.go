// Package harness — мост к pi в режиме RPC и самоперезапуск бота после правок.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// providerKeys — единственные секреты, которые видит агент.
var providerKeys = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"OPENROUTER_API_KEY",
	"OPENCODE_API_KEY",
	"GEMINI_API_KEY",
	"GROQ_API_KEY",
	"DEEPSEEK_API_KEY",
	"XAI_API_KEY",
	"CEREBRAS_API_KEY",
	"MISTRAL_API_KEY",
	"AI_GATEWAY_API_KEY",
}

// Config — где живёт агент и сколько ему можно работать.
type Config struct {
	Binary     string
	Workspace  string
	SessionDir string
	Timeout    time.Duration
	Deploy     bool
	// Firecrawl — тот же веб-поиск/scrape, что у бота: агент видит его как
	// инструменты расширения pi-firecrawl.
	FirecrawlURL string
	FirecrawlKey string
}

// Runner держит по одному процессу pi на чат.
type Runner struct {
	cfg    Config
	logger *log.Logger

	// run сериализует задачи: pi правит один репозиторий, два агента там только мешают.
	run   sync.Mutex
	mu    sync.Mutex
	procs map[int64]*proc

	// replies помнит, какая pi-сессия стоит за сообщением бота.
	replies *replyStore
}

type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan record
	exited chan struct{}
}

type record struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Command    string          `json:"command"`
	Success    *bool           `json:"success"`
	Error      string          `json:"error"`
	Data       json.RawMessage `json:"data"`
	Aborted    bool            `json:"aborted"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	IsError    bool            `json:"isError"`
	DurationMs int64           `json:"durationMs"`
	Message    struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	AssistantMessageEvent struct {
		Type string `json:"type"`
	} `json:"assistantMessageEvent"`
}

// New собирает раннер.
func New(cfg Config, logger *log.Logger) *Runner {
	if cfg.Binary == "" {
		cfg.Binary = "pi"
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "."
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Minute
	}

	return &Runner{
		cfg:     cfg,
		logger:  logger,
		procs:   make(map[int64]*proc),
		replies: newReplyStore(cfg.SessionDir),
	}
}

// Reply — итог прогона агента: текст ответа и сессия, которую он продолжил.
type Reply struct {
	Text        string
	SessionPath string
}

// Progress — событие прогона агента, из которого бот рисует живую строку статуса.
type Progress struct {
	Kind       string // start, thinking, answer, turn, tool, tool_done
	Tool       string
	Detail     string
	Failed     bool
	DurationMs int64
}

// ProgressFunc получает прогресс по мере того, как pi шлёт события.
// Вызывается синхронно из ожидания прогона: колбэк должен быть быстрым.
type ProgressFunc func(Progress)

// Run начинает новую pi-сессию, отправляет задачу и ждёт финальный текст.
func (r *Runner) Run(ctx context.Context, chatID int64, prompt string) (Reply, error) {
	return r.runIn(ctx, chatID, "", prompt, nil)
}

// RunWithProgress — то же, что Run, но с отчётом о ходе работы.
func (r *Runner) RunWithProgress(ctx context.Context, chatID int64, prompt string, progress ProgressFunc) (Reply, error) {
	return r.runIn(ctx, chatID, "", prompt, progress)
}

// Continue продолжает конкретную сессию — так реплай возвращается в свой dev-ран.
func (r *Runner) Continue(ctx context.Context, chatID int64, sessionPath, prompt string) (Reply, error) {
	return r.ContinueWithProgress(ctx, chatID, sessionPath, prompt, nil)
}

// ContinueWithProgress — то же, что Continue, но с отчётом о ходе работы.
func (r *Runner) ContinueWithProgress(ctx context.Context, chatID int64, sessionPath, prompt string, progress ProgressFunc) (Reply, error) {
	if strings.TrimSpace(sessionPath) == "" {
		return Reply{}, errors.New("pi: empty session path")
	}
	return r.runIn(ctx, chatID, sessionPath, prompt, progress)
}

func (r *Runner) runIn(ctx context.Context, chatID int64, sessionPath, prompt string, progress ProgressFunc) (Reply, error) {
	r.run.Lock()
	defer r.run.Unlock()

	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	process, err := r.process(chatID)
	if err != nil {
		return Reply{}, err
	}

	if sessionPath == "" {
		if _, err := r.sendCommand(ctx, chatID, process, map[string]any{"type": "new_session"}); err != nil {
			return Reply{}, err
		}
		if sessionPath, err = r.sessionFile(ctx, chatID, process); err != nil {
			return Reply{}, err
		}
	} else if _, err := r.sendCommand(ctx, chatID, process, map[string]any{
		"type": "switch_session", "sessionPath": sessionPath,
	}); err != nil {
		return Reply{}, err
	}

	command, err := json.Marshal(map[string]any{"type": "prompt", "message": prompt})
	if err != nil {
		return Reply{}, err
	}

	if _, err := process.stdin.Write(append(command, '\n')); err != nil {
		r.drop(chatID)
		return Reply{}, fmt.Errorf("pi: sending the task: %w", err)
	}

	answer, err := r.wait(ctx, chatID, process, progress)
	if err != nil {
		return Reply{}, err
	}
	return Reply{Text: answer, SessionPath: sessionPath}, nil
}

var commandSeq atomic.Uint64

// sendCommand шлёт RPC-команду и ждёт ответ с тем же id.
func (r *Runner) sendCommand(ctx context.Context, chatID int64, process *proc, command map[string]any) (json.RawMessage, error) {
	id := fmt.Sprintf("cmd-%d", commandSeq.Add(1))
	command["id"] = id

	raw, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	if _, err := process.stdin.Write(append(raw, '\n')); err != nil {
		r.drop(chatID)
		return nil, fmt.Errorf("pi: sending %v: %w", command["type"], err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pi: %w", ctx.Err())

		case <-process.exited:
			r.drop(chatID)
			return nil, errors.New("pi: process exited")

		case event := <-process.events:
			if event.Type != "response" || event.ID != id {
				continue
			}
			if event.Success != nil && !*event.Success {
				return nil, fmt.Errorf("pi: %s", strings.TrimSpace(event.Error))
			}
			var result struct {
				Cancelled bool `json:"cancelled"`
			}
			if json.Unmarshal(event.Data, &result) == nil && result.Cancelled {
				return nil, fmt.Errorf("pi: %s cancelled", event.Command)
			}
			return event.Data, nil
		}
	}
}

// sessionFile узнаёт у pi, в каком файле лежит текущая сессия.
func (r *Runner) sessionFile(ctx context.Context, chatID int64, process *proc) (string, error) {
	data, err := r.sendCommand(ctx, chatID, process, map[string]any{"type": "get_state"})
	if err != nil {
		return "", err
	}

	var state struct {
		SessionFile string `json:"sessionFile"`
	}
	if json.Unmarshal(data, &state) != nil || strings.TrimSpace(state.SessionFile) == "" {
		return "", errors.New("pi: session file is unknown")
	}
	return state.SessionFile, nil
}

func (r *Runner) wait(ctx context.Context, chatID int64, process *proc, progress ProgressFunc) (string, error) {
	var answer string

	emit := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("pi: %w", ctx.Err())

		case <-process.exited:
			r.drop(chatID)
			return "", errors.New("pi: process exited")

		case event := <-process.events:
			switch event.Type {
			case "agent_start":
				emit(Progress{Kind: "start"})
			case "turn_start":
				emit(Progress{Kind: "turn"})
			case "message_update":
				switch event.AssistantMessageEvent.Type {
				case "thinking_start":
					emit(Progress{Kind: "thinking"})
				case "text_start":
					emit(Progress{Kind: "answer"})
				}
			case "tool_execution_start":
				emit(Progress{Kind: "tool", Tool: event.ToolName, Detail: toolDetail(event.Args)})
			case "tool_execution_end":
				emit(Progress{Kind: "tool_done", Tool: event.ToolName, Failed: event.IsError, DurationMs: event.DurationMs})
			case "response":
				if event.Success != nil && !*event.Success {
					return "", fmt.Errorf("pi: %s", strings.TrimSpace(event.Error))
				}
			case "message_end":
				if event.Message.Role == "assistant" {
					if text := messageText(event.Message.Content); text != "" {
						answer = text
					}
				}
			case "agent_settled":
				if event.Aborted {
					return "", errors.New("pi: run aborted")
				}
				if answer == "" {
					return "", errors.New("pi: agent finished without any text")
				}
				return answer, nil
			}
		}
	}
}

// process отдаёт живой процесс для чата, поднимая его при необходимости.
func (r *Runner) process(chatID int64) (*proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.procs[chatID]; ok {
		select {
		case <-existing.exited:
			delete(r.procs, chatID)
		default:
			return existing, nil
		}
	}

	spawned, err := r.spawn(chatID)
	if err != nil {
		return nil, err
	}
	r.procs[chatID] = spawned
	return spawned, nil
}

func (r *Runner) spawn(chatID int64) (*proc, error) {
	args := []string{"--mode", "rpc", "--session-id", fmt.Sprintf("tg-%d", chatID)}
	if r.cfg.SessionDir != "" {
		args = append(args, "--session-dir", r.cfg.SessionDir)
	}

	cmd := exec.Command(r.cfg.Binary, args...)
	cmd.Dir = r.cfg.Workspace
	cmd.Env = agentEnv(r.cfg)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi: stdout: %w", err)
	}

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pi failed to start (%s): %w", r.cfg.Binary, err)
	}

	process := &proc{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan record, 128),
		exited: make(chan struct{}),
	}

	go r.read(process, stdout)

	go func() {
		err := cmd.Wait()
		if err != nil {
			r.logger.Printf("harness: pi завершился: %v: %s", err, tail(stderr.String(), 500))
		}
		close(process.exited)
	}()

	r.logger.Printf("harness: запущен pi сессии tg-%d", chatID)
	return process, nil
}

func (r *Runner) read(process *proc, stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 1<<20)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event record
			if json.Unmarshal(line, &event) == nil && event.Type != "" {
				select {
				case process.events <- event:
				default:
					// События модели не должны останавливать pi из-за медленного читателя.
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *Runner) drop(chatID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if process, ok := r.procs[chatID]; ok {
		_ = process.stdin.Close()
		if process.cmd.Process != nil {
			_ = process.cmd.Process.Kill()
		}
		delete(r.procs, chatID)
	}
}

// Stop гасит все процессы pi.
func (r *Runner) Stop() {
	r.mu.Lock()
	chats := make([]int64, 0, len(r.procs))
	for chatID := range r.procs {
		chats = append(chats, chatID)
	}
	r.mu.Unlock()

	for _, chatID := range chats {
		r.drop(chatID)
	}
}

// toolDetail достаёт из аргументов инструмента самое говорящее поле:
// команду, путь, шаблон или запрос. Многострочное схлопывается в одну строку.
func toolDetail(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}

	var fields map[string]any
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}

	for _, key := range []string{"command", "path", "file_path", "filePath", "pattern", "query", "url", "description", "prompt"} {
		value, ok := fields[key].(string)
		if !ok {
			continue
		}
		value = strings.Join(strings.Fields(value), " ")
		if value != "" {
			return clip(value, 200)
		}
	}
	return ""
}

// messageText вытаскивает текст из content: строка или блоки.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// agentEnv оставляет агенту PATH, HOME, ключи провайдеров и доступ к веб-поиску
// (Firecrawl), но не остальные секреты бота.
func agentEnv(cfg Config) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TERM=dumb",
		"GIT_TERMINAL_PROMPT=0",
	}

	for _, key := range providerKeys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}

	// pi-firecrawl читает эти переменные при загрузке расширения.
	if cfg.FirecrawlKey != "" {
		env = append(env, "FIRECRAWL_API_KEY="+cfg.FirecrawlKey)
	}
	if cfg.FirecrawlURL != "" {
		env = append(env, "FIRECRAWL_API_URL="+cfg.FirecrawlURL)
	}

	return env
}

func clip(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func tail(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}
