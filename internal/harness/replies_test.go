package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplyStoreRememberAndReload(t *testing.T) {
	dir := t.TempDir()
	store := newReplyStore(dir)
	store.remember(42, []int{10, 11}, "/sessions/a.jsonl")

	if session, ok := store.session(42, 10); !ok || session != "/sessions/a.jsonl" {
		t.Fatalf("сессия первого чанка: %q, %v", session, ok)
	}
	if session, ok := store.session(42, 11); !ok || session != "/sessions/a.jsonl" {
		t.Fatalf("сессия второго чанка: %q, %v", session, ok)
	}
	if _, ok := store.session(42, 12); ok {
		t.Fatal("чужая сессия не должна находиться")
	}
	if _, ok := store.session(43, 10); ok {
		t.Fatal("чужой чат не должен находиться")
	}

	if _, err := os.Stat(filepath.Join(dir, "replies.json")); err != nil {
		t.Fatalf("карта не сохранилась: %v", err)
	}

	reloaded := newReplyStore(dir)
	if session, ok := reloaded.session(42, 11); !ok || session != "/sessions/a.jsonl" {
		t.Fatalf("после перезагрузки: %q, %v", session, ok)
	}
}
