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
	store    *store.Store
	interval time.Duration
}

func New(conn *broker.Connection, st *store.Store, interval time.Duration) *Scheduler {
	return &Scheduler{conn: conn, store: st, interval: interval}
}

// Run starts the tick loop. Dispatches immediately on start, then every interval.
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
	due, err := s.store.DueURLs(ctx, s.interval)
	if err != nil {
		slog.Error("failed to fetch due URLs", "error", err)
		return
	}
	if len(due) == 0 {
		slog.Debug("scheduler tick: no URLs due")
		return
	}

	slog.Info("scheduler tick: dispatching", "count", len(due))
	dispatched := 0

	for _, entry := range due {
		task := contracts.ScraperTask{
			TaskID:      uuid.New().String(),
			ProductID:   entry.ProductID,
			URL:         entry.URL,
			Platform:    entry.Platform,
			ScheduledAt: time.Now().UTC(),
		}
		if err := s.conn.Publish(ctx, broker.QueueScraperTasks, task); err != nil {
			slog.Error("publish scraper task failed", "url", entry.URL, "error", err)
			continue
		}
		dispatched++
	}

	slog.Info("scheduler tick complete", "dispatched", dispatched, "total_due", len(due))
}
