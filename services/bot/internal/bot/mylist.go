package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

type subscription struct {
	ID        string
	ProductID string
	Name      string
	MinPrice  *float64
	MaxPrice  *float64
	Paused    bool
	URLs      []string
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

	text, keyboard := b.buildMyList(subs)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = keyboard
	b.api.Send(msg)
}

// refreshMyList rewrites the existing mylist message in-place.
func (b *Bot) refreshMyList(ctx context.Context, chatID int64, messageID int) {
	subs, err := b.userSubscriptions(ctx, chatID)
	if err != nil {
		slog.Error("userSubscriptions failed on refresh", "error", err)
		return
	}
	if len(subs) == 0 {
		edit := tgbotapi.NewEditMessageText(chatID, messageID, "У тебя больше нет отслеживаемых товаров.\n\nНапиши название товара чтобы начать.")
		b.api.Send(edit)
		return
	}

	text, keyboard := b.buildMyList(subs)
	editText := tgbotapi.NewEditMessageText(chatID, messageID, text)
	editText.DisableWebPagePreview = true
	b.api.Send(editText)
	b.api.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, messageID, keyboard))
}

func (b *Bot) buildMyList(subs []subscription) (string, tgbotapi.InlineKeyboardMarkup) {
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
		for _, u := range s.URLs {
			sb.WriteString(fmt.Sprintf("   • %s\n", u))
		}
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(subs)*3)
	for _, s := range subs {
		name := truncate(s.Name, 22)
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
				tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить «"+truncate(s.Name, 20)+"»", "del_sub:"+s.ID),
			),
		)
	}

	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (b *Bot) userSubscriptions(ctx context.Context, chatID int64) ([]subscription, error) {
	rows, err := b.db.Query(ctx, `
		SELECT s.id, s.product_id, p.name, s.min_price, s.max_price, s.paused
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
		if err := rows.Scan(&s.ID, &s.ProductID, &s.Name, &s.MinPrice, &s.MaxPrice, &s.Paused); err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range subs {
		subs[i].URLs = b.productURLs(ctx, subs[i].ProductID)
	}
	return subs, nil
}

func (b *Bot) productURLs(ctx context.Context, productID string) []string {
	rows, err := b.db.Query(ctx,
		`SELECT url FROM tracked_urls WHERE product_id = $1 AND active = true ORDER BY created_at`,
		productID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		rows.Scan(&u)
		urls = append(urls, u)
	}
	return urls
}

func (b *Bot) handleHistory(ctx context.Context, chatID int64, subID string) {
	var productID, productName string
	err := b.db.QueryRow(ctx, `
		SELECT s.product_id, p.name FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&productID, &productName)
	if err != nil {
		b.send(chatID, "Подписка не найдена.")
		return
	}

	type priceRow struct {
		Price     float64
		Currency  string
		ScrapedAt time.Time
		Source    string
	}

	dbRows, err := b.db.Query(ctx, `
		SELECT ph.price, ph.currency, ph.scraped_at, tu.source
		FROM price_history ph
		JOIN tracked_urls tu ON tu.id = ph.url_id
		WHERE tu.product_id = $1
		ORDER BY ph.scraped_at DESC
		LIMIT 15
	`, productID)
	if err != nil {
		slog.Error("price history query failed", "error", err)
		b.send(chatID, "Не удалось загрузить историю.")
		return
	}
	defer dbRows.Close()

	var records []priceRow
	for dbRows.Next() {
		var r priceRow
		if err := dbRows.Scan(&r.Price, &r.Currency, &r.ScrapedAt, &r.Source); err != nil {
			continue
		}
		records = append(records, r)
	}

	if len(records) == 0 {
		b.send(chatID, fmt.Sprintf("📊 %s\n\nИстория цен пока пуста — нажми 🔄 Проверить чтобы получить первые данные.", productName))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 История цен: %s\n\n", productName))
	for _, r := range records {
		sb.WriteString(fmt.Sprintf("%-12s  %.0f %s  %s\n",
			r.Source,
			r.Price,
			r.Currency,
			r.ScrapedAt.Local().Format("02.01 15:04"),
		))
	}
	b.send(chatID, sb.String())
}

func (b *Bot) handleForceCheck(ctx context.Context, chatID int64, subID string) {
	var productID, productName string
	err := b.db.QueryRow(ctx, `
		SELECT s.product_id, p.name FROM subscriptions s
		JOIN products p ON p.id = s.product_id
		WHERE s.id = $1 AND s.chat_id = $2
	`, subID, chatID).Scan(&productID, &productName)
	if err != nil {
		b.send(chatID, "Подписка не найдена.")
		return
	}

	urls := b.productURLs(ctx, productID)
	if len(urls) == 0 {
		b.send(chatID, "Нет отслеживаемых ссылок для этого товара.")
		return
	}

	published := 0
	for _, u := range urls {
		task := contracts.ScraperTask{
			TaskID:      uuid.New().String(),
			ProductID:   productID,
			URL:         u,
			ScheduledAt: time.Now().UTC(),
			Force:       true,
		}
		if err := b.broker.Publish(ctx, broker.QueueScraperTasks, task); err != nil {
			slog.Error("force check publish failed", "url", u, "error", err)
			continue
		}
		published++
	}

	b.send(chatID, fmt.Sprintf(
		"🔄 Проверка запущена для %q\n\nОтправлено задач: %d из %d.\nРезультат придёт в течение минуты.",
		productName, published, len(urls),
	))
}

func (b *Bot) pauseSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET paused = true WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("pause subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}

func (b *Bot) resumeSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET paused = false WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("resume subscription failed", "error", err)
		b.send(chatID, "Ошибка. Попробуй снова.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}

func (b *Bot) deleteSubscription(ctx context.Context, chatID int64, messageID int, subID string) {
	if _, err := b.db.Exec(ctx,
		`UPDATE subscriptions SET active = false WHERE id = $1 AND chat_id = $2`,
		subID, chatID,
	); err != nil {
		slog.Error("delete subscription failed", "error", err)
		b.send(chatID, "Ошибка удаления.")
		return
	}
	b.refreshMyList(ctx, chatID, messageID)
}
