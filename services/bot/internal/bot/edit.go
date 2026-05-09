package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func (b *Bot) startEditSubscription(ctx context.Context, chatID int64, subID string) {
	var name string
	var minPrice, maxPrice *float64
	err := b.db.QueryRow(ctx, `
		SELECT p.name, s.min_price, s.max_price
		FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&name, &minPrice, &maxPrice)
	if err != nil {
		b.send(chatID, "Подписка не найдена.")
		return
	}

	var oldMin, oldMax float64
	if minPrice != nil {
		oldMin = *minPrice
	}
	if maxPrice != nil {
		oldMax = *maxPrice
	}

	sess := &state.Session{
		Step:         state.StepEditingMinPrice,
		EditingSubID: subID,
		OldMinPrice:  oldMin,
		OldMaxPrice:  oldMax,
	}
	b.state.Set(ctx, chatID, sess)
	b.send(chatID, fmt.Sprintf(
		"Редактирую %q\n\nУкажи новую минимальную цену (сейчас: %.0f ₽):",
		name, oldMin,
	))
}

func (b *Bot) handleEditMinPrice(ctx context.Context, chatID int64, sess *state.Session, text string) {
	price, err := parsePrice(text)
	if err != nil {
		b.send(chatID, "Введи число. Например: 80000")
		return
	}
	sess.MinPrice = price
	sess.Step = state.StepEditingMaxPrice
	b.state.Set(ctx, chatID, sess)
	b.send(chatID, fmt.Sprintf(
		"Теперь укажи новую максимальную цену (сейчас: %.0f ₽):",
		sess.OldMaxPrice,
	))
}

func (b *Bot) handleEditMaxPrice(ctx context.Context, chatID int64, sess *state.Session, text string) {
	price, err := parsePrice(text)
	if err != nil {
		b.send(chatID, "Введи число. Например: 150000")
		return
	}
	if price <= sess.MinPrice {
		b.send(chatID, fmt.Sprintf("Максимум должен быть больше минимума (%.0f). Попробуй снова:", sess.MinPrice))
		return
	}

	if _, err := b.db.Exec(ctx, `
		UPDATE subscriptions SET min_price = $1, max_price = $2 WHERE id = $3 AND chat_id = $4
	`, sess.MinPrice, price, sess.EditingSubID, chatID); err != nil {
		slog.Error("update subscription failed", "error", err)
		b.send(chatID, "Ошибка обновления. Попробуй снова.")
		return
	}

	b.state.Clear(ctx, chatID)
	b.send(chatID, fmt.Sprintf(
		"Готово! Новый диапазон: %.0f — %.0f ₽\n\n/mylist — посмотреть все товары",
		sess.MinPrice, price,
	))
}
