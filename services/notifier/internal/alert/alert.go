package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// TelegramSender delivers messages to Telegram with retry.
type TelegramSender struct {
	token string
}

func NewTelegramSender(token string) *TelegramSender {
	return &TelegramSender{token: token}
}

func (s *TelegramSender) Send(chatID int64, text string) error {
	return SendWithRetry(s.token, chatID, text)
}

// SendWithRetry sends text to a Telegram chat, retrying up to 4 times on failure.
func SendWithRetry(token string, chatID int64, text string) error {
	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	var lastErr error
	for _, d := range delays {
		if d > 0 {
			time.Sleep(d)
		}
		if lastErr = send(token, chatID, text); lastErr == nil {
			return nil
		}
		slog.Warn("telegram send attempt failed, retrying", "chat_id", chatID, "error", lastErr)
	}
	return lastErr
}

func send(token string, chatID int64, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
