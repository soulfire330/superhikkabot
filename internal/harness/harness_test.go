package harness

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePi отвечает на команды и prompt так же, как pi в режиме rpc.
const fakePi = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"new_session"'*)
      printf '{"id":"%s","type":"response","command":"new_session","success":true}\n' "$id" ;;
    *'"type":"get_state"'*)
      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionFile":"/tmp/pi-tg-test.jsonl"}}\n' "$id" ;;
    *'"type":"switch_session"'*)
      printf '{"id":"%s","type":"response","command":"switch_session","success":true}\n' "$id" ;;
    *)
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[]}}'
      printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"готово"},{"type":"text","text":"и ещё"}]}}'
      printf '%s\n' '{"type":"agent_settled","aborted":false}' ;;
  esac
done
`

func writeFake(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("не записался фейковый pi: %v", err)
	}
	return path
}

func TestRunnerReadsAnswer(t *testing.T) {
	runner := New(Config{
		Binary:  writeFake(t, fakePi),
		Timeout: 5 * time.Second,
	}, log.New(os.Stderr, "", 0))
	defer runner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := runner.Run(ctx, 42, "почини это")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "готово\nи ещё"; reply.Text != want {
		t.Errorf("ответ %q, хотим %q", reply.Text, want)
	}
	if want := "/tmp/pi-tg-test.jsonl"; reply.SessionPath != want {
		t.Errorf("сессия %q, хотим %q", reply.SessionPath, want)
	}
}

func TestRunnerContinuesSession(t *testing.T) {
	runner := New(Config{
		Binary:  writeFake(t, fakePi),
		Timeout: 5 * time.Second,
	}, log.New(os.Stderr, "", 0))
	defer runner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := runner.Continue(ctx, 42, "/tmp/pi-tg-test.jsonl", "продолжай")
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if reply.Text == "" || reply.SessionPath != "/tmp/pi-tg-test.jsonl" {
		t.Errorf("неожиданный ответ: %+v", reply)
	}
}

func TestRunnerReportsProcessExit(t *testing.T) {
	runner := New(Config{
		Binary:  writeFake(t, "#!/bin/sh\nexit 3\n"),
		Timeout: 5 * time.Second,
	}, log.New(os.Stderr, "", 0))
	defer runner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := runner.Run(ctx, 1, "задача"); err == nil {
		t.Fatal("для упавшего процесса ждали ошибку")
	}
}

func TestRunnerReportsFailedResponse(t *testing.T) {
	body := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"new_session"'*)
      printf '{"id":"%s","type":"response","command":"new_session","success":true}\n' "$id" ;;
    *'"type":"get_state"'*)
      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionFile":"/tmp/pi-tg-test.jsonl"}}\n' "$id" ;;
    *)
      printf '{"id":"%s","type":"response","command":"prompt","success":false,"error":"нет модели"}\n' "$id" ;;
  esac
done
`
	runner := New(Config{Binary: writeFake(t, body), Timeout: 5 * time.Second}, log.New(os.Stderr, "", 0))
	defer runner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runner.Run(ctx, 7, "задача")
	if err == nil || !strings.Contains(err.Error(), "нет модели") {
		t.Fatalf("ждали ошибку с текстом pi, получили %v", err)
	}
}

func TestMessageTextHandlesPlainString(t *testing.T) {
	if got := messageText([]byte(`"просто текст"`)); got != "просто текст" {
		t.Errorf("получили %q", got)
	}
	if got := messageText([]byte(`[{"type":"thinking","text":"думаю"},{"type":"text","text":"ответ"}]`)); got != "ответ" {
		t.Errorf("получили %q", got)
	}
}

func TestRunnerReportsProgress(t *testing.T) {
	body := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"new_session"'*)
      printf '{"id":"%s","type":"response","command":"new_session","success":true}\n' "$id" ;;
    *'"type":"get_state"'*)
      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionFile":"/tmp/pi-tg-test.jsonl"}}\n' "$id" ;;
    *)
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      printf '%s\n' '{"type":"agent_start"}'
      printf '%s\n' '{"type":"turn_start"}'
      printf '%s\n' '{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"go test\n ./..."}}'
      printf '%s\n' '{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":false,"durationMs":1200}'
      printf '%s\n' '{"type":"tool_execution_start","toolCallId":"c2","toolName":"edit","args":{"path":"internal/bot/status.go"}}'
      printf '%s\n' '{"type":"tool_execution_end","toolCallId":"c2","toolName":"edit","isError":true,"durationMs":30}'
      printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}'
      printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"готово"}]}}'
      printf '%s\n' '{"type":"agent_settled","aborted":false}' ;;
  esac
done
`
	runner := New(Config{Binary: writeFake(t, body), Timeout: 5 * time.Second}, log.New(os.Stderr, "", 0))
	defer runner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []Progress
	reply, err := runner.RunWithProgress(ctx, 3, "задача", func(p Progress) {
		events = append(events, p)
	})
	if err != nil {
		t.Fatalf("RunWithProgress: %v", err)
	}
	if reply.Text != "готово" {
		t.Errorf("ответ %q", reply.Text)
	}

	want := []Progress{
		{Kind: "start"},
		{Kind: "turn"},
		{Kind: "tool", Tool: "bash", Detail: "go test ./..."},
		{Kind: "tool_done", Tool: "bash", DurationMs: 1200},
		{Kind: "tool", Tool: "edit", Detail: "internal/bot/status.go"},
		{Kind: "tool_done", Tool: "edit", Failed: true, DurationMs: 30},
		{Kind: "thinking"},
	}
	if len(events) != len(want) {
		t.Fatalf("события %+v, хотим %+v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("событие %d: %+v, хотим %+v", i, events[i], want[i])
		}
	}
}

func TestToolDetailPicksSpeakingField(t *testing.T) {
	cases := map[string]string{
		`{"command":"go  test\n./..."}`:  "go test ./...",
		`{"path":"internal/bot/bot.go"}`: "internal/bot/bot.go",
		`{"file_path":"README.md"}`:      "README.md",
		`{"pattern":"Working"}`:          "Working",
		`{"url":"https://example.com"}`:  "https://example.com",
		`{"unknown":"ignored"}`:          "",
		`[]`:                             "",
	}
	for args, want := range cases {
		if got := toolDetail([]byte(args)); got != want {
			t.Errorf("toolDetail(%s) = %q, хотим %q", args, got, want)
		}
	}
}

func TestAgentEnvPassesFirecrawl(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "sk-test")

	env := agentEnv(Config{
		FirecrawlURL: "http://10.20.20.34:3002",
		FirecrawlKey: "fc-local",
	})
	joined := "\n" + strings.Join(env, "\n") + "\n"

	for _, want := range []string{
		"OPENCODE_API_KEY=sk-test",
		"FIRECRAWL_API_KEY=fc-local",
		"FIRECRAWL_API_URL=http://10.20.20.34:3002",
	} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("нет %q в окружении агента: %v", want, env)
		}
	}
	if strings.Contains(joined, "\nTELEGRAM_BOT_TOKEN=") {
		t.Error("секрет бота утёк агенту")
	}
}

func TestAgentEnvSkipsEmptyFirecrawl(t *testing.T) {
	env := agentEnv(Config{})
	for _, item := range env {
		if strings.HasPrefix(item, "FIRECRAWL_") {
			t.Errorf("лишняя переменная без конфига: %q", item)
		}
	}
}
