package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	pendingMarker  = "boot.pending"
	rollbackMarker = "boot.rolledback"
	prevBinary     = "bot.prev"
	newBinary      = "bot.new"
)

// Deploy собирает рабочую копию и заменяет текущий бинарник, если сборка прошла.
// Автоматический вызов после dev управляется конфигом (harness.deploy), ручная
// команда deploy вызывает этот метод напрямую.
func (r *Runner) Deploy(ctx context.Context, note string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("путь к бинарнику: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("путь к бинарнику: %w", err)
	}

	if err := r.command(ctx, "git", "add", "-A"); err != nil {
		return "", err
	}
	// Коммитить нечего — это не ошибка: агент мог всё уже закоммитить.
	_ = r.command(ctx, "git", "commit", "-m", "dev: "+firstLine(note))

	if err := r.command(ctx, "go", "vet", "./..."); err != nil {
		return "go vet не прошёл, остаюсь на старом бинарнике:\n" + err.Error(), nil
	}

	target := filepath.Join(filepath.Dir(exe), newBinary)
	if err := r.command(ctx, "go", "build", "-o", target, "./cmd/bot"); err != nil {
		return "сборка не прошла, остаюсь на старом бинарнике:\n" + err.Error(), nil
	}

	previous := filepath.Join(filepath.Dir(exe), prevBinary)
	_ = os.Remove(previous)
	if err := os.Rename(exe, previous); err != nil {
		return "", fmt.Errorf("не смог отложить прошлый бинарник: %w", err)
	}
	if err := os.Rename(target, exe); err != nil {
		_ = os.Rename(previous, exe)
		return "", fmt.Errorf("не смог поставить новый бинарник: %w", err)
	}

	r.logger.Printf("harness: перезапуск в новый бинарник %s", exe)
	return "Перезапускаюсь с новым бинарником.", Exec(exe)
}

// Head возвращает короткий хеш HEAD рабочей копии — для отчёта о деплое.
func (r *Runner) Head(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = r.cfg.Workspace
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r *Runner) command(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.cfg.Workspace

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, tail(string(output), 800))
	}
	return nil
}

// CheckBoot чистит маркеры прошлых версий. Авто-откат на bot.prev отключён:
// Deploy писал маркер перед Exec, и CheckBoot в новом процессе принимал его за
// падение — каждый деплой откатывался назад. Откат теперь ручной: bin/bot.prev.
func CheckBoot() error {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil
	}

	dir := filepath.Dir(exe)
	_ = os.Remove(filepath.Join(dir, pendingMarker))
	_ = os.Remove(filepath.Join(dir, rollbackMarker))
	return nil
}

// MarkBooted снимает маркеры: процесс дожил до рабочего состояния.
func MarkBooted() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	_ = os.Remove(filepath.Join(dir, pendingMarker))
	_ = os.Remove(filepath.Join(dir, rollbackMarker))
}

// Exec заменяет текущий процесс новым бинарником, сохраняя PID и юнит systemd.
func Exec(path string) error {
	return syscall.Exec(path, []string{path}, os.Environ())
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
