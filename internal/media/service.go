package media

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/soulfire330/superhikkabot/internal/tg"
)

const (
	queueCapacity   = 50
	placeholderText = "⏳ Downloading…"
)

// Config — настройки фоновой очереди.
type Config struct {
	TempDir string
	Timeout time.Duration
}

// Request — задание на скачивание.
type Request struct {
	ChatID      int64
	ReplyTo     int
	Placeholder int
	URL         string
}

// Service качает медиа в единственном воркере, чтобы не блокировать остальных.
type Service struct {
	tg         *tg.Client
	downloader *CLIDownloader
	cfg        Config
	logger     *log.Logger

	video VideoSaver
	photo PhotoSaver

	jobs      chan Request
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
}

// NewService собирает сервис.
func NewService(client *tg.Client, downloader *CLIDownloader, cfg Config, logger *log.Logger) *Service {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}

	return &Service{
		tg:         client,
		downloader: downloader,
		cfg:        cfg,
		logger:     logger,
		jobs:       make(chan Request, queueCapacity),
		stop:       make(chan struct{}),
	}
}

// SetVideoSaver подключает хранилище артефактов видео.
func (s *Service) SetVideoSaver(saver VideoSaver) { s.video = saver }

// SetPhotoSaver подключает кэш отправленных фото.
func (s *Service) SetPhotoSaver(saver PhotoSaver) { s.photo = saver }

// Start поднимает воркер.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.worker(ctx)
	})
}

// Stop дожидается текущего задания и выходит.
func (s *Service) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case job := <-s.jobs:
			s.run(ctx, job)
		}
	}
}

// Submit ставит ссылку в очередь: сначала плейсхолдер, потом задание.
func (s *Service) Submit(ctx context.Context, chatID int64, replyTo int, url string) error {
	ids, err := s.tg.Send(ctx, chatID, replyTo, placeholderText)
	if err != nil {
		return err
	}

	placeholder := 0
	if len(ids) > 0 {
		placeholder = ids[0]
	}

	select {
	case s.jobs <- Request{ChatID: chatID, ReplyTo: replyTo, Placeholder: placeholder, URL: url}:
		return nil
	default:
		if placeholder != 0 {
			_ = s.tg.Edit(ctx, chatID, placeholder, "Too many downloads, try again later.")
		}
		return errors.New("download queue is full")
	}
}

func (s *Service) run(parent context.Context, req Request) {
	ctx, cancel := context.WithTimeout(parent, s.cfg.Timeout)
	defer cancel()

	tmp, err := os.MkdirTemp(s.cfg.TempDir, "media-")
	if err != nil {
		s.fail(ctx, req, fmt.Errorf("temp directory: %w", err))
		return
	}
	defer os.RemoveAll(tmp)

	target, ok := Parse(req.URL)
	if !ok {
		s.fail(ctx, req, ErrUnsupported)
		return
	}

	started := time.Now()
	got, err := s.downloader.Download(ctx, target, tmp)
	if err != nil {
		s.fail(ctx, req, err)
		return
	}
	s.logger.Printf("media: %s → %s за %s", target.Platform, got.Kind, time.Since(started).Round(time.Second))

	switch got.Kind {
	case KindVideo:
		s.sendVideo(ctx, req, got)
	case KindPhoto:
		s.sendPhotos(ctx, req, got)
	default:
		s.fail(ctx, req, fmt.Errorf("unknown media kind"))
	}
}

func (s *Service) sendVideo(ctx context.Context, req Request, got Downloaded) {
	id, err := s.tg.SendVideo(ctx, req.ChatID, req.ReplyTo, got.VideoPath, got.Width, got.Height, got.Duration)
	if err != nil {
		s.fail(ctx, req, fmt.Errorf("video was not sent: %w", err))
		return
	}

	s.removePlaceholder(ctx, req)

	if s.video == nil {
		return
	}
	if err := s.video.SaveVideo(ctx, req.URL, got.VideoPath, req.ChatID, id, got.Duration); err != nil {
		s.logger.Printf("media: артефакты видео не сохранены: %v", err)
	}
}

func (s *Service) sendPhotos(ctx context.Context, req Request, got Downloaded) {
	ids, err := s.tg.SendPhotos(ctx, req.ChatID, req.ReplyTo, got.ImagePaths)
	if err != nil {
		s.fail(ctx, req, fmt.Errorf("photos were not sent: %w", err))
		return
	}

	s.removePlaceholder(ctx, req)

	if s.photo == nil || len(ids) == 0 {
		return
	}
	if err := s.photo.BindGroup(ctx, req.URL, got.ImagePaths, req.ChatID, ids); err != nil {
		s.logger.Printf("media: копии фото не сохранены: %v", err)
	}
}

func (s *Service) removePlaceholder(ctx context.Context, req Request) {
	if req.Placeholder == 0 {
		return
	}
	if err := s.tg.Delete(ctx, req.ChatID, req.Placeholder); err != nil {
		s.logger.Printf("media: плейсхолдер не удалён: %v", err)
	}
}

// fail показывает причину в плейсхолдере человеческим языком.
func (s *Service) fail(ctx context.Context, req Request, err error) {
	s.logger.Printf("media: %s: %v", req.URL, err)
	if req.Placeholder == 0 {
		return
	}

	_ = s.tg.Edit(ctx, req.ChatID, req.Placeholder, failText(err))
}

func failText(err error) string {
	switch {
	case errors.Is(err, ErrUnsupported):
		return "I cannot handle this link: only TikTok, Instagram and YouTube Shorts are supported."
	case errors.Is(err, ErrTooLarge):
		return "⚠️ The file is larger than 50 MB, Telegram will not accept it."
	case errors.Is(err, ErrTooLong):
		return "⚠️ The video is too long."
	case errors.Is(err, ErrNoMedia):
		return "⚠️ The link opened, but there is no media there."
	case errors.Is(err, context.DeadlineExceeded):
		return "⚠️ The download timed out."
	default:
		return "⚠️ Download failed: " + err.Error()
	}
}
