package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/bot/internal/discovery"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

type Bot struct {
	api       *tgbotapi.BotAPI
	discovery *discovery.Client
	state     *state.Store
	db        *pgxpool.Pool
	broker    *broker.Connection
}

func New(token string, dc *discovery.Client, st *state.Store, db *pgxpool.Pool, mq *broker.Connection) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("bot api: %w", err)
	}
	return &Bot{api: api, discovery: dc, state: st, db: db, broker: mq}, nil
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
		b.state.Clear(ctx, chatID)
		b.send(chatID, "Привет! Напиши название товара, который хочешь отслеживать.\n\nПример: iPhone 15 Pro\n\n/mylist — мои товары")
		return
	}

	if msg.Text == "/mylist" || msg.Text == "/pause" {
		b.state.Clear(ctx, chatID)
		b.handleMyList(ctx, chatID)
		return
	}

	sess, err := b.state.Get(ctx, chatID)
	if err != nil {
		b.send(chatID, "Внутренняя ошибка, попробуй снова.")
		return
	}

	switch sess.Step {
	case state.StepIdle:
		b.handleSearch(ctx, chatID, msg.Text)
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
	case strings.HasPrefix(cb.Data, "toggle:"):
		b.handleToggle(ctx, chatID, cb)
	case cb.Data == "done":
		b.handleDone(ctx, chatID, cb)
	case cb.Data == "cancel_search":
		b.handleCancelSearch(ctx, chatID, cb.Message.MessageID)
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
	if _, err := b.api.Send(msg); err != nil {
		slog.Error("send message failed", "chat_id", chatID, "error", err)
	}
}
