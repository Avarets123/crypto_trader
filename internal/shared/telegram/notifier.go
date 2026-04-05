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
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// enabled возвращает true если notifier настроен.
func (n *Notifier) enabled() bool {
	return n.token != "" && n.chatID != ""
}

// Send отправляет текстовое сообщение. Неблокирующий — вызывать в горутине при необходимости.
func (n *Notifier) Send(ctx context.Context, text string) {
	if !n.enabled() {
		return
	}

	body, err := json.Marshal(map[string]string{
		"chat_id":    n.chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	if err != nil {
		n.log.Error("telegram: failed to marshal message", zap.Error(err))
		return
	}

	url := fmt.Sprintf(apiURL, n.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.log.Error("telegram: failed to create request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	n.log.Debug("telegram: sending message", zap.String("text", text))

	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Error("telegram: request failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n.log.Error("telegram: unexpected status",
			zap.Int("status", resp.StatusCode),
			zap.String("response", string(body)),
		)
		return
	}

	n.log.Debug("telegram: message sent successfully")
}
