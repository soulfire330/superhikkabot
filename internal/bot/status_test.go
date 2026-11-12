package bot

import (
	"testing"
	"time"

	"github.com/soulfire330/superhikkabot/internal/harness"
)

func TestStatusLine(t *testing.T) {
	cases := []struct {
		name string
		in   harness.Progress
		want string
	}{
		{"старт", harness.Progress{Kind: "start"}, "🧠 starting the agent"},
		{"думает", harness.Progress{Kind: "thinking"}, "🧠 thinking…"},
		{"пишет", harness.Progress{Kind: "answer"}, "💬 writing the answer…"},
		{
			"bash",
			harness.Progress{Kind: "tool", Tool: "bash", Detail: "go test ./..."},
			"🔧 running <code>go test ./...</code>",
		},
		{
			"правка",
			harness.Progress{Kind: "tool", Tool: "edit", Detail: "internal/bot/status.go"},
			"✏️ editing <code>internal/bot/status.go</code>",
		},
		{
			"чтение без аргумента",
			harness.Progress{Kind: "tool", Tool: "read"},
			"📖 reading",
		},
		{
			"неизвестный инструмент",
			harness.Progress{Kind: "tool", Tool: "mystery", Detail: "<b>"},
			"🛠 mystery <code>&lt;b&gt;</code>",
		},
		{
			"успех",
			harness.Progress{Kind: "tool_done", Tool: "bash", DurationMs: 1200},
			"✓ bash 0:01",
		},
		{
			"провал",
			harness.Progress{Kind: "tool_done", Tool: "bash", Failed: true},
			"⚠️ bash failed",
		},
		{"пусто", harness.Progress{Kind: "unknown"}, ""},
	}

	for _, item := range cases {
		if got := statusLine(item.in); got != item.want {
			t.Errorf("%s: получили %q, хотим %q", item.name, got, item.want)
		}
	}
}

func TestShortDetail(t *testing.T) {
	long := make([]rune, 0, 120)
	for i := 0; i < 120; i++ {
		long = append(long, 'ж')
	}

	if got := shortDetail("  go\ttest\n./...  "); got != "go test ./..." {
		t.Errorf("схлопывание: %q", got)
	}
	if got := shortDetail(string(long)); len([]rune(got)) != detailLimit+1 {
		t.Errorf("обрезка: %d рун", len([]rune(got)))
	}
}

func TestElapsedText(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0:00",
		42 * time.Second: "0:42",
		90 * time.Second: "1:30",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
	}
	for in, want := range cases {
		if got := elapsedText(in); got != want {
			t.Errorf("elapsedText(%v) = %q, хотим %q", in, got, want)
		}
	}
}

func TestRunStatusNilSafe(t *testing.T) {
	var status *runStatus
	status.Report(harness.Progress{Kind: "start"})
	status.Stop()
}
