package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

// Gateway is the minimal API client interface required by Bot.
// Defined here, in the consumer, per Go convention.
type Gateway interface {
	StartLookup(ctx context.Context, rawURL string) (string, error)
	PollLookup(ctx context.Context, lookupID string) (*gateway.LookupResult, error)
	CreateSubscription(ctx context.Context, req gateway.CreateSubscriptionRequest) (*gateway.CreateSubscriptionResponse, error)
	ListSubscriptions(ctx context.Context, chatID int64) ([]gateway.Subscription, error)
	PauseSubscription(ctx context.Context, chatID int64, subID string) error
	ResumeSubscription(ctx context.Context, chatID int64, subID string) error
	EditSubscription(ctx context.Context, chatID int64, subID string, minPrice, maxPrice float64) error
	DeleteSubscription(ctx context.Context, chatID int64, subID string) error
	ForceCheck(ctx context.Context, chatID int64, subID string) error
	GetHistory(ctx context.Context, chatID int64, subID string) ([]gateway.PricePoint, error)
}

// SessionStore is the session storage interface required by Bot.
type SessionStore interface {
	Get(chatID int64) *state.Session
	Set(chatID int64, sess *state.Session)
	Clear(chatID int64)
	Update(chatID int64, fn func(*state.Session))
	CompleteLookup(chatID int64, lookupID string, result state.Session) bool
}

type Bot struct {
	api           *tgbotapi.BotAPI
	gw            Gateway
	state         SessionStore
	lookupWG      sync.WaitGroup
	lookupMu      sync.Mutex
	activeLookups map[int64]lookupTask
	lookupSlots   chan struct{}
	requestClient *contextHTTPClient
	token         string
}

type Option func(*options)
type options struct {
	httpClient  tgbotapi.HTTPClient
	apiEndpoint string
}

func WithHTTPClient(client tgbotapi.HTTPClient) Option {
	return func(o *options) { o.httpClient = client }
}
func WithAPIEndpoint(endpoint string) Option { return func(o *options) { o.apiEndpoint = endpoint } }

func New(token string, st SessionStore, gw Gateway, opts ...Option) (*Bot, error) {
	settings := options{httpClient: &http.Client{Timeout: 35 * time.Second}, apiEndpoint: tgbotapi.APIEndpoint}
	for _, opt := range opts {
		opt(&settings)
	}
	if settings.httpClient == nil {
		settings.httpClient = &http.Client{Timeout: 35 * time.Second}
	}
	ctxClient := &contextHTTPClient{client: settings.httpClient}
	api, err := tgbotapi.NewBotAPIWithClient(token, settings.apiEndpoint, ctxClient)
	if err != nil {
		return nil, fmt.Errorf("bot api initialization failed: %s", redactToken(err.Error(), token))
	}
	b := &Bot{api: api, gw: gw, state: st, token: token, requestClient: ctxClient, activeLookups: make(map[int64]lookupTask), lookupSlots: make(chan struct{}, 8)}
	b.registerCommands()
	return b, nil
}

func (b *Bot) registerCommands() {
	commands := []tgbotapi.BotCommand{
		{Command: "mylist", Description: "Мои отслеживаемые товары"},
		{Command: "cancel", Description: "Отменить текущее действие"},
	}
	if _, err := b.api.Request(tgbotapi.NewSetMyCommands(commands...)); err != nil {
		slog.Warn("register bot commands failed", "error", b.safeError(err))
	}
}

func (b *Bot) Run(ctx context.Context) error {
	b.requestClient.SetContext(ctx)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := make(chan tgbotapi.Update, 64)
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		defer close(updates)
		for ctx.Err() == nil {
			batch, err := b.api.GetUpdates(u)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("poll telegram updates failed", "error", b.safeError(err))
				if !waitContext(ctx, 3*time.Second) {
					return
				}
				continue
			}
			for _, update := range batch {
				if update.UpdateID >= u.Offset {
					u.Offset = update.UpdateID + 1
				}
				select {
				case updates <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	slog.Info("telegram bot started", "username", b.api.Self.UserName)
	err := (&dispatcher{bot: b}).Run(ctx, updates)
	pollWG.Wait()
	b.lookupWG.Wait()
	return err
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	if msg.Text == "/start" || msg.Text == "/cancel" {
		b.cancelLookup(chatID)
		b.state.Clear(chatID)
		reply := tgbotapi.NewMessage(chatID,
			"Привет! Пришли ссылку на товар с Wildberries — я начну следить за ценой.\n\n"+
				"Пример:\nhttps://www.wildberries.ru/catalog/12345678/detail.aspx")
		reply.ReplyMarkup = mainKeyboard()
		if _, err := b.api.Send(reply); err != nil {
			slog.Warn("send welcome failed", "chat_id", chatID, "error", b.safeError(err))
		}
		return
	}

	if msg.Text == "/mylist" || msg.Text == "📋 Мои товары" {
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
	if _, err := b.api.Request(tgbotapi.NewCallback(cb.ID, "")); err != nil {
		slog.Warn("answer callback failed", "error", b.safeError(err))
	}
	if cb.Message == nil || cb.Message.Chat == nil {
		return // callback from an inline/expired message — nothing to act on
	}
	chatID := cb.Message.Chat.ID

	switch {
	case strings.HasPrefix(cb.Data, "page:"):
		page, err := strconv.Atoi(strings.TrimPrefix(cb.Data, "page:"))
		if err == nil && page >= 1 {
			b.state.Update(chatID, func(sess *state.Session) { sess.Page = page })
			b.refreshMyList(ctx, chatID, cb.Message.MessageID)
		}
	case strings.HasPrefix(cb.Data, "edit_sub:"):
		subID := strings.TrimPrefix(cb.Data, "edit_sub:")
		if err := b.request(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID)); err != nil {
			slog.Warn("delete old message failed", "chat_id", chatID, "error", err)
		}
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
	for i, d := range delays {
		if d > 0 {
			if !waitContext(b.requestClient.Context(), d) {
				return
			}
		}
		if _, err := b.api.Send(msg); err == nil {
			return
		} else if d == delays[len(delays)-1] {
			slog.Error("send message failed", "chat_id", chatID, "error", b.safeError(err))
		} else {
			slog.Warn("send message attempt failed, retrying", "chat_id", chatID, "error", b.safeError(err))
		}
		if i == len(delays)-1 {
			return
		}
	}
}

func (b *Bot) safeError(err error) string {
	if err == nil {
		return ""
	}
	return redactToken(err.Error(), b.token)
}

func redactToken(message, token string) string {
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "[redacted]")
}

func (b *Bot) request(config tgbotapi.Chattable) error {
	_, err := b.api.Request(config)
	if err == nil {
		return nil
	}
	return errors.New(b.safeError(err))
}

func waitContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func mainKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📋 Мои товары"),
		),
	)
}
