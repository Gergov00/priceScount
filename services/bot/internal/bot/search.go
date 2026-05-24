package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func (b *Bot) handleURLSubmit(ctx context.Context, chatID int64, text string) {
	platform, err := marketplace.DetectPlatform(text)
	if err != nil {
		b.send(chatID, "Пришли ссылку на товар с Wildberries.\n\n"+
			"Пример:\nhttps://www.wildberries.ru/catalog/12345678/detail.aspx")
		return
	}
	if platform == "ozon" {
		b.send(chatID, "Ozon временно недоступен — площадка блокирует автоматические запросы. "+
			"Пришли ссылку с Wildberries.")
		return
	}

	var normalURL string
	switch platform {
	case "wb":
		normalURL, err = marketplace.NormalizeWBURL(text)
	case "ozon":
		normalURL, err = marketplace.NormalizeOzonURL(text)
	}
	if err != nil {
		b.send(chatID, "Не удалось разобрать ссылку. Убедись, что это ссылка на конкретный товар.")
		return
	}

	b.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
	b.send(chatID, "Загружаю информацию о товаре...")

	var product *marketplace.Product
	switch platform {
	case "wb":
		product, err = b.wb.FetchProduct(ctx, normalURL)
	case "ozon":
		product, err = b.ozon.FetchProduct(ctx, normalURL)
	}
	if err != nil {
		slog.Error("fetch product failed", "platform", platform, "url", normalURL, "error", err)
		b.send(chatID, "Не удалось получить информацию о товаре. Проверь ссылку и попробуй снова.")
		return
	}

	sess := &state.Session{
		Step:         state.StepWaitingMinPrice,
		ProductID:    uuid.New().String(),
		ProductName:  product.Name,
		URL:          normalURL,
		Platform:     platform,
		CurrentPrice: product.Price,
	}
	b.state.Set(ctx, chatID, sess)

	hint := fmt.Sprintf("%.0f", product.Price*0.9)
	b.send(chatID, fmt.Sprintf(
		"📦 %s\nТекущая цена: %.0f ₽\n\nУкажи минимальную цену — уведомлю если цена упадёт ниже.\n\nПример: %s",
		product.Name, product.Price, hint,
	))
}

func (b *Bot) handleMinPrice(ctx context.Context, chatID int64, sess *state.Session, text string) {
	price, err := parsePrice(text)
	if err != nil {
		b.send(chatID, "Введи число. Например: 80000")
		return
	}
	sess.MinPrice = price
	sess.Step = state.StepWaitingMaxPrice
	b.state.Set(ctx, chatID, sess)

	hint := ""
	if sess.CurrentPrice > 0 {
		hint = fmt.Sprintf("Текущая цена: %.0f ₽\n\n", sess.CurrentPrice)
	}
	b.send(chatID, hint+"Теперь укажи максимальную цену — уведомлю если цена вырастет выше.\n\nПример: 150000")
}

func (b *Bot) handleMaxPrice(ctx context.Context, chatID int64, sess *state.Session, text string) {
	price, err := parsePrice(text)
	if err != nil {
		b.send(chatID, "Введи число. Например: 150000")
		return
	}
	if price <= sess.MinPrice {
		b.send(chatID, fmt.Sprintf("Максимум должен быть больше минимума (%.0f). Попробуй снова:", sess.MinPrice))
		return
	}

	if _, err := b.db.Exec(ctx,
		`INSERT INTO products(id, name) VALUES($1, $2) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name`,
		sess.ProductID, sess.ProductName,
	); err != nil {
		slog.Error("upsert product failed", "error", err)
		b.send(chatID, "Ошибка сохранения. Попробуй снова.")
		return
	}

	if _, err := b.db.Exec(ctx, `
		INSERT INTO subscriptions(product_id, chat_id, min_price, max_price)
		VALUES($1, $2, $3, $4)
		ON CONFLICT (product_id, chat_id) DO UPDATE
		  SET min_price = EXCLUDED.min_price,
		      max_price = EXCLUDED.max_price,
		      active    = true
	`, sess.ProductID, chatID, sess.MinPrice, price); err != nil {
		slog.Error("upsert subscription failed", "error", err)
		b.send(chatID, "Ошибка сохранения подписки. Попробуй снова.")
		return
	}

	if _, err := b.db.Exec(ctx, `
		INSERT INTO tracked_urls(product_id, url, source)
		VALUES($1, $2, $3)
		ON CONFLICT (url) DO NOTHING
	`, sess.ProductID, sess.URL, sess.Platform); err != nil {
		slog.Error("insert tracked_url failed", "url", sess.URL, "error", err)
	}

	// Publish to discovery.urls so the scheduler picks it up immediately.
	msg := contracts.DiscoveredURL{
		ProductID:    sess.ProductID,
		ProductName:  sess.ProductName,
		URL:          sess.URL,
		Platform:     sess.Platform,
		Source:       sess.Platform,
		DiscoveredAt: time.Now().UTC(),
	}
	if err := b.broker.Publish(ctx, broker.QueueDiscoveryURLs, msg); err != nil {
		slog.Error("publish discovery url failed", "error", err)
	}

	b.state.Clear(ctx, chatID)

	b.send(chatID, fmt.Sprintf(
		"Готово! Слежу за «%s»\n\n%s\n\nДиапазон: %.0f — %.0f ₽\n\nУведомлю если цена выйдет за границы.",
		sess.ProductName, sess.URL, sess.MinPrice, price,
	))
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

