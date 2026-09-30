//go:build integration

package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// F6: a one-time check must not reactivate paused periodic monitoring.
func TestForceCheckKeepsPausedSchedule(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/7/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, false)); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	var active bool
	if err := db.QueryRow(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()+INTERVAL '2 hours' WHERE url=$1 RETURNING next_check_at,active`, url).Scan(&before, &active); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueForce(ctx, force(id, uuid.NewString(), url)); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := db.QueryRow(ctx, `SELECT next_check_at,active FROM scheduled_urls WHERE url=$1`, url).Scan(&after, &active); err != nil {
		t.Fatal(err)
	}
	if active || !before.Equal(after) {
		t.Fatalf("F6: force changed paused schedule, active=%v next=%v→%v", active, before, after)
	}
}
