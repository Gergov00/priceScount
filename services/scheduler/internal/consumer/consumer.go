package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
)

// Consumer reads DiscoveredURL messages and registers URLs in the Redis pool.
type Consumer struct {
	conn  *broker.Connection
	store *store.URLStore
}

func New(conn *broker.Connection, st *store.URLStore) *Consumer {
	return &Consumer{conn: conn, store: st}
}

// Run blocks, consuming from discovery.urls until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueueDiscoveryURLs, "scheduler-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueueDiscoveryURLs, err)
	}
	slog.Info("consumer started", "queue", broker.QueueDiscoveryURLs)

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
	var msg contracts.DiscoveredURL
	if err := json.Unmarshal(d.Body, &msg); err != nil {
		slog.Error("malformed message, dropping", "error", err, "body", string(d.Body))
		d.Nack(false, false) // don't requeue malformed messages
		return
	}

	if err := c.store.Add(ctx, msg.URL, msg.ProductID); err != nil {
		slog.Error("failed to register URL in pool, requeuing", "url", msg.URL, "error", err)
		d.Nack(false, true) // requeue on transient Redis failure
		return
	}

	slog.Info("URL registered in pool", "url", msg.URL, "product", msg.ProductName, "product_id", msg.ProductID)
	d.Ack(false)
}
