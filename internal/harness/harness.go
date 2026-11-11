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
}

// Runner держит по одному процессу pi на чат.
type Runner struct {
	cfg    Config
	logger *log.Logger

	// run сериализует задачи: pi правит один репозиторий, два агента там только мешают.
	run   sync.Mutex
	mu    sync.Mutex
	procs map[int64]*proc
}

type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan record
	exited chan struct{}
}

type record struct {
	Type    string `json:"type"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Aborted bool   `json:"aborted"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
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

	return &Runner{cfg: cfg, logger: logger, procs: make(map[int64]*proc)}
}

// Run отправляет задачу агенту и ждёт, пока он закончит, возвращая финальный текст.
func (r *Runner) Run(ctx context.Context, chatID int64, prompt string) (string, error) {
	r.run.Lock()
	defer r.run.Unlock()

	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	process, err := r.process(chatID)
	if err != nil {
		return "", err
	}

	command, err := json.Marshal(map[string]any{"type": "prompt", "message": prompt})
	if err != nil {
		return "", err
	}

	if _, err := process.stdin.Write(append(command, '\n')); err != nil {
		r.drop(chatID)
		return "", fmt.Errorf("pi: sending the task: %w", err)
	}

	return r.wait(ctx, chatID, process)
}

func (r *Runner) wait(ctx context.Context, chatID int64, process *proc) (string, error) {
	var answer string

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("pi: %w", ctx.Err())

		case <-process.exited:
			r.drop(chatID)
			return "", errors.New("pi: process exited")

		case event := <-process.events:
			switch event.Type {
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
	cmd.Env = agentEnv()

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

// agentEnv оставляет агенту PATH, HOME и ключи провайдеров, но не секреты бота.
func agentEnv() []string {
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

	return env
}

func tail(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}
