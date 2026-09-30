//go:build integration

package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("TEST_POSTGRES_DSN is required for integration tests")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"test_" + uuid.NewString()}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), string(sql)); err != nil {
		t.Fatal(err)
	}
	return New(db), db
}

func TestScheduleLifecycleIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	url := "https://www.wildberries.ru/catalog/1/detail.aspx"
	id := uuid.NewString()
	if err := st.Add(ctx, id, url, "wb", 2); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueURLs(ctx)
	if err != nil || len(due) != 1 || due[0].ProductID != id || due[0].URL != url || due[0].Platform != "wb" {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	var future bool
	if err := db.QueryRow(ctx, `SELECT next_check_at > NOW()+INTERVAL '115 minutes' FROM scheduled_urls WHERE url=$1`, url).Scan(&future); err != nil || !future {
		t.Fatalf("interval applied=%v err=%v", future, err)
	}
	due, err = st.DueURLs(ctx)
	if err != nil || len(due) != 0 {
		t.Fatalf("redispatched future URL: %+v %v", due, err)
	}
	if err := st.SetActive(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, url); err != nil {
		t.Fatal(err)
	}
	due, err = st.DueURLs(ctx)
	if err != nil || len(due) != 0 {
		t.Fatalf("paused URL dispatched: %+v %v", due, err)
	}
	if err := st.SetNextCheck(ctx, url); err != nil {
		t.Fatal(err)
	}
	due, err = st.DueURLs(ctx)
	if err != nil || len(due) != 1 {
		t.Fatalf("resume did not dispatch: %+v %v", due, err)
	}
	if err := st.Delete(ctx, url); err != nil {
		t.Fatal(err)
	}
	due, err = st.DueURLs(ctx)
	if err != nil || len(due) != 0 {
		t.Fatalf("deleted URL dispatched: %+v %v", due, err)
	}
}

func TestAddPreservesExistingNextCheckIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	url := "https://www.wildberries.ru/catalog/2/detail.aspx"
	if err := st.Add(ctx, uuid.NewString(), url, "wb", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()+INTERVAL '3 hours', active=false WHERE url=$1`, url); err != nil {
		t.Fatal(err)
	}
	var before, after time.Time
	if err := db.QueryRow(ctx, `SELECT next_check_at FROM scheduled_urls WHERE url=$1`, url).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := st.Add(ctx, uuid.NewString(), url, "wb", 4); err != nil {
		t.Fatal(err)
	}
	var active bool
	var hours int
	if err := db.QueryRow(ctx, `SELECT next_check_at,active,check_interval_hours FROM scheduled_urls WHERE url=$1`, url).Scan(&after, &active, &hours); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) || !active || hours != 4 {
		t.Fatalf("resync reset schedule: before=%v after=%v active=%v hours=%d", before, after, active, hours)
	}
}

func TestDueURLsConcurrentWorkersIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	for _, url := range []string{"https://www.wildberries.ru/catalog/3/detail.aspx", "https://www.wildberries.ru/catalog/4/detail.aspx"} {
		if err := st.Add(ctx, uuid.NewString(), url, "wb", 1); err != nil {
			t.Fatal(err)
		}
	}
	other := New(db)
	results := make(chan []URLEntry, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, worker := range []*Store{st, other} {
		wg.Add(1)
		go func(worker *Store) {
			defer wg.Done()
			<-start
			due, err := worker.DueURLs(ctx)
			results <- due
			errs <- err
		}(worker)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for batch := range results {
		for _, e := range batch {
			if seen[e.URL] {
				t.Fatalf("URL claimed by both workers: %s", e.URL)
			}
			seen[e.URL] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("claimed %d URLs, want 2", len(seen))
	}
}

func TestDueURLsSkipsLockedRowsIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	lockedURL := "https://www.wildberries.ru/catalog/5/detail.aspx"
	freeURL := "https://www.wildberries.ru/catalog/6/detail.aspx"
	for _, url := range []string{lockedURL, freeURL} {
		if err := st.Add(ctx, uuid.NewString(), url, "wb", 1); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) }) // It may already have been explicitly rolled back.
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM scheduled_urls WHERE url=$1 FOR UPDATE`, lockedURL).Scan(&id); err != nil {
		t.Fatal(err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	due, err := st.DueURLs(queryCtx)
	if err != nil || len(due) != 1 || due[0].URL != freeURL {
		t.Fatalf("SKIP LOCKED returned %+v err=%v", due, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	due, err = st.DueURLs(ctx)
	if err != nil || len(due) != 1 || due[0].URL != lockedURL {
		t.Fatalf("released lock returned %+v err=%v", due, err)
	}
}
