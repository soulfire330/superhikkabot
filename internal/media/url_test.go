package media

import "testing"

func TestParseSupportedLinks(t *testing.T) {
	cases := []struct {
		url      string
		platform Platform
		kind     Kind
	}{
		{"https://www.tiktok.com/@user/video/123", PlatformTikTok, KindVideo},
		{"https://www.tiktok.com/@user/photo/123", PlatformTikTok, KindPhoto},
		{"https://vm.tiktok.com/ZMabc/", PlatformTikTok, KindAuto},
		{"https://www.instagram.com/reel/abc/", PlatformInstagram, KindVideo},
		{"https://www.instagram.com/p/abc/", PlatformInstagram, KindAuto},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", PlatformYouTube, KindVideo},
		{"https://youtu.be/dQw4w9WgXcQ", PlatformYouTube, KindVideo},
	}

	for _, item := range cases {
		target, ok := Parse(item.url)
		if !ok {
			t.Errorf("%s: не разобралась", item.url)
			continue
		}
		if target.Platform != item.platform || target.Kind != item.kind {
			t.Errorf("%s: получили %s/%v, ждали %s/%v", item.url, target.Platform, target.Kind, item.platform, item.kind)
		}
		if item.platform == PlatformYouTube && target.URL == "" {
			t.Errorf("%s: пустой URL для скачивания", item.url)
		}
	}
}

func TestParseRejectsOtherLinks(t *testing.T) {
	for _, raw := range []string{
		"",
		"просто текст",
		"https://example.com/video/1",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ", // полные видео не качаем
		"ftp://tiktok.com/@user/video/123",
	} {
		if _, ok := Parse(raw); ok {
			t.Errorf("%q должна была не поддерживаться", raw)
		}
	}
}

func TestNormalizeDropsQuery(t *testing.T) {
	got := Normalize("https://WWW.TikTok.com/@User/video/1/?is_from_webapp=1#top")
	want := "https://www.tiktok.com/@User/video/1"
	if got != want {
		t.Errorf("Normalize = %q, хотим %q", got, want)
	}
}
