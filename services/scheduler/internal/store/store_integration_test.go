//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryPublisher struct {
	mu        sync.Mutex
	failures  int
	published int
}

func (p *recoveryPublisher) Publish(ctx context.Context, _ string, _ any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return errors.New("broker unavailable")
	}
	p.published++
	return nil
}

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
	if _, err := db.Exec(t.Context(), string(sql)); err != nil {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	return New(db), db
}

func snapshot(id, url string, version int64, active bool) contracts.TrackRequest {
	return contracts.TrackRequest{TaskID: uuid.NewString(), Action: "set_state", ProductID: id, URL: url, Platform: "wb", Version: version, Active: active, IntervalHours: 2}
}
func force(id, taskID, url string) contracts.TrackRequest {
	return contracts.TrackRequest{TaskID: taskID, Action: "force", ProductID: id, URL: url, Platform: "wb", ChatID: 42}
}

func TestSnapshotReverseOrder(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/101/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 3, false)); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 2, true)); err != nil {
		t.Fatal(err)
	}
	var active bool
	var version int64
	if err := db.QueryRow(ctx, `SELECT active,monitor_version FROM scheduled_urls WHERE url=$1`, url).Scan(&active, &version); err != nil {
		t.Fatal(err)
	}
	if active || version != 3 {
		t.Fatalf("active=%v version=%d", active, version)
	}
}
func TestInactiveTombstone(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/102/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 3, false)); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 2, true)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduled_urls WHERE product_id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("tombstones=%d", count)
	}
}
func TestSnapshotRejectsURLProductMismatch(t *testing.T) {
	st, _ := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/103/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplySnapshot(ctx, snapshot(id, "https://www.wildberries.ru/catalog/104/detail.aspx", 2, true)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mismatch error=%v", err)
	}
}

func TestForceProductURLMismatchDoesNotConsumeTaskID(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	productID := uuid.NewString()
	existingURL := "https://www.wildberries.ru/catalog/114/detail.aspx"
	wrongURL := "https://www.wildberries.ru/catalog/115/detail.aspx"
	taskID := uuid.NewString()
	if err := st.ApplySnapshot(ctx, snapshot(productID, existingURL, 1, true)); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueForce(ctx, force(productID, taskID, wrongURL)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("wrong product URL error=%v", err)
	}
	var processed, events int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_force_commands WHERE task_id=$1`, taskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE event_key=$1`, "force:"+taskID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if processed != 0 || events != 0 {
		t.Fatalf("invalid force consumed task: processed=%d events=%d", processed, events)
	}
	if err := st.EnqueueForce(ctx, force(productID, taskID, existingURL)); err != nil {
		t.Fatalf("correct retry with same TaskID: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_force_commands WHERE task_id=$1`, taskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE event_key=$1`, "force:"+taskID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if processed != 1 || events != 1 {
		t.Fatalf("valid retry was not persisted once: processed=%d events=%d", processed, events)
	}
}
func TestDueOutboxRollback(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/105/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := db.QueryRow(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1 RETURNING next_check_at`, url).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_scheduler_events() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$; CREATE TRIGGER reject_scheduler_events BEFORE INSERT ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION reject_scheduler_events()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDue(ctx); err == nil {
		t.Fatal("expected injected outbox failure")
	}
	var after time.Time
	var events int
	if err := db.QueryRow(ctx, `SELECT next_check_at FROM scheduled_urls WHERE url=$1`, url).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) || events != 0 {
		t.Fatalf("next_check before=%v after=%v events=%d", before, after, events)
	}
}
func TestForceScheduleUnchanged(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/106/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, false)); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	var beforeActive bool
	if err := db.QueryRow(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()+INTERVAL '3 hours' WHERE url=$1 RETURNING next_check_at,active`, url).Scan(&before, &beforeActive); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueForce(ctx, force(id, uuid.NewString(), url)); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	var afterActive bool
	if err := db.QueryRow(ctx, `SELECT next_check_at,active FROM scheduled_urls WHERE url=$1`, url).Scan(&after, &afterActive); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) || beforeActive != afterActive || afterActive {
		t.Fatalf("schedule changed: %v/%v → %v/%v", before, beforeActive, after, afterActive)
	}
}
func TestForceReplaySingleTask(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url, taskID := uuid.NewString(), "https://www.wildberries.ru/catalog/107/detail.aspx", uuid.NewString()
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, false)); err != nil {
		t.Fatal(err)
	}
	req := force(id, taskID, url)
	if err := st.EnqueueForce(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueForce(ctx, req); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE event_key=$1`, "force:"+taskID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("force events=%d", events)
	}
}
func TestForceBeforeSnapshotDoesNotCreateSchedule(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/108/detail.aspx"
	if err := st.EnqueueForce(ctx, force(id, uuid.NewString(), url)); err != nil {
		t.Fatal(err)
	}
	var schedules, events int
	if err := db.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM scheduled_urls),(SELECT COUNT(*) FROM scheduler_outbox)`).Scan(&schedules, &events); err != nil {
		t.Fatal(err)
	}
	if schedules != 0 || events != 1 {
		t.Fatalf("schedules=%d events=%d", schedules, events)
	}
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, false)); err != nil {
		t.Fatal(err)
	}
}
func TestEnqueueDueConcurrentWorkersIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	for n := 200; n < 202; n++ {
		id := uuid.NewString()
		url := fmt.Sprintf("https://www.wildberries.ru/catalog/%d/detail.aspx", n)
		if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, url); err != nil {
			t.Fatal(err)
		}
	}
	other := New(db)
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, worker := range []*Store{st, other} {
		wg.Add(1)
		go func(worker *Store) {
			defer wg.Done()
			<-start
			n, err := worker.EnqueueDue(ctx)
			counts <- n
			errs <- err
		}(worker)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for n := range counts {
		total += n
	}
	if total != 2 {
		t.Fatalf("total claimed=%d", total)
	}
	var tasks int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE event_key LIKE 'periodic:%'`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 {
		t.Fatalf("outbox tasks=%d", tasks)
	}
}

func TestEnqueueDueSkipsLockedRowsIntegration(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	lockedURL := "https://www.wildberries.ru/catalog/111/detail.aspx"
	freeURL := "https://www.wildberries.ru/catalog/112/detail.aspx"
	for _, item := range []struct{ id, url string }{{uuid.NewString(), lockedURL}, {uuid.NewString(), freeURL}} {
		if err := st.ApplySnapshot(ctx, snapshot(item.id, item.url, 1, true)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, item.url); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback row lock: %v", err)
		}
	}()
	var rowID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM scheduled_urls WHERE url=$1 FOR UPDATE`, lockedURL).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	other := New(db)
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	n, err := other.EnqueueDue(queryCtx)
	if err != nil || n != 1 {
		t.Fatalf("enqueue while locked=%d err=%v", n, err)
	}
	var freeEvents int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox o JOIN scheduled_urls s ON o.payload->>'url'=s.url WHERE s.url=$1`, freeURL).Scan(&freeEvents); err != nil {
		t.Fatal(err)
	}
	if freeEvents != 1 {
		t.Fatalf("free row events=%d", freeEvents)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	n, err = other.EnqueueDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("enqueue after lock release=%d err=%v", n, err)
	}
}

func TestOutboxLeaseOwnership(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/113/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, url); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDue(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := st.Claim(ctx, 1, time.Millisecond)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%v err=%v", first, err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := st.Claim(ctx, 1, time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim=%v err=%v", second, err)
	}
	if err := st.MarkPublished(ctx, first[0].ID, first[0].LeaseToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale lease mark=%v", err)
	}
	if err := st.MarkPublished(ctx, second[0].ID, second[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
}
func TestDueOutboxPayloadIsComplete(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/109/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, url); err != nil {
		t.Fatal(err)
	}
	n, err := st.EnqueueDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("enqueue=%d err=%v", n, err)
	}
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT payload FROM scheduler_outbox LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var task contracts.ScraperTask
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.TaskID == "" || task.ProductID != id || !task.ScheduledAt.After(time.Time{}) {
		t.Fatalf("task=%+v", task)
	}
}

func TestOutboxPendingWhilePublisherOfflineThenRecovers(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	id, url := uuid.NewString(), "https://www.wildberries.ru/catalog/110/detail.aspx"
	if err := st.ApplySnapshot(ctx, snapshot(id, url, 1, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()-INTERVAL '1 minute' WHERE url=$1`, url); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDue(ctx); err != nil {
		t.Fatal(err)
	}
	p := &recoveryPublisher{failures: 1}
	worker := outbox.New(st, p)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var attempts int
		if err := db.QueryRow(ctx, `SELECT COALESCE(MAX(attempts),0) FROM scheduler_outbox`).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if attempts >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait until Retry clears the lease; attempts increments at Claim before Publish.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var unlocked int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE lease_token IS NULL AND published_at IS NULL`).Scan(&unlocked); err != nil {
			t.Fatal(err)
		}
		if unlocked == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker error=%v", err)
	}
	var pending int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending after publisher error=%d", pending)
	}
	if _, err := db.Exec(ctx, `UPDATE scheduler_outbox SET available_at=NOW(),locked_until=NULL,lease_token=NULL`); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.failures = 0
	p.mu.Unlock()
	runCtx, cancel = context.WithCancel(ctx)
	done = make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE published_at IS NOT NULL`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery worker error=%v", err)
	}
	if pending != 1 {
		t.Fatal("outbox event was not delivered after publisher recovery")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published != 1 {
		t.Fatalf("published=%d", p.published)
	}
}
