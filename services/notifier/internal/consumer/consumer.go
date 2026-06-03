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
)

// Sender is the notification interface required by Consumer.
type Sender interface {
	Send(chatID int64, text string) error
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	Consume(queue, consumer string) (<-chan amqp.Delivery, error)
}

// Consumer reads notify.tasks and delivers messages by channel.
type Consumer struct {
	mq     MQ
	sender Sender
}

func New(mq MQ, sender Sender) *Consumer {
	return &Consumer{mq: mq, sender: sender}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.mq.Consume(broker.QueueNotifyTasks, "notifier-consumer")
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
			c.handle(d)
		}
	}
}

func (c *Consumer) handle(d amqp.Delivery) {
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
	if err := c.sender.Send(chatID, task.Text); err != nil {
		slog.Error("telegram send failed", "chat_id", chatID, "error", err)
	}
}
