package tg

import (
	"fmt"
	"regexp"
	"strings"
)

// Конвертер markdown → Telegram HTML. Агент (pi) отвечает markdown, а Telegram
// понимает только свой HTML, поэтому **, #, ` и прочее иначе видны как текст.

var (
	mdFence      = regexp.MustCompile("^\\s*((?:`{3,})|(?:~{3,}))\\s*(.*?)\\s*$")
	mdHeading    = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	mdQuote      = regexp.MustCompile(`^\s*>\s?(.*)$`)
	mdBullet     = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdOrdered    = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	mdRule       = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	mdTableRule  = regexp.MustCompile(`^\s*\|?[\s:|-]+\|[\s:|-]*$`)
	mdCode       = regexp.MustCompile("`+([^`]+?)`+")
	mdLink       = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)
	mdAutoLink   = regexp.MustCompile(`<((?:https?|mailto):[^>\s]+)>`)
	mdBoldStar   = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	mdBoldUnder  = regexp.MustCompile(`__([^\n_]+)__`)
	mdStrike     = regexp.MustCompile(`~~([^\n~]+)~~`)
	mdSpoiler    = regexp.MustCompile(`\|\|([^\n|]+)\|\|`)
	mdItalicStar = regexp.MustCompile(`\*([^\s*\n](?:[^*\n]*[^\s*\n])?)\*`)
	mdItalicUnd  = regexp.MustCompile(`(^|[\s(«"'])_([^_\n]+)_($|[\s).,!?:;»"'])`)
)

// Markdown переводит markdown-ответ агента в разметку, которую понимает Telegram.
func Markdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")

	f := &formatter{}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if m := mdFence.FindStringSubmatch(line); m != nil {
			fence, info := m[1], m[2]
			lang := strings.Fields(info)
			body := make([]string, 0, 8)
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
					break
				}
				body = append(body, lines[i])
			}
			code := Escape(strings.Join(body, "\n"))
			if len(lang) > 0 {
				out = append(out, `<pre><code class="language-`+escapeAttr(lang[0])+`">`+code+`</code></pre>`)
			} else {
				out = append(out, "<pre>"+code+"</pre>")
			}
			continue
		}

		switch {
		case mdRule.MatchString(line):
			out = append(out, "──────────")

		case mdHeading.MatchString(line):
			m := mdHeading.FindStringSubmatch(line)
			out = append(out, "<b>"+f.inline(m[1])+"</b>")

		case mdQuote.MatchString(line):
			quoted := make([]string, 0, 4)
			for ; i < len(lines); i++ {
				m := mdQuote.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				quoted = append(quoted, f.inline(m[1]))
			}
			i--
			out = append(out, "<blockquote>"+strings.Join(quoted, "\n")+"</blockquote>")

		case mdBullet.MatchString(line):
			m := mdBullet.FindStringSubmatch(line)
			out = append(out, m[1]+"• "+f.inline(m[2]))

		case mdOrdered.MatchString(line):
			m := mdOrdered.FindStringSubmatch(line)
			out = append(out, m[1]+m[2]+". "+f.inline(m[3]))

		case strings.Contains(line, "|") && mdTableRule.MatchString(line):
			// Разделитель markdown-таблицы: |---|:---:| в Telegram не нужен.

		case strings.HasPrefix(strings.TrimSpace(line), "|"):
			cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
			for j := range cells {
				cells[j] = f.inline(strings.TrimSpace(cells[j]))
			}
			out = append(out, strings.Join(cells, " · "))

		default:
			out = append(out, f.inline(line))
		}
	}

	return strings.Join(out, "\n")
}

// formatter прячет готовые HTML-куски, чтобы инлайн-разметка их не трогала.
type formatter struct {
	tokens []string
}

func (f *formatter) hold(html string) string {
	f.tokens = append(f.tokens, html)
	return fmt.Sprintf("\x00%d\x00", len(f.tokens)-1)
}

func (f *formatter) restore(text string) string {
	for i, token := range f.tokens {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00%d\x00", i), token)
	}
	return text
}

func (f *formatter) inline(text string) string {
	// Код и ссылки прячем первыми: внутри них markdown не действует.
	text = mdCode.ReplaceAllStringFunc(text, func(m string) string {
		return f.hold("<code>" + Escape(strings.Trim(m, "`")) + "</code>")
	})
	text = mdLink.ReplaceAllStringFunc(text, func(m string) string {
		parts := mdLink.FindStringSubmatch(m)
		label := parts[1]
		if strings.TrimSpace(label) == "" {
			label = parts[2]
		}
		return f.hold(`<a href="` + escapeAttr(parts[2]) + `">` + Escape(label) + `</a>`)
	})
	text = mdAutoLink.ReplaceAllStringFunc(text, func(m string) string {
		url := m[1 : len(m)-1]
		return f.hold(`<a href="` + escapeAttr(url) + `">` + Escape(url) + `</a>`)
	})

	text = Escape(text)
	text = mdBoldStar.ReplaceAllString(text, "<b>$1</b>")
	text = mdBoldUnder.ReplaceAllString(text, "<b>$1</b>")
	text = mdStrike.ReplaceAllString(text, "<s>$1</s>")
	text = mdSpoiler.ReplaceAllString(text, `<span class="tg-spoiler">$1</span>`)
	text = mdItalicStar.ReplaceAllString(text, "<i>$1</i>")
	text = mdItalicUnd.ReplaceAllString(text, "$1<i>$2</i>$3")

	return f.restore(text)
}

// escapeAttr экранирует значение HTML-атрибута.
func escapeAttr(text string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	).Replace(text)
}
