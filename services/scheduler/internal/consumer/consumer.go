package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Store interface {
	ApplySnapshot(context.Context, contracts.TrackRequest) error
	EnqueueForce(context.Context, contracts.TrackRequest) error
}
type MQ interface {
	Consume(string, string) (<-chan amqp.Delivery, error)
}

type Consumer struct {
	mq    MQ
	store Store
}

func New(mq MQ, st Store) *Consumer { return &Consumer{mq: mq, store: st} }

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.mq.Consume(broker.QueueTrackRequests, "scheduler-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueueTrackRequests, err)
	}
	slog.Info("scheduler consumer started", "queue", broker.QueueTrackRequests)
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("track request channel closed")
			}
			c.handle(ctx, d)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var req contracts.TrackRequest
	if err := json.Unmarshal(d.Body, &req); err != nil {
		slog.Error("malformed track request, dropping", "error", err)
		_ = d.Nack(false, false)
		return
	}
	var err error
	switch req.Action {
	case "set_state":
		err = c.store.ApplySnapshot(ctx, req)
	case "force":
		err = c.store.EnqueueForce(ctx, req)
	default:
		err = fmt.Errorf("unknown action %q: %w", req.Action, store.ErrInvalidRequest)
	}
	if err != nil {
		if errors.Is(err, store.ErrInvalidRequest) {
			slog.Warn("invalid track request, dropping", "action", req.Action, "error", err)
			_ = d.Nack(false, false)
			return
		}
		slog.Error("track request persistence failed, requeuing", "action", req.Action, "error", err)
		_ = d.Nack(false, true)
		return
	}
	if err := d.Ack(false); err != nil {
		slog.Error("ack track request failed", "action", req.Action, "error", err)
	}
}
