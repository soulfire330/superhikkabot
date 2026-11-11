// Package photo — ленивая загрузка фото из Telegram, кэш по file_id и коллажи.
package photo

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"superhikkabot/internal/tg"
)

const (
	maxAlbumPhotos = 30
	janitorTick    = 10 * time.Minute
)

// Config — настройки кэша и коллажей.
type Config struct {
	Dir         string
	TTL         time.Duration
	MaxBytes    int64
	DirectLimit int
	CellSize    int
	Quality     int
	CollageMax  int
	Logger      *log.Logger
}

// Service знает, какие фото бот видел в чате, и умеет отдавать их байты.
type Service struct {
	tg  *tg.Client
	cfg Config

	mu       sync.Mutex
	bindings map[int64]map[int][]string
	groups   map[string][]string
	msgGroup map[int64]map[int]string
	singles  map[int64]map[int]string
	expires  map[int64]map[int]time.Time

	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// Context — картинки для одного вопроса.
type Context struct {
	Images [][]byte
	Count  int
}

// NewService собирает сервис.
func NewService(client *tg.Client, cfg Config) *Service {
	if cfg.Dir == "" {
		cfg.Dir = "./data/photo"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 256 << 20
	}
	if cfg.DirectLimit <= 0 {
		cfg.DirectLimit = 6
	}
	if cfg.CellSize <= 0 {
		cfg.CellSize = 320
	}
	if cfg.Quality <= 0 {
		cfg.Quality = 80
	}
	if cfg.CollageMax <= 0 {
		cfg.CollageMax = 30
	}

	return &Service{
		tg:       client,
		cfg:      cfg,
		bindings: make(map[int64]map[int][]string),
		groups:   make(map[string][]string),
		msgGroup: make(map[int64]map[int]string),
		singles:  make(map[int64]map[int]string),
		expires:  make(map[int64]map[int]time.Time),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start запускает уборку кэша.
func (s *Service) Start() {
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
func (s *Service) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// Observe запоминает фото из обновления: одиночное или часть альбома.
func (s *Service) Observe(msg *models.Message) {
	if len(msg.Photo) == 0 {
		return
	}

	fileID := largestPhoto(msg.Photo)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.expire(msg.Chat.ID, msg.ID)

	if msg.MediaGroupID == "" {
		if s.singles[msg.Chat.ID] == nil {
			s.singles[msg.Chat.ID] = make(map[int]string)
		}
		s.singles[msg.Chat.ID][msg.ID] = fileID
		return
	}

	if s.msgGroup[msg.Chat.ID] == nil {
		s.msgGroup[msg.Chat.ID] = make(map[int]string)
	}
	s.msgGroup[msg.Chat.ID][msg.ID] = msg.MediaGroupID

	files := s.groups[msg.MediaGroupID]
	for _, existing := range files {
		if existing == fileID {
			return
		}
	}
	if len(files) >= maxAlbumPhotos {
		return
	}
	s.groups[msg.MediaGroupID] = append(files, fileID)
}

func (s *Service) expire(chatID int64, messageID int) {
	if s.expires[chatID] == nil {
		s.expires[chatID] = make(map[int]time.Time)
	}
	s.expires[chatID][messageID] = time.Now().Add(s.cfg.TTL)
}

// BindGroup сохраняет копии отправленных ботом фото и привязывает их к сообщениям.
func (s *Service) BindGroup(ctx context.Context, sourceURL string, paths []string, chatID int64, messageIDs []int) error {
	if len(paths) == 0 || len(messageIDs) == 0 {
		return nil
	}

	copies := make([]string, 0, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("чтение фото: %w", err)
		}
		cached, err := s.write(fmt.Sprintf("sent|%s|%d", sourceURL, i), data)
		if err != nil {
			return err
		}
		copies = append(copies, cached)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bindings[chatID] == nil {
		s.bindings[chatID] = make(map[int][]string)
	}
	for _, id := range messageIDs {
		s.bindings[chatID][id] = copies
		s.expire(chatID, id)
	}

	return nil
}

// Context отдаёт картинки сообщения: скачивает лениво, склеивает в коллаж при необходимости.
func (s *Service) Context(ctx context.Context, chatID int64, messageID int) (*Context, bool) {
	paths := s.localPaths(chatID, messageID)
	fileIDs := s.remoteIDs(chatID, messageID)

	if len(paths) == 0 && len(fileIDs) == 0 {
		return nil, false
	}

	images := make([][]byte, 0, len(paths)+len(fileIDs))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		images = append(images, data)
	}

	for _, fileID := range fileIDs {
		if len(images) >= maxAlbumPhotos {
			break
		}
		data, err := s.download(ctx, fileID)
		if err != nil {
			s.cfg.Logger.Printf("photo: не скачал %s: %v", fileID, err)
			continue
		}
		images = append(images, data)
	}

	if len(images) == 0 {
		return nil, false
	}

	count := len(images)
	if count > s.cfg.DirectLimit {
		collages, err := BuildCollages(images, CollageOptions{
			CellSize:  s.cfg.CellSize,
			Quality:   s.cfg.Quality,
			MaxImages: s.cfg.CollageMax,
		})
		if err == nil && len(collages) > 0 {
			return &Context{Images: collages, Count: count}, true
		}
		s.cfg.Logger.Printf("photo: коллаж не собрался: %v", err)
	}

	return &Context{Images: images, Count: count}, true
}

func (s *Service) localPaths(chatID int64, messageID int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if bound, ok := s.bindings[chatID]; ok {
		if paths, ok := bound[messageID]; ok {
			return paths
		}
	}
	return nil
}

func (s *Service) remoteIDs(chatID int64, messageID int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if groups, ok := s.msgGroup[chatID]; ok {
		if groupID, ok := groups[messageID]; ok {
			return s.groups[groupID]
		}
	}
	if singles, ok := s.singles[chatID]; ok {
		if fileID, ok := singles[messageID]; ok {
			return []string{fileID}
		}
	}
	return nil
}

// download качает фото по file_id и кэширует его на диске.
func (s *Service) download(ctx context.Context, fileID string) ([]byte, error) {
	path, _ := s.cachePath(fileID)
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}

	data, err := s.tg.Download(ctx, fileID)
	if err != nil {
		return nil, err
	}

	if _, err := s.write(fileID, data); err != nil {
		return nil, err
	}

	return data, nil
}

func (s *Service) write(key string, data []byte) (string, error) {
	path, err := s.cachePath(key)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("запись фото в кэш: %w", err)
	}

	return path, nil
}

func (s *Service) cachePath(key string) (string, error) {
	if err := os.MkdirAll(s.cfg.Dir, 0o755); err != nil {
		return "", fmt.Errorf("каталог кэша фото: %w", err)
	}

	sum := sha1.Sum([]byte(key))
	return filepath.Join(s.cfg.Dir, hex.EncodeToString(sum[:8])+".img"), nil
}

// cleanup чистит кэш по TTL и по размеру.
func (s *Service) cleanup() {
	s.mu.Lock()
	now := time.Now()
	for chatID, messages := range s.expires {
		for messageID, expires := range messages {
			if now.After(expires) {
				delete(messages, messageID)
				delete(s.bindings[chatID], messageID)
				delete(s.singles[chatID], messageID)
			}
		}
		if len(messages) == 0 {
			delete(s.expires, chatID)
		}
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return
	}

	var total int64
	type fileInfo struct {
		path    string
		modTime time.Time
		size    int64
	}
	files := make([]fileInfo, 0, len(entries))

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.cfg.Dir, entry.Name())

		if now.Sub(info.ModTime()) > s.cfg.TTL {
			_ = os.Remove(path)
			continue
		}

		files = append(files, fileInfo{path: path, modTime: info.ModTime(), size: info.Size()})
		total += info.Size()
	}

	// Сначала сносим самые старые, пока не влезем в лимит.
	for i := 0; total > s.cfg.MaxBytes && i < len(files); i++ {
		oldest := files[i]
		for _, candidate := range files[i+1:] {
			if candidate.modTime.Before(oldest.modTime) {
				oldest = candidate
			}
		}
		_ = os.Remove(oldest.path)
		total -= oldest.size
	}
}

func largestPhoto(sizes []models.PhotoSize) string {
	best := sizes[0]
	for _, size := range sizes[1:] {
		if size.Width*size.Height > best.Width*best.Height {
			best = size
		}
	}
	return best.FileID
}

// Supported проверяет, что сообщение вообще содержит фото.
func Supported(msg *models.Message) bool {
	return len(msg.Photo) > 0
}
