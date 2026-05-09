package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
)

// Scheduler periodically picks URLs due for re-checking and publishes scraper tasks.
type Scheduler struct {
	conn     *broker.Connection
	store    *store.URLStore
	interval time.Duration
}

func New(conn *broker.Connection, st *store.URLStore, interval time.Duration) *Scheduler {
	return &Scheduler{conn: conn, store: st, interval: interval}
}

// Run starts the tick loop. It dispatches immediately on start, then every interval.
// Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	slog.Info("scheduler tick loop started", "interval", s.interval)
	s.dispatch(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.dispatch(ctx)
		}
	}
}

func (s *Scheduler) dispatch(ctx context.Context) {
	due, err := s.store.DueURLs(ctx)
	if err != nil {
		slog.Error("failed to fetch due URLs from Redis", "error", err)
		return
	}
	if len(due) == 0 {
		slog.Info("scheduler tick: no URLs due")
		return
	}

	slog.Info("scheduler tick: dispatching tasks", "count", len(due))
	dispatched := 0

	for _, meta := range due {
		task := contracts.ScraperTask{
			TaskID:      uuid.New().String(),
			ProductID:   meta.ProductID,
			URL:         meta.URL,
			ScheduledAt: time.Now().UTC(),
		}

		if err := s.conn.Publish(ctx, broker.QueueScraperTasks, task); err != nil {
			slog.Error("failed to publish scraper task", "url", meta.URL, "error", err)
			// Don't reschedule — it stays at current score and will be retried next tick.
			continue
		}

		if err := s.store.Reschedule(ctx, meta.URL, s.interval); err != nil {
			slog.Error("failed to reschedule URL", "url", meta.URL, "error", err)
		}
		dispatched++
	}

	slog.Info("scheduler tick complete", "dispatched", dispatched, "total_due", len(due))
}
