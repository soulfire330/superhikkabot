// Package config читает config.toml и добирает секреты из окружения.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config — весь конфиг бота. Отсутствующие в файле поля остаются дефолтными.
type Config struct {
	Telegram  Telegram  `toml:"telegram"`
	LLM       LLM       `toml:"llm"`
	Digest    Digest    `toml:"digest"`
	Firecrawl Firecrawl `toml:"firecrawl"`
	Media     Media     `toml:"media"`
	Video     Video     `toml:"video"`
	Photo     Photo     `toml:"photo"`
	Harness   Harness   `toml:"harness"`
	Proxy     Proxy     `toml:"proxy"`
}

// Proxy — адреса прокси. Пустое поле означает прямое соединение.
type Proxy struct {
	LLM      string `toml:"llm"`
	Telegram string `toml:"telegram"`
}

type Telegram struct {
	Token        string  `toml:"-"`
	AllowedChats []int64 `toml:"allowed_chats"`
	AdminUsers   []int64 `toml:"admin_users"`
}

type LLM struct {
	URL       string `toml:"url"`
	Model     string `toml:"model"`
	MaxRounds int    `toml:"max_tool_rounds"`
	Reasoning string `toml:"reasoning_effort"`
	APIKey    string `toml:"-"`
}

type Digest struct {
	Language string `toml:"language"`
}

type Firecrawl struct {
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"-"`
}

type Media struct {
	Dir         string `toml:"dir"`
	YTDLP       string `toml:"ytdlp"`
	GalleryDL   string `toml:"gallery_dl"`
	Proxy       string `toml:"proxy"`
	CookiesFile string `toml:"cookies_file"`
	MaxFileSize int64  `toml:"max_file_size"`
	MaxDuration int    `toml:"max_duration"`
	Timeout     string `toml:"timeout"`
}

type Video struct {
	Enabled     bool   `toml:"enabled"`
	Dir         string `toml:"dir"`
	TTL         string `toml:"ttl"`
	MaxBytes    int64  `toml:"max_bytes"`
	Frames      int    `toml:"frames"`
	FrameWidth  int    `toml:"frame_width"`
	MaxDuration int    `toml:"max_duration"`
	GroqURL     string `toml:"groq_url"`
	GroqModel   string `toml:"groq_model"`
	GroqKey     string `toml:"-"`
}

type Photo struct {
	Enabled     bool   `toml:"enabled"`
	Dir         string `toml:"dir"`
	TTL         string `toml:"ttl"`
	MaxBytes    int64  `toml:"max_bytes"`
	DirectLimit int    `toml:"direct_limit"`
	CollageCell int    `toml:"collage_cell"`
	Quality     int    `toml:"collage_quality"`
	CollageMax  int    `toml:"collage_max"`
}

type Harness struct {
	Enabled    bool   `toml:"enabled"`
	Binary     string `toml:"binary"`
	Workspace  string `toml:"workspace"`
	SessionDir string `toml:"session_dir"`
	Timeout    string `toml:"timeout"`
	Deploy     bool   `toml:"deploy"`
}

func defaults() Config {
	return Config{
		Telegram: Telegram{},
		LLM:      LLM{MaxRounds: 2},
		Digest:   Digest{Language: "ru"},
		Firecrawl: Firecrawl{
			BaseURL: "https://api.firecrawl.dev",
		},
		Media: Media{
			Dir:         "./data/media",
			YTDLP:       "yt-dlp",
			GalleryDL:   "gallery-dl",
			MaxFileSize: 50 << 20,
			MaxDuration: 600,
			Timeout:     "10m",
		},
		Video: Video{
			Enabled:     true,
			Dir:         "./data/video",
			TTL:         "24h",
			MaxBytes:    1 << 30,
			Frames:      5,
			FrameWidth:  512,
			MaxDuration: 300,
			GroqURL:     "https://api.groq.com/openai/v1",
			GroqModel:   "whisper-large-v3",
		},
		Photo: Photo{
			Enabled:     true,
			Dir:         "./data/photo",
			TTL:         "24h",
			MaxBytes:    256 << 20,
			DirectLimit: 6,
			CollageCell: 320,
			Quality:     80,
			CollageMax:  30,
		},
		Harness: Harness{
			Binary:     "pi",
			Workspace:  ".",
			SessionDir: "./data/pi",
			Timeout:    "20m",
		},
	}
}

// Load читает файл (если он есть) и накладывает секреты из окружения.
func Load(path string) (*Config, error) {
	cfg := defaults()

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("разбор %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Конфиг целиком из дефолтов и окружения — допустимо.
	default:
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}

	applyEnv(&cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyEnv(cfg *Config) {
	cfg.Telegram.Token = os.Getenv("TELEGRAM_BOT_TOKEN")
	cfg.LLM.APIKey = os.Getenv("LLM_API_KEY")
	cfg.Firecrawl.APIKey = os.Getenv("FIRECRAWL_API_KEY")
	cfg.Video.GroqKey = os.Getenv("GROQ_API_KEY")
}

func (c *Config) validate() error {
	if c.Telegram.Token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN не задан")
	}
	if c.LLM.URL == "" || c.LLM.Model == "" {
		return errors.New("в config.toml нужны llm.url и llm.model")
	}
	if c.LLM.MaxRounds <= 0 {
		c.LLM.MaxRounds = 2
	}
	return nil
}

// Allows сообщает, работает ли бот в этом чате. Пустой список — нигде:
// бот молчит, пока чат явно не разрешён.
func (c *Config) Allows(chatID int64) bool {
	for _, id := range c.Telegram.AllowedChats {
		if id == chatID {
			return true
		}
	}
	return false
}

// IsAdmin сообщает, можно ли пользователю звать dev-агента.
func (c *Config) IsAdmin(userID int64) bool {
	for _, id := range c.Telegram.AdminUsers {
		if id == userID {
			return true
		}
	}
	return false
}

// Duration разбирает длительность из конфига; пустое или битое значение даёт fallback.
func Duration(raw string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
