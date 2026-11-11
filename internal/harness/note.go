package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DeployNote — записка для нового процесса: какое сообщение отредактировать
// после самоперезапуска. Пишется перед Exec и удаляется при чтении.
type DeployNote struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id"`
}

func notePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "deploy-note.json")
}

// SaveDeployNote сохраняет записку перед перезапуском.
func SaveDeployNote(note DeployNote) error {
	path := notePath()
	if path == "" {
		return nil
	}
	data, err := json.Marshal(note)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadDeployNote читает и удаляет записку. ok=false, если её нет.
func LoadDeployNote() (DeployNote, bool) {
	path := notePath()
	if path == "" {
		return DeployNote{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DeployNote{}, false
	}
	_ = os.Remove(path)

	var note DeployNote
	if json.Unmarshal(data, &note) != nil {
		return DeployNote{}, false
	}
	if note.ChatID == 0 || note.MessageID == 0 {
		return DeployNote{}, false
	}
	return note, true
}

// ClearDeployNote удаляет записку, если перезапуск не состоялся.
func ClearDeployNote() {
	if path := notePath(); path != "" {
		_ = os.Remove(path)
	}
}
