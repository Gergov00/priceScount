package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/notifier/internal/alert"
)

// Consumer reads notify.tasks and delivers messages by channel.
type Consumer struct {
	conn          *broker.Connection
	telegramToken string
}

func New(conn *broker.Connection, telegramToken string) *Consumer {
	return &Consumer{conn: conn, telegramToken: telegramToken}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueueNotifyTasks, "notifier-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueueNotifyTasks, err)
	}
	slog.Info("notifier consumer started", "queue", broker.QueueNotifyTasks)

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed unexpectedly")
			}
			c.handle(ctx, d)
		}
	}
}

func (c *Consumer) handle(_ context.Context, d amqp.Delivery) {
	var task contracts.NotifyTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed notify task, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	switch task.Channel {
	case "telegram":
		c.sendTelegram(task)
	default:
		slog.Warn("unknown notify channel, dropping", "channel", task.Channel)
	}

	d.Ack(false)
}

func (c *Consumer) sendTelegram(task contracts.NotifyTask) {
	chatID, err := strconv.ParseInt(task.Target, 10, 64)
	if err != nil {
		slog.Error("invalid telegram target", "target", task.Target, "error", err)
		return
	}
	if err := alert.SendWithRetry(c.telegramToken, chatID, task.Text); err != nil {
		slog.Error("telegram send failed", "chat_id", chatID, "error", err)
	}
}
