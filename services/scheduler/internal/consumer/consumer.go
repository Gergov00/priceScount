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
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
)

// Consumer reads track.requests and updates the scheduled_urls table accordingly.
type Consumer struct {
	conn  *broker.Connection
	store *store.Store
}

func New(conn *broker.Connection, st *store.Store) *Consumer {
	return &Consumer{conn: conn, store: st}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueueTrackRequests, "scheduler-consumer")
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
	}
	if err := c.conn.Publish(ctx, broker.QueueScraperTasks, task); err != nil {
		return fmt.Errorf("publish force scraper task: %w", err)
	}
	return c.store.AdvanceNextCheck(ctx, req.URL)
}
