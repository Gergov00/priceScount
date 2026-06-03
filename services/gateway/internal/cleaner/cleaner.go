package cleaner

import (
	"context"
	"log/slog"
	"time"

	"github.com/Gergov00/pricescount/services/gateway/internal/store"
)

// Run deletes expired lookup_requests every interval until ctx is cancelled.
func Run(ctx context.Context, st *store.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := st.DeleteExpiredLookups(ctx)
			if err != nil {
				slog.Error("ttl cleaner: delete expired lookups", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("ttl cleaner: deleted expired lookups", "count", n)
			}
		}
	}
}
