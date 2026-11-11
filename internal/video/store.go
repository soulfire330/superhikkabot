// Package video — локальные артефакты скачанного видео: аудио, кадры, транскрипт.
package video

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultFrames     = 5
	defaultFrameWidth = 512
	audioName         = "audio.mp3"
	janitorTick       = 30 * time.Minute
)

// Config — настройки кэша артефактов.
type Config struct {
	Dir         string
	TTL         time.Duration
	MaxBytes    int64
	Frames      int
	FrameWidth  int
	MaxDuration int
	FFmpeg      string
	FFprobe     string
	Logger      *log.Logger
}

// Store хранит артефакты видео на диске и привязку к сообщениям в памяти.
type Store struct {
	cfg       Config
	mu        sync.Mutex
	items     map[int64]map[int]*item
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

type item struct {
	key        string
	url        string
	duration   int
	hasAudio   bool
	frames     []string
	transcript string
	expires    time.Time
}

// Snapshot — то, что нужно сервису для ответа по видео.
type Snapshot struct {
	Key        string
	AudioPath  string
	HasAudio   bool
	Frames     []string
	Transcript string
	Duration   int
}

// NewStore собирает хранилище и применяет дефолты.
func NewStore(cfg Config) *Store {
	if cfg.Dir == "" {
		cfg.Dir = "./data/video"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 1 << 30
	}
	if cfg.Frames <= 0 {
		cfg.Frames = defaultFrames
	}
	if cfg.FrameWidth <= 0 {
		cfg.FrameWidth = defaultFrameWidth
	}

	return &Store{
		cfg:   cfg,
		items: make(map[int64]map[int]*item),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start запускает уборку просроченных артефактов.
func (s *Store) Start() {
	s.startOnce.Do(func() {
		go func() {
			defer close(s.done)
			ticker := time.NewTicker(janitorTick)
			defer ticker.Stop()

			for {
				select {
				case <-s.stop:
					return
				case <-ticker.C:
					s.cleanup()
				}
			}
		}()
	})
}

// Close останавливает уборку.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// SaveVideo раскладывает видео на аудио и кадры и привязывает их к сообщению.
func (s *Store) SaveVideo(ctx context.Context, sourceURL, videoPath string, chatID int64, messageID, duration int) error {
	key := cacheKey(sourceURL, chatID, messageID)
	dir := filepath.Join(s.cfg.Dir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("artifacts directory: %w", err)
	}

	probeDuration := duration
	if probeDuration <= 0 {
		probeDuration = s.probeDuration(ctx, videoPath)
	}

	entry := &item{
		key:      key,
		url:      sourceURL,
		duration: probeDuration,
		expires:  time.Now().Add(s.cfg.TTL),
	}

	// Аудио ни к чему, если видео длиннее лимита STT: остаются только кадры.
	if s.cfg.MaxDuration <= 0 || probeDuration <= s.cfg.MaxDuration {
		if ok, _ := s.extractAudio(ctx, videoPath, dir); ok {
			entry.hasAudio = true
		}
	}

	frames, err := s.extractFrames(ctx, videoPath, dir, probeDuration)
	if err != nil {
		return err
	}
	entry.frames = frames

	s.mu.Lock()
	chatItems, ok := s.items[chatID]
	if !ok {
		chatItems = make(map[int]*item)
		s.items[chatID] = chatItems
	}
	chatItems[messageID] = entry
	s.mu.Unlock()

	s.enforceLimit()

	return nil
}

// Get отдаёт артефакты сообщения; продлевает TTL.
func (s *Store) Get(chatID int64, messageID int) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatItems, ok := s.items[chatID]
	if !ok {
		return Snapshot{}, false
	}
	entry, ok := chatItems[messageID]
	if !ok {
		return Snapshot{}, false
	}

	entry.expires = time.Now().Add(s.cfg.TTL)

	return Snapshot{
		Key:        entry.key,
		AudioPath:  filepath.Join(s.cfg.Dir, entry.key, audioName),
		HasAudio:   entry.hasAudio,
		Frames:     entry.frames,
		Transcript: entry.transcript,
		Duration:   entry.duration,
	}, true
}

// SaveTranscript запоминает распознанную речь, чтобы не тратить STT повторно.
func (s *Store) SaveTranscript(chatID int64, messageID int, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if chatItems, ok := s.items[chatID]; ok {
		if entry, ok := chatItems[messageID]; ok {
			entry.transcript = text
		}
	}
}

func (s *Store) extractAudio(ctx context.Context, videoPath, dir string) (bool, string) {
	audioPath := filepath.Join(dir, audioName)

	err := runCommand(ctx, s.bin(s.cfg.FFmpeg, "ffmpeg"),
		"-y", "-loglevel", "error",
		"-i", videoPath,
		"-vn", "-ac", "1", "-ar", "16000",
		"-c:a", "libmp3lame", "-b:a", "64k",
		audioPath,
	)
	if err != nil {
		_ = os.Remove(audioPath)
		return false, ""
	}

	return true, audioPath
}

func (s *Store) extractFrames(ctx context.Context, videoPath, dir string, duration int) ([]string, error) {
	frames := make([]string, 0, s.cfg.Frames)

	for i, ts := range frameTimes(duration, s.cfg.Frames) {
		path := filepath.Join(dir, fmt.Sprintf("frame_%d.jpg", i))
		err := runCommand(ctx, s.bin(s.cfg.FFmpeg, "ffmpeg"),
			"-y", "-loglevel", "error",
			"-ss", strconv.FormatFloat(ts, 'f', 3, 64),
			"-i", videoPath,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2", s.cfg.FrameWidth),
			"-q:v", "4",
			path,
		)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		frames = append(frames, path)
	}

	return frames, nil
}

// probeDuration спрашивает ffprobe; без него длительность остаётся нулевой.
func (s *Store) probeDuration(ctx context.Context, path string) int {
	output, err := runCommandOutput(ctx, s.bin(s.cfg.FFprobe, "ffprobe"),
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		path,
	)
	if err != nil {
		return 0
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0
	}
	return int(seconds)
}

// cleanup удаляет просроченные каталоги и записи о них.
func (s *Store) cleanup() {
	now := time.Now()

	s.mu.Lock()
	for chatID, chatItems := range s.items {
		for messageID, entry := range chatItems {
			if now.After(entry.expires) {
				_ = os.RemoveAll(filepath.Join(s.cfg.Dir, entry.key))
				delete(chatItems, messageID)
			}
		}
		if len(chatItems) == 0 {
			delete(s.items, chatID)
		}
	}
	s.mu.Unlock()
}

// enforceLimit держит каталог в пределах max_bytes, снося самые старые записи.
func (s *Store) enforceLimit() {
	total := dirSize(s.cfg.Dir)
	if total <= s.cfg.MaxBytes {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for total > s.cfg.MaxBytes {
		var oldestChat int64
		var oldestMessage int
		var oldest *item

		for chatID, chatItems := range s.items {
			for messageID, entry := range chatItems {
				if oldest == nil || entry.expires.Before(oldest.expires) {
					oldestChat, oldestMessage, oldest = chatID, messageID, entry
				}
			}
		}
		if oldest == nil {
			return
		}

		size := dirSize(filepath.Join(s.cfg.Dir, oldest.key))
		_ = os.RemoveAll(filepath.Join(s.cfg.Dir, oldest.key))
		delete(s.items[oldestChat], oldestMessage)
		total -= size
	}
}

func (s *Store) bin(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// cacheKey — стабильное имя каталога для конкретного сообщения.
func cacheKey(sourceURL string, chatID int64, messageID int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", sourceURL, chatID, messageID)))
	return hex.EncodeToString(sum[:8])
}

// frameTimes раздаёт кадры равномерно по длительности.
func frameTimes(duration, count int) []float64 {
	if count <= 0 {
		return nil
	}
	if duration <= 0 {
		return []float64{0}
	}

	times := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		times = append(times, float64(duration)*(float64(i)+0.5)/float64(count))
	}
	return times
}

func dirSize(dir string) int64 {
	var total int64

	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})

	return total
}

func runCommand(ctx context.Context, bin string, args ...string) error {
	_, err := runCommandOutput(ctx, bin, args...)
	return err
}

func runCommandOutput(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second

	var stderr strings.Builder
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		return nil, fmt.Errorf("%s: %w: %s", bin, err, tail)
	}

	return output, nil
}
