package video

import (
	"context"
	"log"
	"os"
)

// Service отвечает по содержимому видео: речь и кадры по первому вопросу.
type Service struct {
	store  *Store
	stt    *Transcriber
	logger *log.Logger
}

// NewService собирает сервис.
func NewService(store *Store, stt *Transcriber, logger *log.Logger) *Service {
	return &Service{store: store, stt: stt, logger: logger}
}

// Context — то, что уходит в модель.
type Context struct {
	Transcript string
	Frames     [][]byte
	Duration   int
}

// SaveVideo проксирует сохранение артефактов из медиа-сервиса.
func (s *Service) SaveVideo(ctx context.Context, sourceURL, videoPath string, chatID int64, messageID, duration int) error {
	return s.store.SaveVideo(ctx, sourceURL, videoPath, chatID, messageID, duration)
}

// Context собирает транскрипт (один раз) и кадры. Второе значение — есть ли что показывать.
func (s *Service) Context(ctx context.Context, chatID int64, messageID int) (*Context, bool) {
	snapshot, ok := s.store.Get(chatID, messageID)
	if !ok {
		return nil, false
	}

	transcript := snapshot.Transcript
	if transcript == "" && snapshot.HasAudio && s.stt.Enabled() {
		text, err := s.stt.Transcribe(ctx, snapshot.AudioPath)
		if err != nil {
			s.logger.Printf("video: STT не сработал: %v", err)
		} else {
			transcript = text
			s.store.SaveTranscript(chatID, messageID, text)
		}
	}

	frames := readFrames(snapshot.Frames)
	if transcript == "" && len(frames) == 0 {
		return nil, false
	}

	return &Context{Transcript: transcript, Frames: frames, Duration: snapshot.Duration}, true
}

func readFrames(paths []string) [][]byte {
	frames := make([][]byte, 0, len(paths))

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		frames = append(frames, data)
	}

	return frames
}
