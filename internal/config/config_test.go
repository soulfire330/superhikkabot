package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[telegram]
allowed_chats = [-100, 42]
admin_users = [7]

[llm]
url = "https://llm.example/v1/chat/completions"
model = "test-model"

[media]
timeout = "3m"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("LLM_API_KEY", "llm-key")
	t.Setenv("GROQ_API_KEY", "groq-key")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Telegram.Token != "token" {
		t.Errorf("токен из окружения не подхватился: %q", cfg.Telegram.Token)
	}
	if cfg.LLM.APIKey != "llm-key" || cfg.Video.GroqKey != "groq-key" {
		t.Error("ключи из окружения не подхватились")
	}
	if !cfg.Allows(42) || !cfg.Allows(-100) || cfg.Allows(43) {
		t.Error("список разрешённых чатов работает неверно")
	}
	if !cfg.IsAdmin(7) || cfg.IsAdmin(8) {
		t.Error("список админов работает неверно")
	}
	if got := Duration(cfg.Media.Timeout, time.Minute); got != 3*time.Minute {
		t.Errorf("timeout = %v, ждали 3m", got)
	}
	// Незаданные поля остаются дефолтными.
	if cfg.Video.Frames != 5 || cfg.Digest.Language != "ru" {
		t.Errorf("дефолты не применились: %+v", cfg.Video)
	}
}

func TestLoadWithoutTokenFails(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil {
		t.Fatal("без токена конфиг должен падать")
	}
}

func TestDurationFallback(t *testing.T) {
	if got := Duration("чепуха", time.Minute); got != time.Minute {
		t.Errorf("битая длительность дала %v", got)
	}
	if got := Duration("", time.Second); got != time.Second {
		t.Errorf("пустая длительность дала %v", got)
	}
}
