package cleaner

import (
	"context"
	"log/slog"
	"time"
)

// Store is the persistence interface required by the TTL cleaner.
type Store interface {
	DeleteExpiredLookups(ctx context.Context) (int64, error)
	DeleteOldPriceHistory(ctx context.Context, olderThan time.Duration) (int64, error)
}

// Run deletes expired lookup_requests and price_history rows older than
// retention, every interval, until ctx is cancelled.
func Run(ctx context.Context, st Store, interval, retention time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := st.DeleteExpiredLookups(ctx); err != nil {
				slog.Error("cleaner: delete expired lookups", "error", err)
			} else if n > 0 {
				slog.Info("cleaner: deleted expired lookups", "count", n)
			}

			if n, err := st.DeleteOldPriceHistory(ctx, retention); err != nil {
				slog.Error("cleaner: delete old price history", "error", err)
			} else if n > 0 {
				slog.Info("cleaner: deleted old price points", "count", n)
			}
		}
	}
}
