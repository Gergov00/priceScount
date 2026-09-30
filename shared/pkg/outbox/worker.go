// Package outbox delivers durable events stored by a consumer-owned repository.
package outbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"
)

const (
	batchSize      = 20
	leaseDuration  = 60 * time.Second
	publishTimeout = 10 * time.Second
	minBackoff     = time.Second
	maxBackoff     = 60 * time.Second
)

type Event struct {
	ID         string
	LeaseToken string
	Queue      string
	Payload    json.RawMessage
	Attempts   int
}

type Store interface {
	Claim(ctx context.Context, limit int, lease time.Duration) ([]Event, error)
	MarkPublished(ctx context.Context, id, leaseToken string) error
	Retry(ctx context.Context, id, leaseToken, lastError string, delay time.Duration) error
}

type Publisher interface {
	Publish(ctx context.Context, queue string, v any) error
}

type Worker struct {
	store     Store
	publisher Publisher
}

func New(st Store, publisher Publisher) *Worker { return &Worker{store: st, publisher: publisher} }

func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		events, err := w.store.Claim(ctx, batchSize, leaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := wait(ctx, minBackoff); err != nil {
				return err
			}
			continue
		}
		if len(events) == 0 {
			if err := wait(ctx, minBackoff); err != nil {
				return err
			}
			continue
		}
		for _, event := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !json.Valid(event.Payload) {
				if retryErr := w.store.Retry(ctx, event.ID, event.LeaseToken, "invalid JSON payload", backoff(event.Attempts)); retryErr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					slog.Error("outbox retry failed", "event_id", event.ID, "error", retryErr)
				}
				continue
			}
			publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
			err := w.publisher.Publish(publishCtx, event.Queue, event.Payload)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if retryErr := w.store.Retry(ctx, event.ID, event.LeaseToken, err.Error(), backoff(event.Attempts)); retryErr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					slog.Error("outbox retry failed", "event_id", event.ID, "error", retryErr)
				}
				continue
			}
			if err := w.store.MarkPublished(ctx, event.ID, event.LeaseToken); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				slog.Error("outbox mark published failed", "event_id", event.ID, "error", err)
			}
		}
	}
}

func backoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	n := math.Min(float64(attempts), 6)
	d := minBackoff * time.Duration(1<<uint(n))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
