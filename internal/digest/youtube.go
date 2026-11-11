package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	innerTubeURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"
	// Клиент ANDROID не отдаёт согласие-заглушку вместо captions.
	youtubeUserAgent = "com.google.android.youtube/20.10.38 (Linux; U; Android 11) gzip"
)

var youtubeID = regexp.MustCompile(`\A[a-zA-Z0-9_-]{11}\z`)

type captionTrack struct {
	BaseURL      string `json:"baseUrl"`
	LanguageCode string `json:"languageCode"`
	Kind         string `json:"kind"`
}

type playerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		Title         string `json:"title"`
		Author        string `json:"author"`
		LengthSeconds string `json:"lengthSeconds"`
	} `json:"videoDetails"`
	Captions struct {
		Renderer struct {
			CaptionTracks []captionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

// youtube достаёт транскрипт видео через InnerTube.
func (c *Client) youtube(ctx context.Context, source string) (string, error) {
	videoID, err := extractVideoID(source)
	if err != nil {
		return "", err
	}

	player, err := c.fetchPlayer(ctx, videoID)
	if err != nil {
		return "", err
	}

	tracks := player.Captions.Renderer.CaptionTracks
	if len(tracks) == 0 {
		return "", fmt.Errorf("the video has no subtitles, nothing to summarize")
	}

	track := selectTrack(tracks, c.language)
	if track == nil {
		return "", fmt.Errorf("no suitable subtitle track found")
	}

	transcript, err := c.fetchTranscript(ctx, track.BaseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(transcript) == "" {
		return "", fmt.Errorf("transcript is empty")
	}

	return fmt.Sprintf(
		"Видео: %s\nАвтор: %s\nДлительность: %s с\n\nТранскрипт:\n%s",
		player.VideoDetails.Title,
		player.VideoDetails.Author,
		player.VideoDetails.LengthSeconds,
		transcript,
	), nil
}

func (c *Client) fetchPlayer(ctx context.Context, videoID string) (*playerResponse, error) {
	payload, _ := json.Marshal(map[string]any{
		"videoId": videoID,
		"context": map[string]any{
			"client": map[string]any{
				"clientName":        "ANDROID",
				"clientVersion":     "20.10.38",
				"androidSdkVersion": 30,
				"osName":            "Android",
				"osVersion":         "11",
			},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, innerTubeURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("InnerTube request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", youtubeUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("InnerTube: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("InnerTube returned HTTP %d", resp.StatusCode)
	}

	var player playerResponse
	if err := json.NewDecoder(resp.Body).Decode(&player); err != nil {
		return nil, fmt.Errorf("parsing InnerTube response: %w", err)
	}

	if player.PlayabilityStatus.Status != "" && player.PlayabilityStatus.Status != "OK" {
		return nil, fmt.Errorf("video unavailable: %s %s", player.PlayabilityStatus.Status, player.PlayabilityStatus.Reason)
	}

	return &player, nil
}

func (c *Client) fetchTranscript(ctx context.Context, baseURL string) (string, error) {
	url := json3URL(baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("subtitle request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("subtitles: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("subtitles returned HTTP %d", resp.StatusCode)
	}

	return parseTranscript(resp.Body)
}

// parseTranscript разбирает формат timedtext json3 и склеивает строки.
func parseTranscript(body io.Reader) (string, error) {
	var payload struct {
		Events []struct {
			Segs []struct {
				Utf8 string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return "", fmt.Errorf("parsing subtitles: %w", err)
	}

	var builder strings.Builder
	for _, event := range payload.Events {
		for _, seg := range event.Segs {
			builder.WriteString(seg.Utf8)
		}
	}

	lines := make([]string, 0, 64)
	for _, line := range strings.Split(builder.String(), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	return strings.Join(lines, "\n"), nil
}

func json3URL(baseURL string) string {
	if strings.Contains(baseURL, "fmt=srv3") {
		return strings.Replace(baseURL, "fmt=srv3", "fmt=json3", 1)
	}
	if strings.Contains(baseURL, "fmt=json3") {
		return baseURL
	}
	if strings.Contains(baseURL, "?") {
		return baseURL + "&fmt=json3"
	}
	return baseURL + "?fmt=json3"
}

// selectTrack предпочитает ручные субтитры на нужном языке, затем ASR, затем английские.
func selectTrack(tracks []captionTrack, language string) *captionTrack {
	language = strings.ToLower(strings.TrimSpace(language))

	var targetManual, targetASR, englishManual, englishASR *captionTrack
	for i := range tracks {
		code := strings.ToLower(tracks[i].LanguageCode)
		manual := tracks[i].Kind != "asr"

		if language != "" && strings.HasPrefix(code, language) {
			if manual && targetManual == nil {
				targetManual = &tracks[i]
			} else if !manual && targetASR == nil {
				targetASR = &tracks[i]
			}
			continue
		}
		if strings.HasPrefix(code, "en") {
			if manual && englishManual == nil {
				englishManual = &tracks[i]
			} else if !manual && englishASR == nil {
				englishASR = &tracks[i]
			}
		}
	}

	switch {
	case targetManual != nil:
		return targetManual
	case targetASR != nil:
		return targetASR
	case englishManual != nil:
		return englishManual
	case englishASR != nil:
		return englishASR
	default:
		return &tracks[0]
	}
}

// extractVideoID понимает youtu.be, /watch?v=, /shorts/, /embed/ и голый id.
func extractVideoID(source string) (string, error) {
	source = strings.TrimSpace(source)
	if youtubeID.MatchString(source) {
		return source, nil
	}

	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		source = "https://" + source
	}

	u, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("invalid YouTube link: %w", err)
	}

	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")

	if host == "youtu.be" {
		id := strings.Trim(u.Path, "/")
		if youtubeID.MatchString(id) {
			return id, nil
		}
	}

	if id := u.Query().Get("v"); youtubeID.MatchString(id) {
		return id, nil
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		switch parts[0] {
		case "embed", "shorts", "live", "v", "vi":
			if youtubeID.MatchString(parts[1]) {
				return parts[1], nil
			}
		}
	}

	return "", fmt.Errorf("no video id found in the link")
}
