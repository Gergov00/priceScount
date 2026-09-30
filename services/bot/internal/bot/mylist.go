package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
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
	text, keyboard, actualPage := buildMyListPage(subs, b.state.Get(chatID).Page)
	b.state.Update(chatID, func(sess *state.Session) { sess.Page = actualPage })
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = keyboard
	if _, err := b.api.Send(msg); err != nil {
		slog.Warn("send subscription list failed", "chat_id", chatID, "error", b.safeError(err))
	}
}

func (b *Bot) refreshMyList(ctx context.Context, chatID int64, messageID int) {
	subs, err := b.gw.ListSubscriptions(ctx, chatID)
	if err != nil {
		slog.Error("list subscriptions failed on refresh", "error", err)
		return
	}
	if len(subs) == 0 {
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID,
			"У тебя больше нет отслеживаемых товаров.\n\nПришли ссылку на товар чтобы начать.", tgbotapi.InlineKeyboardMarkup{InlineKeyboard: make([][]tgbotapi.InlineKeyboardButton, 0)})
		if _, err := b.api.Send(edit); err != nil {
			slog.Warn("edit empty subscription list failed", "chat_id", chatID, "error", b.safeError(err))
		}
		b.state.Update(chatID, func(sess *state.Session) { sess.Page = 0 })
		return
	}
	text, keyboard, actualPage := buildMyListPage(subs, b.state.Get(chatID).Page)
	b.state.Update(chatID, func(sess *state.Session) { sess.Page = actualPage })
	editText := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, text, keyboard)
	editText.DisableWebPagePreview = true
	if _, err := b.api.Send(editText); err != nil {
		slog.Warn("edit subscription list failed", "chat_id", chatID, "error", b.safeError(err))
		return
	}
}

func buildMyList(subs []gateway.Subscription) (string, tgbotapi.InlineKeyboardMarkup) {
	text, keyboard, _ := buildMyListPage(subs, 1)
	return text, keyboard
}

const myListPageItems = 10
const telegramTextLimit = 4096

func buildMyListPage(subs []gateway.Subscription, page int) (string, tgbotapi.InlineKeyboardMarkup, int) {
	entries := make([]string, len(subs))
	headerBudget := utf16Units(myListHeader(len(subs), maxInt(1, len(subs))))
	for i, sub := range subs {
		entries[i] = myListEntry(sub, i+1, telegramTextLimit-headerBudget)
	}
	pages := make([][]int, 0, (len(subs)+myListPageItems-1)/myListPageItems)
	current := make([]int, 0, myListPageItems)
	textUnits := utf16Units(myListHeader(len(subs), 1))
	for i, entry := range entries {
		units := utf16Units(entry)
		if len(current) > 0 && (len(current) == myListPageItems || textUnits+units > telegramTextLimit) {
			pages = append(pages, current)
			current = make([]int, 0, myListPageItems)
			textUnits = utf16Units(myListHeader(len(subs), len(pages)+1))
		}
		current = append(current, i)
		textUnits += units
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	if page < 1 {
		page = 1
	}
	if page > len(pages) {
		page = len(pages)
	}
	actualPage := page
	var sb strings.Builder
	sb.WriteString(myListHeader(len(subs), actualPage))
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(pages[actualPage-1])*3+1)
	for _, index := range pages[actualPage-1] {
		s := subs[index]
		sb.WriteString(entries[index])
		name := truncate(s.ProductName, 22)
		pauseBtn := tgbotapi.NewInlineKeyboardButtonData("⏸", "pause_sub:"+s.ID)
		if s.Paused {
			pauseBtn = tgbotapi.NewInlineKeyboardButtonData("▶", "resume_sub:"+s.ID)
		}
		rows = append(rows,
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✏️ "+name, "edit_sub:"+s.ID), pauseBtn),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📊 История", "history_sub:"+s.ID), tgbotapi.NewInlineKeyboardButtonData("🔄 Проверить", "check_sub:"+s.ID)),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить «"+truncate(s.ProductName, 20)+"»", "del_sub:"+s.ID)),
		)
	}
	if actualPage > 1 || actualPage < len(pages) {
		nav := make([]tgbotapi.InlineKeyboardButton, 0, 2)
		if actualPage > 1 {
			nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("← Назад", fmt.Sprintf("page:%d", actualPage-1)))
		}
		if actualPage < len(pages) {
			nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("Дальше →", fmt.Sprintf("page:%d", actualPage+1)))
		}
		rows = append(rows, nav)
	}
	if len(rows) == 0 {
		return sb.String(), tgbotapi.InlineKeyboardMarkup{InlineKeyboard: make([][]tgbotapi.InlineKeyboardButton, 0)}, actualPage
	}
	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(rows...), actualPage
}

func myListHeader(total, page int) string {
	return fmt.Sprintf("📋 Твои товары (%d) — страница %d:\n", total, page)
}

func myListEntry(s gateway.Subscription, index, maxUnits int) string {
	status := ""
	if s.Paused {
		status = " ⏸"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%d. %s%s\n", index, truncateUTF16(s.ProductName, 200), status))
	if s.MinPrice != nil && s.MaxPrice != nil {
		sb.WriteString(fmt.Sprintf("   %.0f — %.0f ₽\n", *s.MinPrice, *s.MaxPrice))
	}
	if s.ProductURL != "" {
		prefix := "   • "
		remaining := maxUnits - utf16Units(sb.String()) - utf16Units(prefix) - 1
		sb.WriteString(prefix + truncateUTF16(s.ProductURL, remaining) + "\n")
	}
	return sb.String()
}

func truncateUTF16(s string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	runes := []rune(s)
	total := 0
	for _, r := range runes {
		if r > 0xffff {
			total += 2
		} else {
			total++
		}
	}
	if total <= maxUnits {
		return s
	}
	limit := maxUnits - 1
	units := 0
	var sb strings.Builder
	for _, r := range runes {
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > limit {
			break
		}
		sb.WriteRune(r)
		units += runeUnits
	}
	return sb.String() + "…"
}

func utf16Units(s string) int { return len(utf16.Encode([]rune(s))) }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
		sb.WriteString(fmt.Sprintf("%.0f %s  %s\n", p.Price, p.Currency, p.ScrapedAt.Local().Format("02.01 15:04")))
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
