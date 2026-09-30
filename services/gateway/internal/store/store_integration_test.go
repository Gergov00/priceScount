//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationStore uses a fresh schema, never truncating a shared database.
func integrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("TEST_POSTGRES_DSN is required for integration tests")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "test_" + uuid.New().String()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted + ",public"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // Existing data must tolerate reapplying the migration.
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	return New(db), db
}

func seedProduct(t *testing.T, st *Store) (string, string) {
	t.Helper()
	user, err := st.UpsertUser(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	product, err := st.EnsureProduct(t.Context(), uuid.NewString(), "Телефон", "https://www.wildberries.ru/catalog/123/detail.aspx", "wb")
	if err != nil {
		t.Fatal(err)
	}
	return user, product
}

func TestLookupLifecycleIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id := uuid.NewString()
	url := "https://www.wildberries.ru/catalog/123/detail.aspx"
	if err := st.CreateLookup(ctx, id, url); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PendingLookupCount(ctx); err != nil || n != 1 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
	r, err := st.GetLookup(ctx, id)
	if err != nil || r.Status != LookupPending || r.URL != url {
		t.Fatalf("pending lookup=%+v err=%v", r, err)
	}
	if err := st.CompleteLookup(ctx, id, "Телефон", 1234.50); err != nil {
		t.Fatal(err)
	}
	r, err = st.GetLookup(ctx, id)
	if err != nil || r.Status != LookupDone || r.Name != "Телефон" || r.Price != 1234.50 {
		t.Fatalf("completed lookup=%+v err=%v", r, err)
	}
	var extended bool
	if err := db.QueryRow(ctx, `SELECT expires_at > NOW() + INTERVAL '55 minutes' FROM lookup_requests WHERE id=$1`, id).Scan(&extended); err != nil || !extended {
		t.Fatalf("completed TTL extended=%v err=%v", extended, err)
	}
	failedID := uuid.NewString()
	if err := st.CreateLookup(ctx, failedID, url); err != nil {
		t.Fatal(err)
	}
	if err := st.FailLookup(ctx, failedID, "price unavailable"); err != nil {
		t.Fatal(err)
	}
	r, err = st.GetLookup(ctx, failedID)
	if err != nil || r.Status != LookupFailed || r.Error != "price unavailable" {
		t.Fatalf("failed lookup=%+v err=%v", r, err)
	}
	if n, err := st.PendingLookupCount(ctx); err != nil || n != 0 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
	if _, err := db.Exec(ctx, `UPDATE lookup_requests SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, failedID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.DeleteExpiredLookups(ctx); err != nil || n != 1 {
		t.Fatalf("expired deleted=%d err=%v", n, err)
	}
	if _, err := st.GetLookup(ctx, failedID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired lookup err=%v", err)
	}
	if _, err := st.GetLookup(ctx, id); err != nil {
		t.Fatalf("valid lookup deleted: %v", err)
	}
}

func TestEnsureProductConcurrentIntegration(t *testing.T) {
	st, db := integrationStore(t)
	const workers = 8
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := st.EnsureProduct(t.Context(), uuid.NewString(), "Товар", "https://www.wildberries.ru/catalog/1/detail.aspx", "wb")
			ids <- id
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("different canonical IDs: %s / %s", first, id)
		}
	}
	var n int
	if err := db.QueryRow(t.Context(), `SELECT COUNT(*) FROM products`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("products=%d err=%v", n, err)
	}
}

func TestSubscriptionsOwnershipAndLifecycleIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u1, product := seedProduct(t, st)
	u2, err := st.UpsertUser(ctx, 202)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := st.UpsertUser(ctx, 101); err != nil || again != u1 {
		t.Fatalf("user upsert id=%s err=%v", again, err)
	}
	if got, err := st.UserIDByChatID(ctx, 101); err != nil || got != u1 {
		t.Fatalf("user lookup=%s err=%v", got, err)
	}
	if _, err := st.UserIDByChatID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user err=%v", err)
	}
	s1, err := st.CreateSubscription(ctx, u1, product, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := st.CreateSubscription(ctx, u2, product, 80, 300)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSubscription(ctx, s1, u2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner accessed subscription: %v", err)
	}
	if err := st.PauseSubscription(ctx, s1, u2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner pause error=%v, want not found", err)
	}
	sub, err := st.GetSubscription(ctx, s1, u1)
	if err != nil || sub.Paused {
		t.Fatalf("other owner mutated subscription: %+v %v", sub, err)
	}
	if err := st.PauseSubscription(ctx, s1, u1); err != nil {
		t.Fatal(err)
	}
	var activeCount, watchingCount int
	if err = db.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE NOT paused) FROM subscriptions WHERE product_id=$1 AND active`, product).Scan(&activeCount, &watchingCount); err != nil || activeCount != 2 || watchingCount != 1 {
		t.Fatalf("active=%d watching=%d err=%v", activeCount, watchingCount, err)
	}
	var watcherCount int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM subscriptions WHERE product_id=$1 AND active AND NOT paused`, product).Scan(&watcherCount); err != nil || watcherCount != 1 {
		t.Fatalf("watchers=%d err=%v", watcherCount, err)
	}
	var activeProducts int
	if err = db.QueryRow(ctx, `SELECT COUNT(DISTINCT product_id) FROM subscriptions WHERE active AND NOT paused`).Scan(&activeProducts); err != nil || activeProducts != 1 {
		t.Fatalf("active products=%d err=%v", activeProducts, err)
	}
	if err := st.ResumeSubscription(ctx, s1, u1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE subscriptions SET alert_state='up' WHERE id=$1`, s1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateThresholds(ctx, s1, u1, 120, 250); err != nil {
		t.Fatal(err)
	}
	var state string
	var min, max float64
	if err = db.QueryRow(ctx, `SELECT alert_state,min_price,max_price FROM subscriptions WHERE id=$1`, s1).Scan(&state, &min, &max); err != nil || state != "" || min != 120 || max != 250 {
		t.Fatalf("edited subscription state=%q min=%v max=%v err=%v", state, min, max, err)
	}
	_ = s2
	if err := st.DeleteSubscription(ctx, s1, u1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSubscription(ctx, s1, u1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted subscription err=%v", err)
	}
	list, err := st.UserSubscriptions(ctx, u1)
	if err != nil || len(list) != 0 {
		t.Fatalf("deleted subscription visible: %+v %v", list, err)
	}
	if err := st.PauseSubscription(ctx, s2, u2); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(DISTINCT product_id) FROM subscriptions WHERE active AND NOT paused`).Scan(&activeProducts); err != nil || activeProducts != 0 {
		t.Fatalf("paused product still active: %d %v", activeProducts, err)
	}
}

func TestPriceHistoryRetentionIntegration(t *testing.T) {
	st, db := integrationStore(t)
	_, product := seedProduct(t, st)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	for _, point := range []struct {
		price float64
		at    time.Time
	}{{99, now.Add(-100 * 24 * time.Hour)}, {110, now.Add(-time.Hour)}, {120.50, now}} {
		if _, err := db.Exec(ctx, `INSERT INTO price_history(product_id,price,currency,scraped_at) VALUES($1,$2,'RUB',$3)`, product, point.price, point.at); err != nil {
			t.Fatal(err)
		}
	}
	points, err := st.PriceHistory(ctx, product, 2)
	if err != nil || len(points) != 2 || points[0].Price != 120.50 || points[1].Price != 110 || !points[0].ScrapedAt.Equal(now) {
		t.Fatalf("history=%+v err=%v", points, err)
	}
	if n, err := st.DeleteOldPriceHistory(ctx, 90*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("retention deleted=%d err=%v", n, err)
	}
	points, err = st.PriceHistory(ctx, product, 50)
	if err != nil || len(points) != 2 {
		t.Fatalf("retained history=%+v err=%v", points, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO price_history(product_id,price,currency,scraped_at) VALUES($1,1,'RUB',$2)`, uuid.NewString(), now); err == nil {
		t.Fatal("history accepted nonexistent product")
	}
}
