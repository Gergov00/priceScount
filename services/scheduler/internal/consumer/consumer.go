package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

// Store is the persistence interface required by Consumer.
type Store interface {
	Add(ctx context.Context, productID, url, platform string, intervalHours int) error
	SetActive(ctx context.Context, url string, active bool) error
	SetNextCheck(ctx context.Context, url string) error
	Delete(ctx context.Context, url string) error
	AdvanceNextCheck(ctx context.Context, url string) error
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	Consume(queue, consumer string) (<-chan amqp.Delivery, error)
	Publish(ctx context.Context, queue string, v any) error
}

// Consumer reads track.requests and updates the scheduled_urls table accordingly.
type Consumer struct {
	mq    MQ
	store Store
}

func New(mq MQ, st Store) *Consumer {
	return &Consumer{mq: mq, store: st}
}

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
				return fmt.Errorf("delivery channel closed unexpectedly")
			}
			c.handle(ctx, d)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var req contracts.TrackRequest
	if err := json.Unmarshal(d.Body, &req); err != nil {
		slog.Error("malformed track request, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("action", req.Action, "url", req.URL, "product_id", req.ProductID)

	var err error
	switch req.Action {
	case "add":
		intervalHours := req.IntervalHours
		if intervalHours <= 0 {
			intervalHours = 1
		}
		err = c.store.Add(ctx, req.ProductID, req.URL, req.Platform, intervalHours)
	case "pause":
		err = c.store.SetActive(ctx, req.URL, false)
	case "resume":
		err = c.store.SetNextCheck(ctx, req.URL)
	case "delete":
		err = c.store.Delete(ctx, req.URL)
	case "force":
		err = c.handleForce(ctx, req)
	default:
		log.Error("unknown action, dropping")
		d.Nack(false, false)
		return
	}

	if err != nil {
		log.Error("store operation failed, requeuing", "error", err)
		d.Nack(false, true)
		return
	}

	log.Info("track request processed")
	d.Ack(false)
}

// handleForce publishes a ScraperTask immediately (bypassing the tick) and advances
// next_check_at so the tick does not redispatch within the same interval.
func (c *Consumer) handleForce(ctx context.Context, req contracts.TrackRequest) error {
	task := contracts.ScraperTask{
		TaskID:      uuid.New().String(),
		ProductID:   req.ProductID,
		URL:         req.URL,
		Platform:    req.Platform,
		ScheduledAt: time.Now().UTC(),
		Force:       true,
		ChatID:      req.ChatID,
	}
	if err := c.mq.Publish(ctx, broker.QueueScraperTasks, task); err != nil {
		return fmt.Errorf("publish force scraper task: %w", err)
	}
	return c.store.AdvanceNextCheck(ctx, req.URL)
}
