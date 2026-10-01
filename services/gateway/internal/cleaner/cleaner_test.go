package cleaner

import (
	"context"
	"testing"
	"time"
)

type storeSpy struct{ lookups, prices int }

func (s *storeSpy) DeleteExpiredLookups(context.Context) (int64, error) { s.lookups++; return 0, nil }
func (s *storeSpy) DeleteOldPriceHistory(context.Context, time.Duration) (int64, error) {
	s.prices++
	return 0, nil
}
func TestRunReturnsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &storeSpy{}
	Run(ctx, s, time.Hour, 24*time.Hour)
	if s.lookups != 0 || s.prices != 0 {
		t.Fatalf("cleaner touched store after cancellation: lookups=%d prices=%d", s.lookups, s.prices)
	}
}
