package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func (b *Bot) handleURLSubmit(ctx context.Context, chatID int64, text string) {
	if !strings.Contains(text, "wildberries.ru") {
		b.send(chatID, "Пришли ссылку на товар с Wildberries.\n\n"+
			"Пример:\nhttps://www.wildberries.ru/catalog/12345678/detail.aspx")
		return
	}

	b.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
	b.send(chatID, "Загружаю информацию о товаре...")

	lookupID, err := b.gw.StartLookup(ctx, text)
	if err != nil {
		slog.Error("start lookup failed", "url", text, "error", err)
		b.send(chatID, "Не удалось запустить поиск товара. Проверь ссылку и попробуй снова.")
		return
	}

	b.state.Set(chatID, &state.Session{
		Step:     state.StepWaitingLookup,
		LookupID: lookupID,
	})

	go b.pollLookup(ctx, chatID, lookupID)
}

func (b *Bot) pollLookup(ctx context.Context, chatID int64, lookupID string) {
	pollCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	result, err := b.gw.PollLookup(pollCtx, lookupID)
	if err != nil {
		slog.Error("poll lookup failed", "lookup_id", lookupID, "error", err)
		// only notify if the user is still waiting for this lookup
		if sess := b.state.Get(chatID); sess.LookupID == lookupID {
			b.state.Clear(chatID)
			b.send(chatID, "Не удалось получить информацию о товаре. Попробуй снова.")
		}
		return
	}

	if result.Status == "failed" {
		if sess := b.state.Get(chatID); sess.LookupID == lookupID {
			b.state.Clear(chatID)
			b.send(chatID, "Не удалось получить информацию о товаре. Проверь ссылку и попробуй снова.")
		}
		return
	}

	sess := b.state.Get(chatID)
	if sess.Step != state.StepWaitingLookup || sess.LookupID != lookupID {
		return // user cancelled or started a new lookup
	}

	b.state.Set(chatID, &state.Session{
		Step:         state.StepWaitingMinPrice,
		LookupID:     lookupID,
		ProductName:  result.Name,
		ProductURL:   result.URL,
		CurrentPrice: result.Price,
	})

	hint := fmt.Sprintf("%.0f", result.Price*0.9)
	b.send(chatID, fmt.Sprintf(
		"📦 %s\nТекущая цена: %.0f ₽\n\nУкажи минимальную цену — уведомлю если цена упадёт ниже.\n\nПример: %s",
		result.Name, result.Price, hint,
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
	b.state.Set(chatID, sess)

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

	resp, err := b.gw.CreateSubscription(ctx, gateway.CreateSubscriptionRequest{
		ChatID:   chatID,
		LookupID: sess.LookupID,
		MinPrice: sess.MinPrice,
		MaxPrice: price,
	})
	if err != nil {
		slog.Error("create subscription failed", "error", err)
		b.send(chatID, "Ошибка сохранения подписки. Попробуй снова.")
		return
	}
	_ = resp

	b.state.Clear(chatID)
	b.send(chatID, fmt.Sprintf(
		"Готово! Слежу за «%s»\n\n%s\n\nДиапазон: %.0f — %.0f ₽\n\nУведомлю если цена выйдет за границы.",
		sess.ProductName, sess.ProductURL, sess.MinPrice, price,
	))
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
