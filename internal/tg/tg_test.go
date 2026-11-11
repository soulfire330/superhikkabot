package tg

import (
	"strings"
	"testing"
)

func TestSplitKeepsShortText(t *testing.T) {
	chunks := split("короткий текст")
	if len(chunks) != 1 || chunks[0] != "короткий текст" {
		t.Fatalf("получили %q", chunks)
	}
}

func TestSplitByParagraphs(t *testing.T) {
	paragraph := strings.Repeat("а", 2000)
	text := strings.Join([]string{paragraph, paragraph, paragraph}, "\n")

	chunks := split(text)
	if len(chunks) < 2 {
		t.Fatalf("длинный текст не разбился: %d кусков", len(chunks))
	}

	var total int
	for _, chunk := range chunks {
		if len([]rune(chunk)) > chunkLimit {
			t.Errorf("кусок длиннее лимита: %d", len([]rune(chunk)))
		}
		total += len([]rune(strings.ReplaceAll(chunk, "\n", "")))
	}

	if want := len([]rune(strings.ReplaceAll(text, "\n", ""))); total != want {
		t.Errorf("после разбиения %d символов, было %d", total, want)
	}
}

func TestSplitHardBreaksLongLine(t *testing.T) {
	text := strings.Repeat("б", chunkLimit*2+10)

	chunks := split(text)
	if len(chunks) != 3 {
		t.Fatalf("получили %d кусков, ждали 3", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > chunkLimit {
			t.Errorf("кусок длиннее лимита: %d", len([]rune(chunk)))
		}
	}
	if total := len([]rune(strings.Join(chunks, ""))); total != len([]rune(text)) {
		t.Errorf("склеенный текст %d, исходный %d", total, len([]rune(text)))
	}
}

func TestStripHTMLAndEscape(t *testing.T) {
	if got := stripHTML("<b>жирный</b> текст"); got != "жирный текст" {
		t.Errorf("stripHTML = %q", got)
	}
	if got := Escape("a < b & c"); got != "a &lt; b &amp; c" {
		t.Errorf("Escape = %q", got)
	}
}
