package media

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	tiktokVideoPath = regexp.MustCompile(`^/@[^/]+/video/\d+`)
	tiktokPhotoPath = regexp.MustCompile(`^/@[^/]+/photo/\d+`)
	tiktokShortPath = regexp.MustCompile(`^/t/[A-Za-z0-9]+`)
	instagramPath   = regexp.MustCompile(`^/(reel|reels|tv|p)/[^/]+`)
	youtubeShortID  = regexp.MustCompile(`^/shorts/[A-Za-z0-9_-]{11}/?$`)
	youtuBeID       = regexp.MustCompile(`^/[A-Za-z0-9_-]{11}/?$`)
)

// Parse разбирает ссылку и определяет площадку и тип медиа.
// Второй результат false означает, что ссылка не поддерживается.
func Parse(raw string) (Target, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Target{}, false
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Target{}, false
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return Target{}, false
	}

	// Для скачивания используем https, даже если пользователь прислал http.
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	normalized := Normalize(u.String())

	switch {
	case isTikTokHost(host):
		return parseTikTok(u, normalized)
	case isInstagramHost(host):
		return parseInstagram(u, normalized)
	case isYouTubeHost(host):
		return parseYouTube(u, normalized)
	}

	return Target{}, false
}

// Supported сообщает, поддерживается ли ссылка.
func Supported(raw string) bool {
	_, ok := Parse(raw)
	return ok
}

// Normalize приводит URL к виду, удобному для ключа кэша.
func Normalize(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")

	if u.Path == "" {
		u.Path = "/"
	}

	return u.String()
}

func parseTikTok(u *url.URL, normalized string) (Target, bool) {
	path := u.EscapedPath()

	switch strings.ToLower(u.Hostname()) {
	case "vm.tiktok.com", "vt.tiktok.com", "www.tiktokv.com", "m.tiktokv.com":
		if path == "" || path == "/" {
			return Target{}, false
		}
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformTikTok, Kind: KindAuto, Short: true}, true
	}

	if tiktokShortPath.MatchString(path) {
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformTikTok, Kind: KindAuto, Short: true}, true
	}
	if tiktokPhotoPath.MatchString(path) {
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformTikTok, Kind: KindPhoto}, true
	}
	if tiktokVideoPath.MatchString(path) {
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformTikTok, Kind: KindVideo}, true
	}

	return Target{}, false
}

func parseInstagram(u *url.URL, normalized string) (Target, bool) {
	match := instagramPath.FindStringSubmatch(u.EscapedPath())
	if match == nil {
		return Target{}, false
	}

	kind := KindVideo
	if match[1] == "p" {
		// /p/ бывает и одиночным фото, и каруселью, и видео.
		kind = KindAuto
	}

	return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformInstagram, Kind: kind}, true
}

func parseYouTube(u *url.URL, normalized string) (Target, bool) {
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()

	if host == "youtu.be" && youtuBeID.MatchString(path) {
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformYouTube, Kind: KindVideo}, true
	}
	if youtubeShortID.MatchString(path) {
		return Target{URL: u.String(), NormalizedURL: normalized, Platform: PlatformYouTube, Kind: KindVideo}, true
	}

	return Target{}, false
}

func isTikTokHost(host string) bool {
	return host == "tiktok.com" || host == "www.tiktok.com" ||
		host == "m.tiktok.com" || host == "vm.tiktok.com" ||
		host == "vt.tiktok.com" || host == "www.tiktokv.com" ||
		host == "m.tiktokv.com"
}

func isInstagramHost(host string) bool {
	return host == "instagram.com" || host == "www.instagram.com" || host == "m.instagram.com"
}

func isYouTubeHost(host string) bool {
	return host == "youtube.com" || host == "www.youtube.com" ||
		host == "m.youtube.com" || host == "youtu.be"
}
