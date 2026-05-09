package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type subscription struct {
	ID       string
	Name     string
	MinPrice *float64
	MaxPrice *float64
	Paused   bool
}

func (b *Bot) handleMyList(ctx context.Context, chatID int64) {
	subs, err := b.userSubscriptions(ctx, chatID)
	if err != nil {
		slog.Error("userSubscriptions failed", "error", err)
		b.send(chatID, "Не удалось загрузить список.")
		return
	}
	if len(subs) == 0 {
		b.send(chatID, "У тебя пока нет отслеживаемых товаров.\n\nНапиши название товара чтобы начать.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Твои товары (%d):\n", len(subs)))
	for i, s := range subs {
		status := ""
		if s.Paused {
			status = " ⏸"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s%s\n", i+1, s.Name, status))
		if s.MinPrice != nil && s.MaxPrice != nil {
			sb.WriteString(fmt.Sprintf("   %.0f — %.0f ₽\n", *s.MinPrice, *s.MaxPrice))
		}
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(subs))
	for _, s := range subs {
		name := s.Name
		if len(name) > 18 {
			name = name[:18] + "…"
		}
		pauseBtn := tgbotapi.NewInlineKeyboardButtonData("⏸", "pause_sub:"+s.ID)
		if s.Paused {
			pauseBtn = tgbotapi.NewInlineKeyboardButtonData("▶", "resume_sub:"+s.ID)
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ "+name, "edit_sub:"+s.ID),
			pauseBtn,
			tgbotapi.NewInlineKeyboardButtonData("❌", "del_sub:"+s.ID),
		))
	}

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	b.api.Send(msg)
}

func (b *Bot) userSubscriptions(ctx context.Context, chatID int64) ([]subscription, error) {
	rows, err := b.db.Query(ctx, `
		SELECT s.id, p.name, s.min_price, s.max_price, s.paused
		FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.chat_id = $1 AND s.active = true
		ORDER BY s.created_at
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []subscription
	for rows.Next() {
		var s subscription
		if err := rows.Scan(&s.ID, &s.Name, &s.MinPrice, &s.MaxPrice, &s.Paused); err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func (b *Bot) pauseSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	var name string
	b.db.QueryRow(ctx, `
		SELECT p.name FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&name)

	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET paused = true WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("pause subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}

	b.api.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
	b.send(chatID, fmt.Sprintf("⏸ Отслеживание %q поставлено на паузу.\n\n/mylist — управление товарами", name))
}

func (b *Bot) resumeSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	var name string
	b.db.QueryRow(ctx, `
		SELECT p.name FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&name)

	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET paused = false WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("resume subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}

	b.api.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
	b.send(chatID, fmt.Sprintf("▶ Отслеживание %q возобновлено.\n\n/mylist — управление товарами", name))
}

func (b *Bot) deleteSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	var name string
	b.db.QueryRow(ctx, `
		SELECT p.name FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&name)

	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET active = false WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("delete subscription failed", "error", err)
		b.send(chatID, "Ошибка удаления.")
		return
	}

	b.api.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
	b.send(chatID, fmt.Sprintf("Удалено: %q\n\n/mylist — посмотреть оставшиеся товары", name))
}
