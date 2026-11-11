package bot

import "testing"

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
