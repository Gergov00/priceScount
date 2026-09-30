package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/services/notifier/internal/alert"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

// Sender is the notification interface required by Consumer.
type Sender interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	Consume(queue, consumer string) (<-chan amqp.Delivery, error)
	Publish(ctx context.Context, queue string, v any) error
}

// Consumer reads notify.tasks and delivers messages by channel.
type Consumer struct {
	mq            MQ
	sender        Sender
	transferPause time.Duration
}

func New(mq MQ, sender Sender, transferPause ...time.Duration) *Consumer {
	pause := 250 * time.Millisecond
	if len(transferPause) > 0 {
		pause = transferPause[0]
	}
	return &Consumer{mq: mq, sender: sender, transferPause: pause}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.mq.Consume(broker.QueueNotifyTasks, "notifier-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueueNotifyTasks, err)
	}
	slog.Info("notifier consumer started", "queue", broker.QueueNotifyTasks)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		select {
		case <-workerCtx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				cancel()
				return fmt.Errorf("delivery channel closed unexpectedly")
			}
			c.handle(workerCtx, d)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var task contracts.NotifyTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		c.transfer(ctx, d, broker.QueueNotifyDead, malformedEnvelope{Body: string(d.Body), Error: "malformed notification JSON"})
		return
	}

	if !task.NotBefore.IsZero() && time.Now().Before(task.NotBefore) {
		c.transfer(ctx, d, broker.QueueNotifyRetry, task)
		return
	}
	var deliveryErr error
	switch task.Channel {
	case "telegram":
		deliveryErr = c.sendTelegram(ctx, task)
	default:
		deliveryErr = fmt.Errorf("unknown notification channel %q: %w", task.Channel, alert.ErrPermanent)
	}
	if deliveryErr == nil {
		if err := ctx.Err(); err == nil {
			if err := d.Ack(false); err != nil {
				slog.Error("ack delivered notification", "error", err)
			}
		}
		return
	}
	if errors.Is(deliveryErr, alert.ErrPermanent) {
		c.transfer(ctx, d, broker.QueueNotifyDead, deadEnvelope{Task: task, Error: safeError(deliveryErr)})
		return
	}
	var retryErr *alert.RetryError
	if errors.As(deliveryErr, &retryErr) && retryErr.After > broker.NotifyRetryDelay {
		task.NotBefore = time.Now().Add(retryErr.After)
		c.transfer(ctx, d, broker.QueueNotifyRetry, task)
		return
	}
	c.transfer(ctx, d, broker.QueueNotifyRetry, task)
}

type malformedEnvelope struct {
	Body  string `json:"body"`
	Error string `json:"error"`
}

type deadEnvelope struct {
	Task  contracts.NotifyTask `json:"task"`
	Error string               `json:"error"`
}

func (c *Consumer) sendTelegram(ctx context.Context, task contracts.NotifyTask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	chatID, err := strconv.ParseInt(task.Target, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid telegram target: %w", alert.ErrPermanent)
	}
	return c.sender.Send(ctx, chatID, task.Text)
}

func (c *Consumer) transfer(ctx context.Context, d amqp.Delivery, queue string, message any) {
	if err := ctx.Err(); err != nil {
		return
	}
	if err := c.mq.Publish(ctx, queue, message); err != nil {
		slog.Error("notification transfer failed", "queue", queue, "error", err)
		if wait(ctx, c.transferPause) == nil {
			if ctx.Err() == nil {
				if err := d.Nack(false, true); err != nil {
					slog.Error("nack notification after transfer failure", "error", err)
				}
			}
		}
		return
	}
	if ctx.Err() == nil {
		if err := d.Ack(false); err != nil {
			slog.Error("ack transferred notification", "error", err)
		}
	}
}

func safeError(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

func wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
