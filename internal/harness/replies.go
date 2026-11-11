package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxReplyRefs ограничивает карту: старые привязки вытесняются, сессии на диске остаются.
const maxReplyRefs = 500

// replyRef — одно сообщение бота, за которым стоит pi-сессия.
type replyRef struct {
	ChatID  int64  `json:"chat_id"`
	Session string `json:"session"`
	Updated int64  `json:"updated"`
}

// replyStore связывает id сообщений с сессиями и переживает перезапуск бота.
type replyStore struct {
	mu    sync.Mutex
	path  string
	items map[string]replyRef
}

func newReplyStore(sessionDir string) *replyStore {
	store := &replyStore{items: make(map[string]replyRef)}
	if sessionDir != "" {
		store.path = filepath.Join(sessionDir, "replies.json")
		store.load()
	}
	return store
}

func replyKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func (s *replyStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}

	var items map[string]replyRef
	if json.Unmarshal(data, &items) != nil {
		return
	}
	for key, ref := range items {
		if ref.Session != "" {
			s.items[key] = ref
		}
	}
}

// remember привязывает все сообщения ответа к одной сессии.
func (s *replyStore) remember(chatID int64, messageIDs []int, session string) {
	if session == "" || len(messageIDs) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	for _, id := range messageIDs {
		if id == 0 {
			continue
		}
		s.items[replyKey(chatID, id)] = replyRef{ChatID: chatID, Session: session, Updated: now}
	}
	s.prune()
	s.save()
}

// session отдаёт сессию, продолжением которой является реплай.
func (s *replyStore) session(chatID int64, messageID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, ok := s.items[replyKey(chatID, messageID)]
	if !ok || ref.Session == "" {
		return "", false
	}
	return ref.Session, true
}

// prune вытесняет самые старые привязки, чтобы файл не рос бесконечно.
func (s *replyStore) prune() {
	for len(s.items) > maxReplyRefs {
		oldestKey, oldest := "", int64(0)
		for key, ref := range s.items {
			if oldestKey == "" || ref.Updated < oldest {
				oldestKey, oldest = key, ref.Updated
			}
		}
		delete(s.items, oldestKey)
	}
}

func (s *replyStore) save() {
	if s.path == "" {
		return
	}

	data, err := json.Marshal(s.items)
	if err != nil {
		return
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// RememberReply запоминает, из какой сессии пришёл ответ в эти сообщения.
func (r *Runner) RememberReply(chatID int64, messageIDs []int, sessionPath string) {
	if r == nil {
		return
	}
	r.replies.remember(chatID, messageIDs, sessionPath)
}

// ReplySession говорит, какую сессию продолжает реплай на сообщение бота.
func (r *Runner) ReplySession(chatID int64, messageID int) (string, bool) {
	if r == nil {
		return "", false
	}
	return r.replies.session(chatID, messageID)
}
