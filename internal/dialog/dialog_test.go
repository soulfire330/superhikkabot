package dialog

import "testing"

func TestContextFollowsReplyChain(t *testing.T) {
	store := NewStore()

	store.Record(1, 10, 0, "первое", false)
	store.Record(1, 11, 10, "второе", false)
	store.Record(1, 12, 11, "третье", false)

	got := store.Context(1, 12)
	want := "первое\n\nвторое\n\nтретье"
	if got != want {
		t.Errorf("Context = %q, хотим %q", got, want)
	}
}

func TestContextKeepsBranchesSeparate(t *testing.T) {
	store := NewStore()

	store.Record(1, 10, 0, "корень", false)
	store.Record(1, 11, 10, "ветка А", false)
	store.Record(1, 12, 11, "продолжение А", false)
	store.Record(1, 13, 10, "ветка Б", false)

	if got, want := store.Context(1, 12), "корень\n\nветка А\n\nпродолжение А"; got != want {
		t.Errorf("ветка А = %q, хотим %q", got, want)
	}
	if got, want := store.Context(1, 13), "корень\n\nветка Б"; got != want {
		t.Errorf("ветка Б = %q, хотим %q", got, want)
	}
}

func TestChunkAliasesOfOneAnswer(t *testing.T) {
	store := NewStore()

	store.Record(2, 20, 0, "вопрос", false)
	store.Record(2, 21, 20, "длинный ответ", true)
	store.Record(2, 22, 21, "", true)

	if !store.IsBot(2, 22) {
		t.Fatal("второй чанк должен считаться сообщением бота")
	}
	if got, want := store.Context(2, 22), "вопрос\n\nдлинный ответ"; got != want {
		t.Errorf("Context = %q, хотим %q", got, want)
	}
}

func TestContextUnknownChat(t *testing.T) {
	store := NewStore()
	if got := store.Context(99, 1); got != "" {
		t.Errorf("для неизвестного чата ждали пустоту, получили %q", got)
	}
	if store.IsBot(99, 1) {
		t.Error("для неизвестного чата ботом быть никто не может")
	}
}
