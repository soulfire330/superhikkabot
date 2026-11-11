// Command bot — Telegram-бот на поллинге с агентом pi за спиной.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/soulfire330/superhikkabot/internal/bot"
	"github.com/soulfire330/superhikkabot/internal/config"
	"github.com/soulfire330/superhikkabot/internal/dialog"
	"github.com/soulfire330/superhikkabot/internal/digest"
	"github.com/soulfire330/superhikkabot/internal/firecrawl"
	"github.com/soulfire330/superhikkabot/internal/harness"
	"github.com/soulfire330/superhikkabot/internal/llm"
	"github.com/soulfire330/superhikkabot/internal/media"
	"github.com/soulfire330/superhikkabot/internal/photo"
	"github.com/soulfire330/superhikkabot/internal/proxy"
	"github.com/soulfire330/superhikkabot/internal/tg"
	"github.com/soulfire330/superhikkabot/internal/video"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	// Прошлый самоперезапуск не дожил до рабочего состояния — уходим на старый бинарник.
	if err := harness.CheckBoot(); err != nil {
		logger.Printf("откат не удался: %v", err)
	}

	cfg, err := config.Load(configPath())
	if err != nil {
		logger.Fatalf("конфиг: %v", err)
	}

	for _, dir := range []string{cfg.Media.Dir, cfg.Video.Dir, cfg.Photo.Dir, cfg.Harness.SessionDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Fatalf("каталог %s: %v", dir, err)
		}
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Прокси для внешних API: пустое поле — прямое соединение.
	contentTransport, err := proxy.Transport(cfg.Proxy.LLM)
	if err != nil {
		logger.Fatalf("прокси llm: %v", err)
	}
	telegramTransport, err := proxy.Transport(cfg.Proxy.Telegram)
	if err != nil {
		logger.Fatalf("прокси telegram: %v", err)
	}
	contentHTTP := func(timeout time.Duration) *http.Client {
		return &http.Client{Transport: contentTransport, Timeout: timeout}
	}
	if cfg.Proxy.LLM != "" {
		logger.Printf("прокси llm: %s", cfg.Proxy.LLM)
	}
	if cfg.Proxy.Telegram != "" {
		logger.Printf("прокси telegram: %s", cfg.Proxy.Telegram)
	}

	var router *bot.Bot

	initCtx, cancelInit := context.WithTimeout(rootCtx, 30*time.Second)
	tgClient, err := tg.New(initCtx, tg.Options{
		Token:     cfg.Telegram.Token,
		Transport: telegramTransport,
		Logger:    logger,
		OnMessage: func(ctx context.Context, msg *models.Message) {
			router.Handle(ctx, msg)
		},
	})
	cancelInit()
	if err != nil {
		logger.Fatalf("telegram: %v", err)
	}
	logger.Printf("бот @%s (id %d)", tgClient.Username(), tgClient.ID())

	history := dialog.NewStore()
	history.Start()
	defer history.Close()

	tgClient.SetHooks(
		func(chatID int64, ids []int, text string, replyTo int) {
			first := 0
			for i, id := range ids {
				// Все чанки одного ответа — алиасы первого: реплай на любой поднимает весь ответ.
				if i == 0 {
					history.Record(chatID, id, replyTo, text, true)
					first = id
					continue
				}
				history.Record(chatID, id, first, "", true)
			}
		},
		func(chatID int64, messageID int, text string) {
			history.UpdateText(chatID, messageID, text)
		},
	)

	crawl := firecrawl.New(cfg.Firecrawl.BaseURL, cfg.Firecrawl.APIKey).
		WithHTTPClient(contentHTTP(45 * time.Second))
	digestClient := digest.New(
		llm.New(cfg.LLM.URL, cfg.LLM.APIKey, cfg.LLM.Model).
			WithHTTPClient(contentHTTP(2*time.Minute)).
			WithLogger(logger),
		crawl,
		cfg.Digest.Language,
	).WithHTTPClient(contentHTTP(30 * time.Second))

	llmClient := llm.New(cfg.LLM.URL, cfg.LLM.APIKey, cfg.LLM.Model, crawl.Tool(), digestClient.Tool()).
		WithHTTPClient(contentHTTP(2 * time.Minute)).
		WithLogger(logger).
		WithMaxRounds(cfg.LLM.MaxRounds).
		WithReasoning(cfg.LLM.Reasoning)

	var videoService *video.Service
	if cfg.Video.Enabled {
		videoStore := video.NewStore(video.Config{
			Dir:         cfg.Video.Dir,
			TTL:         config.Duration(cfg.Video.TTL, 24*time.Hour),
			MaxBytes:    cfg.Video.MaxBytes,
			Frames:      cfg.Video.Frames,
			FrameWidth:  cfg.Video.FrameWidth,
			MaxDuration: cfg.Video.MaxDuration,
			Logger:      logger,
		})
		videoStore.Start()
		defer videoStore.Close()

		videoService = video.NewService(
			videoStore,
			video.NewTranscriber(cfg.Video.GroqKey, cfg.Video.GroqURL, cfg.Video.GroqModel),
			logger,
		)
	}

	var photoService *photo.Service
	if cfg.Photo.Enabled {
		photoService = photo.NewService(tgClient, photo.Config{
			Dir:         cfg.Photo.Dir,
			TTL:         config.Duration(cfg.Photo.TTL, 24*time.Hour),
			MaxBytes:    cfg.Photo.MaxBytes,
			DirectLimit: cfg.Photo.DirectLimit,
			CellSize:    cfg.Photo.CollageCell,
			Quality:     cfg.Photo.Quality,
			CollageMax:  cfg.Photo.CollageMax,
			Logger:      logger,
		})
		photoService.Start()
		defer photoService.Close()
	}

	mediaService := media.NewService(
		tgClient,
		media.NewCLIDownloader(media.CLIConfig{
			YTDLP:       cfg.Media.YTDLP,
			GalleryDL:   cfg.Media.GalleryDL,
			Proxy:       cfg.Media.Proxy,
			CookiesFile: cfg.Media.CookiesFile,
			MaxFileSize: cfg.Media.MaxFileSize,
			MaxDuration: cfg.Media.MaxDuration,
		}),
		media.Config{
			TempDir: cfg.Media.Dir,
			Timeout: config.Duration(cfg.Media.Timeout, 10*time.Minute),
		},
		logger,
	)
	if videoService != nil {
		mediaService.SetVideoSaver(videoService)
	}
	if photoService != nil {
		mediaService.SetPhotoSaver(photoService)
	}
	mediaService.Start(rootCtx)
	defer mediaService.Stop()

	var runner *harness.Runner
	if cfg.Harness.Enabled {
		runner = harness.New(harness.Config{
			Binary:     cfg.Harness.Binary,
			Workspace:  cfg.Harness.Workspace,
			SessionDir: cfg.Harness.SessionDir,
			Timeout:    config.Duration(cfg.Harness.Timeout, 20*time.Minute),
			Deploy:     cfg.Harness.Deploy,
		}, logger)
		defer runner.Stop()
	}

	router = bot.New(bot.Deps{
		TG:      tgClient,
		Config:  cfg,
		LLM:     llmClient,
		Digest:  digestClient,
		Media:   mediaService,
		Video:   videoService,
		Photo:   photoService,
		Dialog:  history,
		Harness: runner,
		Logger:  logger,
	})

	// Маркер снимаем после GetMe: до этого места бот не умеет ничего, и падение
	// здесь — показание откатиться на прошлый бинарник.
	harness.MarkBooted()

	if len(cfg.Telegram.AllowedChats) == 0 && len(cfg.Telegram.AdminUsers) == 0 {
		logger.Println("белый список пуст: бот не ответит никому, впиши allowed_chats или admin_users")
	}
	logger.Printf("слушаю обновления (чатов в белом списке: %d, админов: %d)",
		len(cfg.Telegram.AllowedChats), len(cfg.Telegram.AdminUsers))
	tgClient.API().Start(rootCtx)

	logger.Println("остановлено")
}

func configPath() string {
	if path := os.Getenv("CONFIG_PATH"); path != "" {
		return path
	}
	return "config.toml"
}
