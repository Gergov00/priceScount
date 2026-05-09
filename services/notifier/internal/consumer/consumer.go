package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/notifier/internal/alert"
	"github.com/Gergov00/pricescount/services/notifier/internal/store"
)

type Consumer struct {
	conn  *broker.Connection
	store *store.Store
}

func New(conn *broker.Connection, st *store.Store) *Consumer {
	return &Consumer{conn: conn, store: st}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueuePriceResults, "notifier-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueuePriceResults, err)
	}
	slog.Info("notifier consumer started", "queue", broker.QueuePriceResults)

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

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var result contracts.PriceResult
	if err := json.Unmarshal(d.Body, &result); err != nil {
		slog.Error("malformed price result, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("task_id", result.TaskID, "url", result.URL, "product_id", result.ProductID)

	if !result.Success {
		log.Info("price result unsuccessful, skipping", "error", result.Error)
		d.Ack(false)
		return
	}

	if err := c.store.SavePrice(ctx, result.ProductID, result.URL, result.Price, result.Currency, result.ScrapedAt); err != nil {
		log.Error("failed to save price, requeuing", "error", err)
		d.Nack(false, true)
		return
	}

	log.Info("price saved", "price", result.Price, "currency", result.Currency)

	subs, err := c.store.TriggeredSubscriptions(ctx, result.ProductID, result.Price)
	if err != nil {
		log.Error("failed to query subscriptions", "error", err)
		// Price is already saved — ack and move on.
		d.Ack(false)
		return
	}

	for _, sub := range subs {
		alert.Fire(alert.Alert{
			Channel:     sub.Channel,
			UserID:      sub.UserID,
			ProductID:   result.ProductID,
			URL:         result.URL,
			Price:       result.Price,
			Currency:    result.Currency,
			TargetPrice: sub.TargetPrice,
		})
	}

	d.Ack(false)
}
