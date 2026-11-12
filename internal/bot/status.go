package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/soulfire330/superhikkabot/internal/harness"
	"github.com/soulfire330/superhikkabot/internal/tg"
)

const (
	// statusInterval — как часто перерисовывать статус, даже если агент молчит:
	// по таймеру видно, что прогон жив, а не завис.
	statusInterval = 5 * time.Second

	// minStatusGap — не чаще одной правки за этот промежуток по событиям агента:
	// Telegram плохо переносит частые правки одного сообщения.
	minStatusGap = 1500 * time.Millisecond

	// detailLimit — сколько символов аргумента инструмента показывать в статусе.
	detailLimit = 80
)

// runStatus переписывает сообщение-заглушку текущим занятием агента.
// События приходят синхронно из прогона pi, а правки шлёт отдельная горутина:
// сеть не должна тормозить разбор событий агента.
type runStatus struct {
	bot       *Bot
	ctx       context.Context
	chatID    int64
	messageID int
	start     time.Time

	mu   sync.Mutex
	line string    // текущее действие, например "🔧 running go test ./..."
	sent string    // последний отправленный текст
	at   time.Time // когда отправляли в прошлый раз

	wake    chan struct{}
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// newRunStatus запускает отрисовку. Без сообщения-заглушки возвращает nil:
// методы nil-безопасны, и прогон просто идёт без статуса.
func newRunStatus(ctx context.Context, b *Bot, chatID int64, messageID int) *runStatus {
	if b == nil || b.deps.TG == nil || messageID == 0 {
		return nil
	}

	status := &runStatus{
		bot:       b,
		ctx:       ctx,
		chatID:    chatID,
		messageID: messageID,
		start:     time.Now(),
		line:      "🧠 starting the agent",
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	go status.loop()
	return status
}

// Report принимает событие pi и запоминает, что показать. Он вызывается из
// ожидания прогона, поэтому только обновляет состояние и будит отрисовку.
func (s *runStatus) Report(p harness.Progress) {
	if s == nil {
		return
	}

	line := statusLine(p)
	if line == "" {
		return
	}

	s.mu.Lock()
	s.line = line
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Stop гасит отрисовку и дожидается её: иначе поздняя правка перезапишет ответ.
func (s *runStatus) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		<-s.stopped
	})
}

func (s *runStatus) loop() {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	defer close(s.stopped)

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if !s.flush() {
				return
			}
		case <-s.wake:
			if !s.flush() {
				return
			}
		}
	}
}

// flush отправляет статус, если он изменился и прошло достаточно времени.
// false — правки больше не пройдут (сообщение удалили), горутине пора спать.
func (s *runStatus) flush() bool {
	text := s.render()

	s.mu.Lock()
	if text == "" || text == s.sent || time.Since(s.at) < minStatusGap {
		s.mu.Unlock()
		return true
	}
	s.sent = text
	s.at = time.Now()
	s.mu.Unlock()

	if err := s.bot.deps.TG.Edit(s.ctx, s.chatID, s.messageID, text); err != nil {
		if strings.Contains(err.Error(), "not modified") {
			return true
		}
		if s.bot.deps.Logger != nil {
			s.bot.deps.Logger.Printf("статус: %v", err)
		}
		return false
	}
	return true
}

func (s *runStatus) render() string {
	s.mu.Lock()
	line := s.line
	s.mu.Unlock()

	head := "⏳ Working… " + elapsedText(time.Since(s.start))
	if line == "" {
		return head
	}
	return head + "\n" + line
}

// statusLine переводит событие pi в человеческую строку. Пустая строка —
// событие не стоит показывать.
func statusLine(p harness.Progress) string {
	switch p.Kind {
	case "start":
		return "🧠 starting the agent"
	case "thinking":
		return "🧠 thinking…"
	case "answer":
		return "💬 writing the answer…"
	case "turn":
		return "🔄 next step…"
	case "tool":
		return toolLine(p.Tool, p.Detail)
	case "tool_done":
		return toolDoneLine(p)
	}
	return ""
}

func toolLine(tool, detail string) string {
	verbs := map[string]string{
		"bash":       "🔧 running",
		"read":       "📖 reading",
		"edit":       "✏️ editing",
		"write":      "📝 writing",
		"grep":       "🔎 searching",
		"glob":       "🔎 searching",
		"ls":         "📁 listing",
		"webfetch":   "🌐 fetching",
		"web_search": "🌐 searching the web",
		"task":       "🤖 delegating",
	}

	verb := verbs[tool]
	if verb == "" {
		verb = "🛠 " + tg.Escape(tool)
	}

	detail = shortDetail(detail)
	if detail == "" {
		return verb
	}
	return verb + " <code>" + tg.Escape(detail) + "</code>"
}

func toolDoneLine(p harness.Progress) string {
	name := tg.Escape(p.Tool)
	if p.Failed {
		return "⚠️ " + name + " failed"
	}

	line := "✓ " + name
	if p.DurationMs > 0 {
		line += " " + elapsedText(time.Duration(p.DurationMs)*time.Millisecond)
	}
	return line
}

// shortDetail делает аргумент инструмента пригодным для одной строки.
func shortDetail(detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	runes := []rune(detail)
	if len(runes) > detailLimit {
		detail = string(runes[:detailLimit]) + "…"
	}
	return detail
}

// elapsedText печатает длительность как 0:42 или 1:02:03.
func elapsedText(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	if total < 0 {
		total = 0
	}
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, total%3600/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
