package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
)

func (b *Bot) handleMyList(ctx context.Context, chatID int64) {
	subs, err := b.gw.ListSubscriptions(ctx, chatID)
	if err != nil {
		slog.Error("list subscriptions failed", "error", err)
		b.send(chatID, "Не удалось загрузить список.")
		return
	}
	if len(subs) == 0 {
		b.send(chatID, "У тебя пока нет отслеживаемых товаров.\n\nПришли ссылку на товар с Wildberries чтобы начать.")
		return
	}

	text, keyboard := buildMyList(subs)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = keyboard
	b.api.Send(msg)
}

func (b *Bot) refreshMyList(ctx context.Context, chatID int64, messageID int) {
	subs, err := b.gw.ListSubscriptions(ctx, chatID)
	if err != nil {
		slog.Error("list subscriptions failed on refresh", "error", err)
		return
	}
	if len(subs) == 0 {
		edit := tgbotapi.NewEditMessageText(chatID, messageID,
			"У тебя больше нет отслеживаемых товаров.\n\nПришли ссылку на товар чтобы начать.")
		b.api.Send(edit)
		return
	}

	text, keyboard := buildMyList(subs)
	editText := tgbotapi.NewEditMessageText(chatID, messageID, text)
	editText.DisableWebPagePreview = true
	b.api.Send(editText)
	b.api.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, messageID, keyboard))
}

func buildMyList(subs []gateway.Subscription) (string, tgbotapi.InlineKeyboardMarkup) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Твои товары (%d):\n", len(subs)))
	for i, s := range subs {
		status := ""
		if s.Paused {
			status = " ⏸"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s%s\n", i+1, s.ProductName, status))
		if s.MinPrice != nil && s.MaxPrice != nil {
			sb.WriteString(fmt.Sprintf("   %.0f — %.0f ₽\n", *s.MinPrice, *s.MaxPrice))
		}
		if s.ProductURL != "" {
			sb.WriteString(fmt.Sprintf("   • %s\n", s.ProductURL))
		}
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(subs)*3)
	for _, s := range subs {
		name := truncate(s.ProductName, 22)
		pauseBtn := tgbotapi.NewInlineKeyboardButtonData("⏸", "pause_sub:"+s.ID)
		if s.Paused {
			pauseBtn = tgbotapi.NewInlineKeyboardButtonData("▶", "resume_sub:"+s.ID)
		}
		rows = append(rows,
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✏️ "+name, "edit_sub:"+s.ID),
				pauseBtn,
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📊 История", "history_sub:"+s.ID),
				tgbotapi.NewInlineKeyboardButtonData("🔄 Проверить", "check_sub:"+s.ID),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить «"+truncate(s.ProductName, 20)+"»", "del_sub:"+s.ID),
			),
		)
	}

	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (b *Bot) findSubscription(ctx context.Context, chatID int64, subID string) (*gateway.Subscription, error) {
	subs, err := b.gw.ListSubscriptions(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	for i := range subs {
		if subs[i].ID == subID {
			return &subs[i], nil
		}
	}
	return nil, fmt.Errorf("subscription %s not found", subID)
}

func (b *Bot) handleHistory(ctx context.Context, chatID int64, subID string) {
	sub, err := b.findSubscription(ctx, chatID, subID)
	if err != nil {
		b.send(chatID, "Подписка не найдена.")
		return
	}

	points, err := b.gw.GetHistory(ctx, chatID, subID)
	if err != nil {
		slog.Error("get history failed", "sub_id", subID, "error", err)
		b.send(chatID, "Не удалось загрузить историю.")
		return
	}

	if len(points) == 0 {
		b.send(chatID, fmt.Sprintf("📊 %s\n\nИстория цен пока пуста — нажми 🔄 Проверить чтобы получить первые данные.", sub.ProductName))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 История цен: %s\n\n", sub.ProductName))
	for _, p := range points {
		sb.WriteString(fmt.Sprintf("%.0f %s  %s\n",
			p.Price,
			p.Currency,
			p.ScrapedAt.Local().Format("02.01 15:04"),
		))
	}
	b.send(chatID, sb.String())
}

func (b *Bot) handleForceCheck(ctx context.Context, chatID int64, subID string) {
	if err := b.gw.ForceCheck(ctx, chatID, subID); err != nil {
		slog.Error("force check failed", "sub_id", subID, "error", err)
		b.send(chatID, "Не удалось запустить проверку. Попробуй снова.")
		return
	}
	b.send(chatID, "🔄 Проверка запущена.\n\nРезультат придёт в течение минуты.")
}

func (b *Bot) pauseSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if err := b.gw.PauseSubscription(ctx, chatID, subID); err != nil {
		slog.Error("pause subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}

func (b *Bot) resumeSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if err := b.gw.ResumeSubscription(ctx, chatID, subID); err != nil {
		slog.Error("resume subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}

func (b *Bot) deleteSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if err := b.gw.DeleteSubscription(ctx, chatID, subID); err != nil {
		slog.Error("delete subscription failed", "error", err)
		b.send(chatID, "Ошибка удаления.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}
