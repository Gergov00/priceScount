package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

var lookupSequence atomic.Uint64

type lookupTask struct {
	id     string
	cancel context.CancelFunc
}

func (b *Bot) handleURLSubmit(ctx context.Context, chatID int64, text string) {
	if !strings.Contains(text, "wildberries.ru") {
		b.send(chatID, "Пришли ссылку на товар с Wildberries.\n\n"+
			"Пример:\nhttps://www.wildberries.ru/catalog/12345678/detail.aspx")
		return
	}

	localID := fmt.Sprintf("bot-%d", lookupSequence.Add(1))
	b.cancelLookup(chatID)
	page := b.state.Get(chatID).Page
	b.state.Set(chatID, &state.Session{
		Step:     state.StepWaitingLookup,
		LookupID: localID,
		Page:     page,
	})
	if err := b.request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)); err != nil {
		slog.Warn("send typing action failed", "chat_id", chatID, "error", b.safeError(err))
	}
	b.send(chatID, "Загружаю информацию о товаре...")
	lookupCtx, cancel := context.WithCancel(ctx)
	b.lookupMu.Lock()
	select {
	case b.lookupSlots <- struct{}{}:
		b.activeLookups[chatID] = lookupTask{id: localID, cancel: cancel}
		b.lookupWG.Add(1)
	default:
		b.lookupMu.Unlock()
		cancel()
		b.state.CompleteLookup(chatID, localID, state.Session{Step: state.StepIdle, Page: page})
		b.send(chatID, "Сейчас слишком много активных поисков. Попробуй ещё раз через минуту.")
		return
	}
	b.lookupMu.Unlock()
	go func() {
		defer b.lookupWG.Done()
		defer cancel()
		defer func() {
			<-b.lookupSlots
			b.lookupMu.Lock()
			if active, ok := b.activeLookups[chatID]; ok && active.id == localID {
				delete(b.activeLookups, chatID)
			}
			b.lookupMu.Unlock()
		}()
		lookupID, err := b.gw.StartLookup(lookupCtx, text)
		if err != nil {
			if lookupCtx.Err() == nil && b.state.CompleteLookup(chatID, localID, state.Session{Step: state.StepIdle, Page: page}) {
				slog.Error("start lookup failed", "error", err)
				b.send(chatID, "Не удалось запустить поиск товара. Проверь ссылку и попробуй снова.")
			}
			return
		}
		b.pollLookup(lookupCtx, chatID, localID, lookupID)
	}()
}

func (b *Bot) cancelLookup(chatID int64) {
	b.lookupMu.Lock()
	task, ok := b.activeLookups[chatID]
	if ok {
		delete(b.activeLookups, chatID)
	}
	b.lookupMu.Unlock()
	if ok {
		task.cancel()
	}
}

func (b *Bot) pollLookup(ctx context.Context, chatID int64, localID, lookupID string) {
	pollCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	result, err := b.gw.PollLookup(pollCtx, lookupID)
	if err != nil {
		slog.Error("poll lookup failed", "lookup_id", lookupID, "error", err)
		// only notify if the user is still waiting for this lookup
		if ctx.Err() == nil && b.state.CompleteLookup(chatID, localID, state.Session{Step: state.StepIdle}) {
			b.send(chatID, "Не удалось получить информацию о товаре. Попробуй снова.")
		}
		return
	}

	if result.Status == "failed" {
		if ctx.Err() == nil && b.state.CompleteLookup(chatID, localID, state.Session{Step: state.StepIdle}) {
			b.send(chatID, "Не удалось получить информацию о товаре. Проверь ссылку и попробуй снова.")
		}
		return
	}

	if ctx.Err() != nil {
		return
	}
	if !b.state.CompleteLookup(chatID, localID, state.Session{
		Step:         state.StepWaitingMinPrice,
		LookupID:     lookupID,
		ProductName:  result.Name,
		ProductURL:   result.URL,
		CurrentPrice: result.Price,
	}) {
		return
	}

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
	b.send(chatID, hint+fmt.Sprintf("Теперь укажи максимальную цену — уведомлю если цена вырастет выше.\n\nПример: %0.0f", sess.CurrentPrice*1.1))
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

	b.state.Set(chatID, &state.Session{Step: state.StepIdle, Page: sess.Page})
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
