package scheduler

import (
	"context"
	"log/slog"
	"time"
)

type Store interface {
	EnqueueDue(context.Context) (int, error)
}
type Scheduler struct {
	store    Store
	interval time.Duration
}

func New(st Store, interval time.Duration) *Scheduler {
	return &Scheduler{store: st, interval: interval}
}

// Run periodically records due scraper work in the durable outbox.
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
	count, err := s.store.EnqueueDue(ctx)
	if err != nil {
		slog.Error("failed to enqueue due URLs", "error", err)
		return
	}
	if count == 0 {
		slog.Debug("scheduler tick: no URLs due")
		return
	}
	slog.Info("scheduler tick: queued scraper tasks", "count", count)
}
