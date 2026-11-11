package tg

import "testing"

func TestMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "жирный и курсив",
			in:   "Это **жирный** и *курсив* текст",
			want: "Это <b>жирный</b> и <i>курсив</i> текст",
		},
		{
			name: "код и экранирование",
			in:   "Запусти `go test ./...` и смотри <b>",
			want: "Запусти <code>go test ./...</code> и смотри &lt;b&gt;",
		},
		{
			name: "ссылка",
			in:   "Смотри [доки](https://example.com/a?b=1&c=2) там",
			want: `Смотри <a href="https://example.com/a?b=1&amp;c=2">доки</a> там`,
		},
		{
			name: "блок кода",
			in:   "Пример:\n```go\nfmt.Println(\"<ok>\")\n```\nготово",
			want: "Пример:\n<pre><code class=\"language-go\">fmt.Println(\"&lt;ok&gt;\")</code></pre>\nготово",
		},
		{
			name: "заголовок и список",
			in:   "# Итог\n\n- первое\n- второе",
			want: "<b>Итог</b>\n\n• первое\n• второе",
		},
		{
			name: "нумерованный список",
			in:   "1. один\n2. два",
			want: "1. один\n2. два",
		},
		{
			name: "цитата",
			in:   "> важно\n> очень",
			want: "<blockquote>важно\nочень</blockquote>",
		},
		{
			name: "зачёркнутый и спойлер",
			in:   "~~старое~~ и ||секрет||",
			want: `<s>старое</s> и <span class="tg-spoiler">секрет</span>`,
		},
		{
			name: "подчёркивания в именах не курсив",
			in:   "файл bot_markdown.go и snake_case",
			want: "файл bot_markdown.go и snake_case",
		},
		{
			name: "звёздочки как умножение",
			in:   "2 * 3 * 4 = 24",
			want: "2 * 3 * 4 = 24",
		},
		{
			name: "линия",
			in:   "до\n---\nпосле",
			want: "до\n──────────\nпосле",
		},
		{
			name: "таблица",
			in:   "| a | b |\n|---|:--:|\n| 1 | 2 |",
			want: "a · b\n1 · 2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Markdown(test.in); got != test.want {
				t.Errorf("Markdown(%q)\n got: %q\nwant: %q", test.in, got, test.want)
			}
		})
	}
}
