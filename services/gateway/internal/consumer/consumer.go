package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

// Store is the persistence interface required by Consumer.
// Defined here, in the consumer, per Go convention.
type Store interface {
	ProcessPriceResult(ctx context.Context, result contracts.PriceResult) error
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	Consume(queue, consumer string) (<-chan amqp.Delivery, error)
}

// Consumer reads price.results and either:
//   - completes a pending lookup_request (if LookupID is set), or
//   - saves price history and fires threshold alerts (if ProductID is set).
type Consumer struct {
	mq    MQ
	store Store
}

func New(mq MQ, st Store) *Consumer {
	return &Consumer{mq: mq, store: st}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.mq.Consume(broker.QueuePriceResults, "gateway-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueuePriceResults, err)
	}
	slog.Info("gateway price.results consumer started")

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

	log := slog.With("task_id", result.TaskID, "url", result.URL)

	if err := c.store.ProcessPriceResult(ctx, result); err != nil {
		if errors.Is(err, store.ErrInvalidResult) {
			log.Warn("invalid price result, dropping", "error", err)
			d.Nack(false, false)
			return
		}
		log.Error("process price result failed, requeuing", "error", err)
		d.Nack(false, true)
		return
	}
	d.Ack(false)
}
