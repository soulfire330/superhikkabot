package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	groqTimeout          = 60 * time.Second
	groqMaxResponseBytes = 10 << 20
	groqMaxAudioBytes    = 24 << 20
)

// Transcriber — распознавание речи через Groq Whisper.
type Transcriber struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewTranscriber собирает клиент; пустой ключ делает распознавание выключенным.
func NewTranscriber(apiKey, baseURL, model string) *Transcriber {
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1"
	}
	if model == "" {
		model = "whisper-large-v3"
	}

	return &Transcriber{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: groqTimeout},
	}
}

// Enabled сообщает, настроен ли ключ.
func (t *Transcriber) Enabled() bool { return t != nil && t.apiKey != "" }

// Transcribe отправляет аудио в Whisper и возвращает речь без музыки и тишины.
func (t *Transcriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if !t.Enabled() {
		return "", fmt.Errorf("GROQ_API_KEY не задан")
	}

	info, err := os.Stat(audioPath)
	if err != nil {
		return "", fmt.Errorf("аудиофайл: %w", err)
	}
	if info.Size() > groqMaxAudioBytes {
		return "", fmt.Errorf("аудиофайл больше %d байт", groqMaxAudioBytes)
	}

	file, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("открытие аудиофайла: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", fmt.Errorf("multipart file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("чтение аудиофайла: %w", err)
	}

	for key, value := range map[string]string{
		"model":           t.model,
		"response_format": "verbose_json",
		"temperature":     "0",
	} {
		if err := writer.WriteField(key, value); err != nil {
			return "", fmt.Errorf("multipart field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("multipart close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("создание запроса Groq: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("запрос Groq: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, groqMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("чтение ответа Groq: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Groq вернул HTTP %d: %s", resp.StatusCode, string(raw[:min(len(raw), 300)]))
	}

	var result struct {
		Text     string `json:"text"`
		Segments []struct {
			Text         string  `json:"text"`
			NoSpeechProb float64 `json:"no_speech_prob"`
			AvgLogprob   float64 `json:"avg_logprob"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("разбор ответа Groq: %w", err)
	}

	return joinSegments(result.Text, result.Segments), nil
}

func joinSegments(fallback string, segments []struct {
	Text         string  `json:"text"`
	NoSpeechProb float64 `json:"no_speech_prob"`
	AvgLogprob   float64 `json:"avg_logprob"`
}) string {
	if len(segments) == 0 {
		return strings.TrimSpace(fallback)
	}

	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		// Музыка и тишина иначе превращаются в галлюцинации вида «la la la».
		if segment.NoSpeechProb > 0.5 || segment.AvgLogprob < -1.0 {
			continue
		}
		if text := strings.TrimSpace(segment.Text); text != "" {
			parts = append(parts, text)
		}
	}

	return strings.TrimSpace(strings.Join(parts, " "))
}
