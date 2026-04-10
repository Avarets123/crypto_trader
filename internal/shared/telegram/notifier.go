package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

const apiURL = "https://api.telegram.org/bot%s/sendMessage"

// tgErrorResponse — тело ответа Telegram при ошибке.
type tgErrorResponse struct {
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Notifier отправляет сообщения в Telegram-чат.
type Notifier struct {
	token  string
	chatID string
	log    *zap.Logger
	client *http.Client
}

// New создаёт Notifier. Если token или chatID пустые — возвращает nil-совместимый объект,
// который молча пропускает все отправки.
func New(token, chatID string, log *zap.Logger) *Notifier {
	if token == "" || chatID == "" {
		log.Warn("telegram notifier disabled: TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set")
		return &Notifier{log: log}
	}
	log.Info("telegram notifier enabled", zap.String("chat_id", chatID))
	return &Notifier{
		token:  token,
		chatID: chatID,
		log:    log,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// enabled возвращает true если notifier настроен.
func (n *Notifier) enabled() bool {
	return n.token != "" && n.chatID != ""
}

// Send отправляет текстовое сообщение. Неблокирующий — вызывать в горутине при необходимости.
func (n *Notifier) Send(ctx context.Context, text string) {
	n.SendToThread(ctx, text, 0)
}

// SendToThread отправляет сообщение в конкретный топик (thread) группы.
// Если threadID <= 0 — поведение идентично Send (без message_thread_id).
// При 429 Too Many Requests автоматически ждёт retry_after секунд и повторяет (до 3 попыток).
func (n *Notifier) SendToThread(ctx context.Context, text string, threadID int) {
	if !n.enabled() {
		return
	}

	payload := map[string]any{
		"chat_id":                  n.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		n.log.Error("telegram: failed to marshal message", zap.Error(err))
		return
	}

	n.log.Debug("telegram: sending message",
		zap.String("text", text),
		zap.Int("thread_id", threadID),
	)

	url := fmt.Sprintf(apiURL, n.token)
	const maxAttempts = 3

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			n.log.Error("telegram: failed to create request", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.client.Do(req)
		if err != nil {
			n.log.Warn("telegram: request failed, retrying",
				zap.Error(err),
				zap.Int("attempt", attempt),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			n.log.Debug("telegram: message sent successfully", zap.Int("thread_id", threadID))
			return
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := 1
			var errResp tgErrorResponse
			if json.Unmarshal(respBody, &errResp) == nil && errResp.Parameters.RetryAfter > 0 {
				retryAfter = errResp.Parameters.RetryAfter
			}
			n.log.Warn("telegram: rate limited, retrying",
				zap.Int("retry_after_sec", retryAfter),
				zap.Int("attempt", attempt),
				zap.Int("max_attempts", maxAttempts),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(retryAfter) * time.Second):
			}
			continue
		}

		n.log.Error("telegram: unexpected status",
			zap.Int("status", resp.StatusCode),
			zap.String("response", string(respBody)),
		)
		return
	}

	n.log.Error("telegram: gave up after retries",
		zap.Int("attempts", maxAttempts),
		zap.Int("thread_id", threadID),
	)
}
