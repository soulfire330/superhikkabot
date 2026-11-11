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

// fakePi отвечает на prompt так же, как pi в режиме rpc: ответ, сообщение, agent_settled.
const fakePi = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' '{"type":"response","command":"prompt","success":true}'
  printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[]}}'
  printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"готово"},{"type":"text","text":"и ещё"}]}}'
  printf '%s\n' '{"type":"agent_settled","aborted":false}'
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

	answer, err := runner.Run(ctx, 42, "почини это")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "готово\nи ещё"; answer != want {
		t.Errorf("ответ %q, хотим %q", answer, want)
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
  printf '%s\n' '{"type":"response","command":"prompt","success":false,"error":"нет модели"}'
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
