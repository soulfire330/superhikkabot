package digest

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ": "youtube",
		"https://youtu.be/dQw4w9WgXcQ":                "youtube",
		"https://github.com/wxtering/summario":        "github",
		"https://example.com/post":                    "webpage",
	}

	for source, want := range cases {
		got, err := resolve(source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got != want {
			t.Errorf("%s: тип %q, хотим %q", source, got, want)
		}
	}

	if _, err := resolve("не ссылка"); err == nil {
		t.Error("для строки без хоста ждали ошибку")
	}
}

func TestExtractVideoID(t *testing.T) {
	cases := map[string]string{
		"dQw4w9WgXcQ":                                   "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                  "dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":   "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":    "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&t=3": "dQw4w9WgXcQ",
	}

	for source, want := range cases {
		got, err := extractVideoID(source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got != want {
			t.Errorf("%s: id %q, хотим %q", source, got, want)
		}
	}

	if _, err := extractVideoID("https://www.youtube.com/watch?v=short"); err == nil {
		t.Error("для короткого id ждали ошибку")
	}
}

func TestSelectTrackPrefersManual(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "ru", Kind: "asr", BaseURL: "asr-ru"},
		{LanguageCode: "ru", BaseURL: "manual-ru"},
		{LanguageCode: "en", BaseURL: "manual-en"},
	}

	if got := selectTrack(tracks, "ru"); got == nil || got.BaseURL != "manual-ru" {
		t.Fatalf("выбрали %+v, ждали ручную ru-дорожку", got)
	}

	// Нет нужного языка — берём английскую.
	if got := selectTrack(tracks, "de"); got == nil || got.BaseURL != "manual-en" {
		t.Fatalf("выбрали %+v, ждали английскую дорожку", got)
	}

	// Есть только ASR.
	only := []captionTrack{{LanguageCode: "ru", Kind: "asr", BaseURL: "asr-ru"}}
	if got := selectTrack(only, "ru"); got == nil || got.BaseURL != "asr-ru" {
		t.Fatalf("выбрали %+v, ждали ASR-дорожку", got)
	}
}

func TestParseTranscript(t *testing.T) {
	payload := `{"events":[{"segs":[{"utf8":"привет"}]},{"segs":[{"utf8":"\n"}]},{"segs":[{"utf8":"мир"}]}]}`

	got, err := parseTranscript(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if want := "привет\nмир"; got != want {
		t.Errorf("получили %q, хотим %q", got, want)
	}
}

func TestJSON3URL(t *testing.T) {
	cases := map[string]string{
		"https://x/api/timedtext?fmt=srv3":  "https://x/api/timedtext?fmt=json3",
		"https://x/api/timedtext?fmt=json3": "https://x/api/timedtext?fmt=json3",
		"https://x/api/timedtext":           "https://x/api/timedtext?fmt=json3",
	}

	for source, want := range cases {
		if got := json3URL(source); got != want {
			t.Errorf("%s: %q, хотим %q", source, got, want)
		}
	}
}

func TestRepoInfo(t *testing.T) {
	owner, repo, err := repoInfo("https://github.com/wxtering/summario")
	if err != nil {
		t.Fatalf("repoInfo: %v", err)
	}
	if owner != "wxtering" || repo != "summario" {
		t.Errorf("получили %s/%s", owner, repo)
	}

	if _, _, err := repoInfo("https://github.com/wxtering"); err == nil {
		t.Error("для ссылки без репозитория ждали ошибку")
	}
}

func TestStripFences(t *testing.T) {
	raw := "```json\n{\"title\":\"a\",\"summary\":\"b\"}\n```"
	if got := stripFences(raw); got != `{"title":"a","summary":"b"}` {
		t.Errorf("получили %q", got)
	}
}
