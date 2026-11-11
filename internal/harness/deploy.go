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
// Возвращает описание: собралось или нет. При успехе процесс не возвращается.
func (r *Runner) Deploy(ctx context.Context, note string) (string, error) {
	if !r.cfg.Deploy {
		return "", fmt.Errorf("самоперезапуск выключен: harness.deploy = false")
	}

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

	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), pendingMarker), []byte("1"), 0o644); err != nil {
		return "", fmt.Errorf("маркер перезапуска: %w", err)
	}

	r.logger.Printf("harness: перезапуск в новый бинарник %s", exe)
	return "Перезапускаюсь с новым бинарником.", Exec(exe)
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

// CheckBoot откатывается на прошлый бинарник, если предыдущий запуск не дожил до MarkBooted.
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
	pending := filepath.Join(dir, pendingMarker)
	if _, err := os.Stat(pending); err != nil {
		return nil
	}

	// Уже работаем откаченным бинарником: второй откат не нужен.
	if filepath.Base(exe) == prevBinary {
		_ = os.Remove(pending)
		return nil
	}

	previous := filepath.Join(dir, prevBinary)
	if _, err := os.Stat(previous); err != nil {
		_ = os.Remove(pending)
		return nil
	}

	_ = os.Rename(pending, filepath.Join(dir, rollbackMarker))
	return Exec(previous)
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
