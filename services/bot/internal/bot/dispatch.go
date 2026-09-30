package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const dispatcherWorkers = 8

type dispatcher struct {
	bot    *Bot
	handle func(context.Context, tgbotapi.Update)
}

// Run serializes updates for each chat on one of eight bounded FIFO queues.
func (d *dispatcher) Run(ctx context.Context, updates <-chan tgbotapi.Update) error {
	queues := make([]chan tgbotapi.Update, dispatcherWorkers)
	var workers sync.WaitGroup
	for i := range queues {
		queues[i] = make(chan tgbotapi.Update, 32)
		workers.Add(1)
		go func(queue <-chan tgbotapi.Update) {
			defer workers.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				update, ok := <-queue
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				if d.handle != nil {
					d.handle(ctx, update)
				} else if update.Message != nil {
					d.bot.handleMessage(ctx, update.Message)
				} else if update.CallbackQuery != nil {
					d.bot.handleCallback(ctx, update.CallbackQuery)
				}
			}
		}(queues[i])
	}

	defer func() {
		for _, queue := range queues {
			close(queue)
		}
		workers.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			chatID, valid := updateChatID(update)
			if !valid {
				continue
			}
			h := fnv.New32a()
			_, _ = h.Write([]byte(strconv.FormatInt(chatID, 10)))
			queue := queues[int(h.Sum32()%dispatcherWorkers)]
			select {
			case queue <- update:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func updateChatID(update tgbotapi.Update) (int64, bool) {
	if update.Message != nil && update.Message.Chat != nil {
		return update.Message.Chat.ID, true
	}
	if update.CallbackQuery != nil && update.CallbackQuery.Message != nil && update.CallbackQuery.Message.Chat != nil {
		return update.CallbackQuery.Message.Chat.ID, true
	}
	return 0, false
}

type contextHTTPClient struct {
	mu     sync.RWMutex
	client tgbotapi.HTTPClient
	ctx    context.Context
}

func (c *contextHTTPClient) SetContext(ctx context.Context) { c.mu.Lock(); c.ctx = ctx; c.mu.Unlock() }
func (c *contextHTTPClient) Context() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}
func (c *contextHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.RLock()
	client, ctx := c.client, c.ctx
	c.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("telegram HTTP client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	baseCtx, cancelBase := context.WithCancel(ctx)
	stop := context.AfterFunc(req.Context(), cancelBase)
	deadline := 10 * time.Second
	if strings.Contains(req.URL.Path, "/getUpdates") {
		deadline = 35 * time.Second
	}
	requestCtx, timeoutCancel := context.WithTimeout(baseCtx, deadline)
	resp, err := client.Do(req.WithContext(requestCtx))
	if err != nil {
		timeoutCancel()
		stop()
		cancelBase()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cleanup: func() { timeoutCancel(); stop(); cancelBase() }}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cleanup)
	return err
}
