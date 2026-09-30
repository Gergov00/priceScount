package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func (b *Bot) startEditSubscription(ctx context.Context, chatID int64, subID string) {
	sub, err := b.findSubscription(ctx, chatID, subID)
	if err != nil {
		b.send(chatID, "Подписка не найдена.")
		return
	}

	var oldMin, oldMax float64
	if sub.MinPrice != nil {
		oldMin = *sub.MinPrice
	}
	if sub.MaxPrice != nil {
		oldMax = *sub.MaxPrice
	}

	current := b.state.Get(chatID)
	b.state.Set(chatID, &state.Session{
		Step:         state.StepEditingMinPrice,
		EditingSubID: subID,
		OldMinPrice:  oldMin,
		OldMaxPrice:  oldMax,
		Page:         current.Page,
	})
	b.send(chatID, fmt.Sprintf(
		"Редактирую %q\n\nУкажи новую минимальную цену (сейчас: %.0f ₽):",
		sub.ProductName, oldMin,
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
	b.state.Set(chatID, sess)
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

	if err := b.gw.EditSubscription(ctx, chatID, sess.EditingSubID, sess.MinPrice, price); err != nil {
		slog.Error("edit subscription failed", "error", err)
		b.send(chatID, "Ошибка обновления. Попробуй снова.")
		return
	}

	b.state.Set(chatID, &state.Session{Step: state.StepIdle, Page: sess.Page})
	b.send(chatID, fmt.Sprintf(
		"Готово! Новый диапазон: %.0f — %.0f ₽\n\n/mylist — посмотреть все товары",
		sess.MinPrice, price,
	))
}
