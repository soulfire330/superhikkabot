package bot

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/soulfire330/superhikkabot/internal/config"
)

func TestStripMention(t *testing.T) {
	cases := []struct {
		text      string
		want      string
		mentioned bool
	}{
		{"@mybot что это", "что это", true},
		{"глянь @MyBot", "глянь", true},
		{"без упоминания", "без упоминания", false},
		{"@mybot", "", true},
	}

	for _, item := range cases {
		got, mentioned := stripMention(item.text, "mybot")
		if got != item.want || mentioned != item.mentioned {
			t.Errorf("stripMention(%q) = %q, %v; хотим %q, %v", item.text, got, mentioned, item.want, item.mentioned)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		text    string
		command string
		rest    string
	}{
		{"/start", "start", ""},
		{"open как дела", "open", "как дела"},
		{"tldr https://example.com/a b", "tldr", "https://example.com/a b"},
		{"/dev@mybot добавь ping", "dev", "добавь ping"},
		{"просто текст", "просто", "текст"},
	}

	for _, item := range cases {
		command, rest := splitCommand(item.text)
		if command != item.command || rest != item.rest {
			t.Errorf("splitCommand(%q) = %q, %q; хотим %q, %q", item.text, command, rest, item.command, item.rest)
		}
	}
}

func TestFirstLink(t *testing.T) {
	cases := map[string]string{
		"смотри https://example.com/post.": "https://example.com/post",
		"текст https://youtu.be/abc и ещё": "https://youtu.be/abc",
		"(https://example.com/a?b=1)":      "https://example.com/a?b=1",
		"без ссылки":                       "",
	}

	for text, want := range cases {
		if got := firstLink(text); got != want {
			t.Errorf("firstLink(%q) = %q, хотим %q", text, got, want)
		}
	}
}

func TestAllowed(t *testing.T) {
	router := New(Deps{Config: &config.Config{Telegram: config.Telegram{
		AllowedChats: []int64{-100},
		AdminUsers:   []int64{5},
	}}})

	cases := []struct {
		name string
		msg  *models.Message
		want bool
	}{
		{"разрешённый чат", &models.Message{Chat: models.Chat{ID: -100}, From: &models.User{ID: 1}}, true},
		{"личка админа", &models.Message{Chat: models.Chat{ID: 5, Type: "private"}, From: &models.User{ID: 5}}, true},
		{"админ в чужой группе", &models.Message{Chat: models.Chat{ID: -200, Type: "supergroup"}, From: &models.User{ID: 5}}, false},
		{"посторонний в личке", &models.Message{Chat: models.Chat{ID: 9, Type: "private"}, From: &models.User{ID: 9}}, false},
		{"посторонний в группе", &models.Message{Chat: models.Chat{ID: -200}, From: &models.User{ID: 9}}, false},
	}

	for _, item := range cases {
		if got := router.allowed(item.msg); got != item.want {
			t.Errorf("%s: allowed = %v, хотели %v", item.name, got, item.want)
		}
	}
}

func TestIgnoresStrangers(t *testing.T) {
	router := New(Deps{Config: &config.Config{Telegram: config.Telegram{AllowedChats: []int64{-100}}}})

	// Зависимости роутера nil: если посторонний проскочит фильтр, будет паника.
	// Ничего не должно произойти — в этом и проверка.
	messages := []*models.Message{
		{Chat: models.Chat{ID: 7, Type: "private"}, From: &models.User{ID: 7}, Text: "/start"},
		{Chat: models.Chat{ID: -200}, From: &models.User{ID: 8}, Text: "/dev добавь ping"},
		{Chat: models.Chat{ID: 9, Type: "private"}, From: &models.User{ID: 9}, Text: "привет"},
	}

	for _, msg := range messages {
		router.Handle(context.Background(), msg)
	}
}
