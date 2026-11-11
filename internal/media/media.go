// Package media — скачивание видео и фотопостов через yt-dlp и gallery-dl.
package media

import (
	"context"
	"errors"
)

// Platform — площадка, с которой качаем.
type Platform string

const (
	PlatformTikTok    Platform = "tiktok"
	PlatformInstagram Platform = "instagram"
	PlatformYouTube   Platform = "youtube"
)

// Kind — что именно нашлось по ссылке.
type Kind int

const (
	KindAuto Kind = iota
	KindVideo
	KindPhoto
)

func (k Kind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindPhoto:
		return "photo"
	default:
		return "auto"
	}
}

// Target — разобранная ссылка.
type Target struct {
	URL           string
	NormalizedURL string
	Platform      Platform
	Kind          Kind
	Short         bool
}

// Downloaded — результат скачивания.
type Downloaded struct {
	Kind       Kind
	VideoPath  string
	ImagePaths []string
	Width      int
	Height     int
	Duration   int
}

var (
	// ErrUnsupported — ссылка не относится к поддерживаемым площадкам.
	ErrUnsupported = errors.New("unsupported link")
	// ErrTooLarge — файл больше лимита.
	ErrTooLarge = errors.New("file is too large")
	// ErrTooLong — видео длиннее лимита.
	ErrTooLong = errors.New("video is too long")
	// ErrNoMedia — инструмент ничего не скачал.
	ErrNoMedia = errors.New("no media found")
)

// VideoSaver сохраняет артефакты скачанного видео для будущих вопросов.
type VideoSaver interface {
	SaveVideo(ctx context.Context, sourceURL, videoPath string, chatID int64, messageID, duration int) error
}

// PhotoSaver сохраняет локальные копии отправленных фото.
type PhotoSaver interface {
	BindGroup(ctx context.Context, sourceURL string, paths []string, chatID int64, messageIDs []int) error
}
