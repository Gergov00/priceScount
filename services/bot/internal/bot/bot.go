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

type Bot struct {
	api   *tgbotapi.BotAPI
	gw    *gateway.Client
	state *state.Store
}

func New(token string, st *state.Store, gw *gateway.Client) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("bot api: %w", err)
	}
	b := &Bot{api: api, gw: gw, state: st}
	b.registerCommands()
	return b, nil
}

func (b *Bot) registerCommands() {
	commands := []tgbotapi.BotCommand{
		{Command: "mylist", Description: "Мои отслеживаемые товары"},
		{Command: "cancel", Description: "Отменить текущее действие"},
	}
	b.api.Request(tgbotapi.NewSetMyCommands(commands...))
}

func (b *Bot) Run(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)
	slog.Info("telegram bot started", "username", b.api.Self.UserName)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return nil
		case update := <-updates:
			if update.Message != nil {
				b.handleMessage(ctx, update.Message)
			} else if update.CallbackQuery != nil {
				b.handleCallback(ctx, update.CallbackQuery)
			}
		}
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	if msg.Text == "/start" || msg.Text == "/cancel" {
		b.state.Clear(chatID)
		reply := tgbotapi.NewMessage(chatID,
			"Привет! Пришли ссылку на товар с Wildberries — я начну следить за ценой.\n\n"+
				"Пример:\nhttps://www.wildberries.ru/catalog/12345678/detail.aspx")
		reply.ReplyMarkup = mainKeyboard()
		b.api.Send(reply)
		return
	}

	if msg.Text == "/mylist" || msg.Text == "📋 Мои товары" {
		b.state.Clear(chatID)
		b.handleMyList(ctx, chatID)
		return
	}

	sess := b.state.Get(chatID)

	switch sess.Step {
	case state.StepIdle:
		b.handleURLSubmit(ctx, chatID, msg.Text)
	case state.StepWaitingLookup:
		b.send(chatID, "Подожди, загружаю товар...")
	case state.StepWaitingMinPrice:
		b.handleMinPrice(ctx, chatID, sess, msg.Text)
	case state.StepWaitingMaxPrice:
		b.handleMaxPrice(ctx, chatID, sess, msg.Text)
	case state.StepEditingMinPrice:
		b.handleEditMinPrice(ctx, chatID, sess, msg.Text)
	case state.StepEditingMaxPrice:
		b.handleEditMaxPrice(ctx, chatID, sess, msg.Text)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	chatID := cb.Message.Chat.ID
	b.api.Request(tgbotapi.NewCallback(cb.ID, ""))

	switch {
	case strings.HasPrefix(cb.Data, "edit_sub:"):
		subID := strings.TrimPrefix(cb.Data, "edit_sub:")
		b.api.Request(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		b.startEditSubscription(ctx, chatID, subID)
	case strings.HasPrefix(cb.Data, "pause_sub:"):
		subID := strings.TrimPrefix(cb.Data, "pause_sub:")
		b.pauseSubscription(ctx, chatID, cb.Message.MessageID, subID)
	case strings.HasPrefix(cb.Data, "resume_sub:"):
		subID := strings.TrimPrefix(cb.Data, "resume_sub:")
		b.resumeSubscription(ctx, chatID, cb.Message.MessageID, subID)
	case strings.HasPrefix(cb.Data, "del_sub:"):
		subID := strings.TrimPrefix(cb.Data, "del_sub:")
		b.deleteSubscription(ctx, chatID, cb.Message.MessageID, subID)
	case strings.HasPrefix(cb.Data, "history_sub:"):
		subID := strings.TrimPrefix(cb.Data, "history_sub:")
		b.handleHistory(ctx, chatID, subID)
	case strings.HasPrefix(cb.Data, "check_sub:"):
		subID := strings.TrimPrefix(cb.Data, "check_sub:")
		b.handleForceCheck(ctx, chatID, subID)
	}
}

func (b *Bot) send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	for _, d := range delays {
		if d > 0 {
			time.Sleep(d)
		}
		if _, err := b.api.Send(msg); err == nil {
			return
		} else if d == delays[len(delays)-1] {
			slog.Error("send message failed", "chat_id", chatID, "error", err)
		} else {
			slog.Warn("send message attempt failed, retrying", "chat_id", chatID, "error", err)
		}
	}
}

func mainKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📋 Мои товары"),
		),
	)
}
