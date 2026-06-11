package alert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// errPermanent marks Telegram API rejections that retrying cannot fix
// (bot blocked by the user, malformed chat_id, message too long, ...).
var errPermanent = errors.New("permanent telegram error")

// SendWithRetry sends text to a Telegram chat, retrying up to 4 times on
// transient failures. Permanent 4xx rejections are not retried.
func SendWithRetry(token string, chatID int64, text string) error {
	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	var lastErr error
	for _, d := range delays {
		if d > 0 {
			time.Sleep(d)
		}
		lastErr = send(token, chatID, text)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, errPermanent) {
			return lastErr
		}
		slog.Warn("telegram send attempt failed, retrying", "chat_id", chatID, "error", lastErr)
	}
	return lastErr
}

func send(token string, chatID int64, text string) error {
	// Plain text on purpose: product names come from scraped pages, and any
	// markup characters in them would make parse_mode fail the whole message.
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	apiErr := fmt.Sprintf("telegram api status %d", resp.StatusCode)
	if b, readErr := io.ReadAll(io.LimitReader(resp.Body, 512)); readErr == nil && len(b) > 0 {
		apiErr += ": " + string(b)
	}
	// 429 and 5xx are transient; other 4xx will fail the same way every time.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%s: %w", apiErr, errPermanent)
	}
	return errors.New(apiErr)
}
