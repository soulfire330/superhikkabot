package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// Bot API принимает фото не больше 10 МБ.
	maxPhotoSize   = 10 << 20
	maxCommandTail = 1200
)

var (
	videoExtensions = map[string]bool{".mp4": true, ".webm": true, ".mkv": true, ".mov": true, ".m4v": true}
	imageExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}
)

// CLIConfig — настройки CLI-загрузчика.
type CLIConfig struct {
	YTDLP       string
	GalleryDL   string
	Proxy       string
	CookiesFile string
	MaxFileSize int64
	MaxDuration int
}

// CLIDownloader скачивает медиа через yt-dlp (видео) и gallery-dl (фото).
type CLIDownloader struct {
	cfg        CLIConfig
	httpClient *http.Client
}

// NewCLIDownloader собирает загрузчик. Пустые пути заменяются на yt-dlp и gallery-dl из PATH.
func NewCLIDownloader(cfg CLIConfig) *CLIDownloader {
	if cfg.YTDLP == "" {
		cfg.YTDLP = "yt-dlp"
	}
	if cfg.GalleryDL == "" {
		cfg.GalleryDL = "gallery-dl"
	}
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = 50 << 20
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = 600
	}

	return &CLIDownloader{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// Download скачивает медиа по ссылке во временный каталог dir.
func (d *CLIDownloader) Download(ctx context.Context, target Target, dir string) (Downloaded, error) {
	target = d.resolve(ctx, target)

	switch target.Platform {
	case PlatformYouTube:
		return d.downloadVideo(ctx, target, filepath.Join(dir, "video"))

	case PlatformTikTok:
		switch target.Kind {
		case KindPhoto:
			return d.downloadPhotos(ctx, target, filepath.Join(dir, "photo"))
		case KindVideo:
			return d.downloadVideo(ctx, target, filepath.Join(dir, "video"))
		default:
			// Короткая ссылка: чаще видео, но бывает и фотопост.
			got, err := d.downloadVideo(ctx, target, filepath.Join(dir, "video"))
			if err == nil {
				return got, nil
			}
			if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooLong) {
				return Downloaded{}, err
			}
			return d.downloadPhotos(ctx, target, filepath.Join(dir, "photo"))
		}

	case PlatformInstagram:
		switch target.Kind {
		case KindPhoto:
			return d.downloadPhotos(ctx, target, filepath.Join(dir, "photo"))
		case KindVideo:
			return d.downloadVideo(ctx, target, filepath.Join(dir, "video"))
		default:
			// Instagram /p/ — сначала галерея: карусель из фото встречается чаще.
			got, err := d.downloadPhotos(ctx, target, filepath.Join(dir, "photo"))
			if err == nil {
				return got, nil
			}
			if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooLong) {
				return Downloaded{}, err
			}
			return d.downloadVideo(ctx, target, filepath.Join(dir, "video"))
		}
	}

	return Downloaded{}, ErrUnsupported
}

func (d *CLIDownloader) resolve(ctx context.Context, target Target) Target {
	if !target.Short {
		return target
	}

	finalURL, err := d.resolveRedirect(ctx, target.URL)
	if err != nil {
		return target
	}

	resolved, ok := Parse(finalURL)
	if !ok {
		return target
	}
	return resolved
}

func (d *CLIDownloader) resolveRedirect(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	return resp.Request.URL.String(), nil
}

func (d *CLIDownloader) downloadVideo(ctx context.Context, target Target, dir string) (Downloaded, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Downloaded{}, fmt.Errorf("creating temp directory: %w", err)
	}

	if err := runCommand(ctx, d.cfg.YTDLP, d.ytdlpArgs(target, dir)); err != nil {
		return Downloaded{}, err
	}

	files := collectFiles(dir, videoExtensions)
	if len(files) == 0 {
		return Downloaded{}, fmt.Errorf("%w: yt-dlp produced no video file", ErrNoMedia)
	}

	path := pickLargest(files)
	if err := d.validate(path); err != nil {
		return Downloaded{}, err
	}

	width, height, duration, err := probeVideo(ctx, path)
	if err != nil {
		return Downloaded{}, err
	}
	if d.cfg.MaxDuration > 0 && duration > d.cfg.MaxDuration {
		return Downloaded{}, fmt.Errorf("%w: %d seconds", ErrTooLong, duration)
	}

	return Downloaded{Kind: KindVideo, VideoPath: path, Width: width, Height: height, Duration: duration}, nil
}

func (d *CLIDownloader) downloadPhotos(ctx context.Context, target Target, dir string) (Downloaded, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Downloaded{}, fmt.Errorf("creating temp directory: %w", err)
	}

	if err := runCommand(ctx, d.cfg.GalleryDL, d.galleryArgs(target, dir)); err != nil {
		return Downloaded{}, err
	}

	images := collectFiles(dir, imageExtensions)
	if len(images) > 0 {
		filtered := images[:0]
		for _, path := range images {
			info, err := os.Stat(path)
			if err != nil {
				return Downloaded{}, fmt.Errorf("checking photos: %w", err)
			}
			if info.Size() > maxPhotoSize {
				continue
			}
			filtered = append(filtered, path)
		}
		if len(filtered) == 0 {
			return Downloaded{}, fmt.Errorf("%w: all photos are larger than 10 MB", ErrTooLarge)
		}
		return Downloaded{Kind: KindPhoto, ImagePaths: filtered}, nil
	}

	// Instagram /p/ иногда оказывается видео, а не фотопостом.
	videos := collectFiles(dir, videoExtensions)
	if len(videos) == 0 {
		return Downloaded{}, fmt.Errorf("%w: gallery-dl produced neither photos nor video", ErrNoMedia)
	}

	path := pickLargest(videos)
	if err := d.validate(path); err != nil {
		return Downloaded{}, err
	}
	width, height, duration, err := probeVideo(ctx, path)
	if err != nil {
		return Downloaded{}, err
	}
	if d.cfg.MaxDuration > 0 && duration > d.cfg.MaxDuration {
		return Downloaded{}, fmt.Errorf("%w: %d seconds", ErrTooLong, duration)
	}

	return Downloaded{Kind: KindVideo, VideoPath: path, Width: width, Height: height, Duration: duration}, nil
}

func (d *CLIDownloader) ytdlpArgs(target Target, dir string) []string {
	format := "best[ext=mp4][acodec!=none]/best[ext=mp4]/best"
	if target.Platform == PlatformYouTube {
		format = "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best"
	}

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-mtime",
		"--no-write-thumbnail",
		"--no-write-subs",
		"--no-write-auto-subs",
		"--paths", dir,
		"-o", "%(id)s.%(ext)s",
		"--format", format,
		"--merge-output-format", "mp4",
		"--max-filesize", strconv.FormatInt(d.cfg.MaxFileSize, 10),
		"--socket-timeout", "30",
		"--retries", "2",
		"--fragment-retries", "2",
	}

	if d.cfg.Proxy != "" {
		args = append(args, "--proxy", d.cfg.Proxy)
	}
	if d.cfg.CookiesFile != "" {
		args = append(args, "--cookies", d.cfg.CookiesFile)
	}

	return append(args, target.URL)
}

func (d *CLIDownloader) galleryArgs(target Target, dir string) []string {
	args := []string{"--destination", dir, "--no-mtime", "--no-input"}

	if d.cfg.Proxy != "" {
		args = append(args, "--proxy", d.cfg.Proxy)
	}
	if d.cfg.CookiesFile != "" {
		args = append(args, "--cookies", d.cfg.CookiesFile)
	}

	return append(args, target.URL)
}

func (d *CLIDownloader) validate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("checking file: %w", err)
	}
	if info.Size() > d.cfg.MaxFileSize {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, info.Size())
	}
	return nil
}

func runCommand(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second

	var stderr strings.Builder
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > maxCommandTail {
			tail = tail[len(tail)-maxCommandTail:]
		}
		if tail == "" {
			return fmt.Errorf("%s: %w", bin, err)
		}
		return fmt.Errorf("%s: %w: %s", bin, err, tail)
	}

	return nil
}

func collectFiles(dir string, extensions map[string]bool) []string {
	var files []string

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if extensions[strings.ToLower(filepath.Ext(path))] {
			files = append(files, path)
		}
		return nil
	})

	return files
}

func pickLargest(files []string) string {
	largest := files[0]
	var largestSize int64

	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Size() > largestSize {
			largest = path
			largestSize = info.Size()
		}
	}

	return largest
}

type ffprobeResult struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// probeVideo читает размеры и длительность. Без ffprobe размеры просто остаются нулями.
func probeVideo(ctx context.Context, path string) (int, int, int, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height:format=duration",
		"-of", "json",
		path,
	)
	cmd.WaitDelay = 5 * time.Second

	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, nil
	}

	var result ffprobeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, 0, 0, nil
	}

	width, height := 0, 0
	if len(result.Streams) > 0 {
		width = result.Streams[0].Width
		height = result.Streams[0].Height
	}

	duration := 0
	if seconds, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil {
		duration = int(seconds)
	}

	return width, height, duration, nil
}
