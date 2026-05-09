package bot

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"log/slog"

	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func (b *Bot) handleSearch(ctx context.Context, chatID int64, productName string) {
	b.send(chatID, fmt.Sprintf("Ищу %q...", productName))
	b.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))

	result, err := b.discovery.Discover(ctx, productName)
	if err != nil {
		slog.Error("discovery failed", "error", err)
		b.send(chatID, "Не удалось найти товар. Попробуй ещё раз.")
		return
	}
	if len(result.Items) == 0 {
		b.send(chatID, "Ничего не нашёл. Попробуй указать более конкретную модель.")
		return
	}

	urls := make([]state.URLItem, len(result.Items))
	for i, item := range result.Items {
		urls[i] = state.URLItem{URL: item.URL, Source: item.Source, Title: item.Title, Price: item.Price}
	}

	sess := &state.Session{
		Step:        state.StepSelectingURLs,
		ProductID:   result.ProductID,
		ProductName: productName,
		URLs:        urls,
	}
	b.state.Set(ctx, chatID, sess)
	b.sendURLSelection(chatID, sess)
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

	// Remind current prices when asking for max.
	lines := make([]string, 0, len(sess.SelectedIdxs))
	for _, idx := range sess.SelectedIdxs {
		if idx < len(sess.URLs) && sess.URLs[idx].Price != "" {
			lines = append(lines, "• "+sess.URLs[idx].Source+": "+sess.URLs[idx].Price)
		}
	}
	hint := ""
	if len(lines) > 0 {
		hint = "Текущие цены:\n" + strings.Join(lines, "\n") + "\n\n"
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

	// Save selected URLs to tracked_urls so they appear in /mylist immediately.
	for _, idx := range sess.SelectedIdxs {
		if idx < len(sess.URLs) {
			item := sess.URLs[idx]
			if _, err := b.db.Exec(ctx, `
				INSERT INTO tracked_urls(product_id, url, source)
				VALUES($1, $2, $3)
				ON CONFLICT (url) DO NOTHING
			`, sess.ProductID, item.URL, item.Source); err != nil {
				slog.Error("insert tracked_url failed", "url", item.URL, "error", err)
			}
		}
	}

	b.state.Clear(ctx, chatID)

	shops := make([]string, 0, len(sess.SelectedIdxs))
	for _, idx := range sess.SelectedIdxs {
		if idx < len(sess.URLs) {
			shops = append(shops, "• "+sess.URLs[idx].URL)
		}
	}

	b.send(chatID, fmt.Sprintf(
		"Готово! Слежу за %q\n\n%s\n\nДиапазон: %.0f — %.0f ₽\n\nУведомлю если цена выйдет за границы.",
		sess.ProductName, strings.Join(shops, "\n"), sess.MinPrice, price,
	))
}

func (b *Bot) handleToggle(ctx context.Context, chatID int64, cb *tgbotapi.CallbackQuery) {
	sess, err := b.state.Get(ctx, chatID)
	if err != nil || sess.Step != state.StepSelectingURLs {
		return
	}
	idx, err := parseIndex(cb.Data, "toggle:")
	if err != nil || idx < 0 || idx >= len(sess.URLs) {
		return
	}
	found := false
	for i, s := range sess.SelectedIdxs {
		if s == idx {
			sess.SelectedIdxs = append(sess.SelectedIdxs[:i], sess.SelectedIdxs[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		sess.SelectedIdxs = append(sess.SelectedIdxs, idx)
	}
	b.state.Set(ctx, chatID, sess)
	b.editURLSelection(chatID, cb.Message.MessageID, sess)
}

func (b *Bot) handleDone(ctx context.Context, chatID int64, cb *tgbotapi.CallbackQuery) {
	sess, err := b.state.Get(ctx, chatID)
	if err != nil || sess.Step != state.StepSelectingURLs {
		return
	}
	if len(sess.SelectedIdxs) == 0 {
		b.api.Request(tgbotapi.NewCallbackWithAlert(cb.ID, "Выбери хотя бы один магазин"))
		return
	}
	sess.Step = state.StepWaitingMinPrice
	b.state.Set(ctx, chatID, sess)
	b.api.Request(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))

	// Show selected sources with current prices.
	lines := make([]string, 0, len(sess.SelectedIdxs))
	for _, idx := range sess.SelectedIdxs {
		if idx < len(sess.URLs) {
			item := sess.URLs[idx]
			line := "• " + item.URL
			if item.Price != "" {
				line += "  (" + item.Price + ")"
			}
			lines = append(lines, line)
		}
	}
	b.send(chatID, fmt.Sprintf(
		"Выбраны источники:\n%s\n\nУкажи минимальную цену — уведомлю если цена упадёт ниже.\n\nПример: 80000",
		strings.Join(lines, "\n"),
	))
}

func (b *Bot) handleCancelSearch(ctx context.Context, chatID int64, messageID int) {
	b.state.Clear(ctx, chatID)
	b.api.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
	b.send(chatID, "Отменено. Напиши другое название товара.")
}

func (b *Bot) sendURLSelection(chatID int64, sess *state.Session) {
	msg := tgbotapi.NewMessage(chatID, "Выбери магазины для отслеживания:")
	msg.ReplyMarkup = b.buildKeyboard(sess)
	b.api.Send(msg)
}

func (b *Bot) editURLSelection(chatID int64, messageID int, sess *state.Session) {
	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, messageID, b.buildKeyboard(sess))
	b.api.Send(edit)
}

func (b *Bot) buildKeyboard(sess *state.Session) tgbotapi.InlineKeyboardMarkup {
	selected := make(map[int]bool, len(sess.SelectedIdxs))
	for _, idx := range sess.SelectedIdxs {
		selected[idx] = true
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(sess.URLs)+2)
	for i, item := range sess.URLs {
		check := "☐"
		if selected[i] {
			check = "✅"
		}
		label := fmt.Sprintf("%s %s", check, itemLabel(item))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("toggle:%d", i)),
		))
	}
	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Готово", "done"),
			tgbotapi.NewInlineKeyboardButtonData("🚫 Отмена", "cancel_search"),
		),
	)
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

// itemLabel builds a short human-readable label: "Shop — Title — Price"
func itemLabel(item state.URLItem) string {
	source := truncate(item.Source, 15)
	title := truncate(item.Title, 25)
	switch {
	case title != "" && item.Price != "":
		return fmt.Sprintf("%s — %s — %s", source, title, item.Price)
	case title != "":
		return fmt.Sprintf("%s — %s", source, title)
	case item.Price != "":
		return fmt.Sprintf("%s — %s", source, item.Price)
	default:
		return source
	}
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
