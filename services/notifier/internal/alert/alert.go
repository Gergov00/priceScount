package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type Alert struct {
	ChatID      int64
	ProductName string
	URL         string
	Price       float64
	Currency    string
	MinPrice    *float64
	MaxPrice    *float64
}

func Fire(token string, a Alert) {
	text := buildText(a)
	if err := sendWithRetry(token, a.ChatID, text); err != nil {
		slog.Error("telegram send failed", "chat_id", a.ChatID, "error", err)
	}
}

// FireCurrent sends a plain "current price" message for force-check results.
func FireCurrent(token string, a Alert) {
	cur := a.Currency
	if cur == "" {
		cur = "₽"
	}
	text := fmt.Sprintf("💰 Текущая цена: %.0f %s\n\n%s\n%s", a.Price, cur, a.ProductName, a.URL)
	if err := sendWithRetry(token, a.ChatID, text); err != nil {
		slog.Error("telegram send failed", "chat_id", a.ChatID, "error", err)
	}
}

func buildText(a Alert) string {
	cur := a.Currency
	if cur == "" {
		cur = "₽"
	}

	if a.MinPrice != nil && a.Price < *a.MinPrice {
		return fmt.Sprintf(
			"📉 Цена упала!\n\n%s\n\nЦена: %.0f %s\nВаш минимум: %.0f %s\n\n%s",
			a.ProductName, a.Price, cur, *a.MinPrice, cur, a.URL,
		)
	}
	return fmt.Sprintf(
		"📈 Цена выросла!\n\n%s\n\nЦена: %.0f %s\nВаш максимум: %.0f %s\n\n%s",
		a.ProductName, a.Price, cur, *a.MaxPrice, cur, a.URL,
	)
}

// sendWithRetry tries up to 4 times with 3s, 6s, 12s backoff.
func sendWithRetry(token string, chatID int64, text string) error {
	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	var lastErr error
	for _, d := range delays {
		if d > 0 {
			time.Sleep(d)
		}
		if lastErr = sendTelegram(token, chatID, text); lastErr == nil {
			return nil
		}
		slog.Warn("telegram send attempt failed, retrying", "chat_id", chatID, "error", lastErr)
	}
	return lastErr
}

func sendTelegram(token string, chatID int64, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
