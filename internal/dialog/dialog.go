// Package dialog — контекст reply-веток в памяти: узел на сообщение, путь по родителям.
package dialog

import (
	"strings"
	"sync"
	"time"
)

const (
	defaultTTL         = 100 * time.Minute
	defaultMaxChars    = 100_000
	defaultMaxNodes    = 1_000
	defaultJanitorTick = time.Minute
)

// Store хранит деревья ответов по чатам.
type Store struct {
	mu       sync.Mutex
	threads  map[int64]*thread
	ttl      time.Duration
	maxChars int
	maxNodes int

	stop chan struct{}
	done chan struct{}
}

type node struct {
	parent int
	text   string
	bot    bool
}

type thread struct {
	nodes   map[int]*node
	expires time.Time
}

// NewStore собирает хранилище с дефолтными TTL и лимитами.
func NewStore() *Store {
	return &Store{
		threads:  make(map[int64]*thread),
		ttl:      defaultTTL,
		maxChars: defaultMaxChars,
		maxNodes: defaultMaxNodes,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start запускает сборщик просроченных веток.
func (s *Store) Start() {
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(defaultJanitorTick)
		defer ticker.Stop()

		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.janitor()
			}
		}
	}()
}

// Close останавливает сборщик.
func (s *Store) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

func (s *Store) janitor() {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for chatID, t := range s.threads {
		if now.After(t.expires) {
			delete(s.threads, chatID)
		}
	}
}

// Record запоминает сообщение и продлевает TTL ветки.
func (s *Store) Record(chatID int64, id, parent int, text string, fromBot bool) {
	if id == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.threads[chatID]
	if !ok || len(t.nodes) >= s.maxNodes {
		t = &thread{nodes: make(map[int]*node)}
		s.threads[chatID] = t
	}
	t.expires = time.Now().Add(s.ttl)
	t.nodes[id] = &node{parent: parent, text: strings.TrimSpace(text), bot: fromBot}
}

// UpdateText заменяет текст уже записанного сообщения.
func (s *Store) UpdateText(chatID int64, id int, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.threads[chatID]
	if !ok {
		return
	}
	if n, ok := t.nodes[id]; ok {
		n.text = strings.TrimSpace(text)
	}
}

// IsBot сообщает, отправил ли это сообщение бот.
func (s *Store) IsBot(chatID int64, id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.threads[chatID]
	if !ok {
		return false
	}
	n, ok := t.nodes[id]
	return ok && n.bot
}

// Context собирает историю от корня ветки до сообщения id включительно.
func (s *Store) Context(chatID int64, id int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.threads[chatID]
	if !ok {
		return ""
	}

	var chain []string
	seen := make(map[int]bool)
	total := 0

	for current := id; current != 0 && !seen[current]; {
		seen[current] = true

		n, ok := t.nodes[current]
		if !ok {
			break
		}
		if n.text != "" {
			total += len([]rune(n.text))
			if total > s.maxChars {
				break
			}
			chain = append(chain, n.text)
		}
		current = n.parent
	}

	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	return strings.Join(chain, "\n\n")
}
