package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	if err := sendTelegram(token, a.ChatID, text); err != nil {
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
