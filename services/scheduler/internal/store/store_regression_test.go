//go:build integration && regression

package store

import (
	"testing"

	"github.com/google/uuid"
)

// F6: a one-time check must not reactivate paused periodic monitoring.
func TestAdvanceNextCheckKeepsPausedScheduleRegression(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	url := "https://www.wildberries.ru/catalog/7/detail.aspx"
	if err := st.Add(ctx, uuid.NewString(), url, "wb", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceNextCheck(ctx, url); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := db.QueryRow(ctx, `SELECT active FROM scheduled_urls WHERE url=$1`, url).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("F6: force-check reactivated paused monitoring")
	}
}
