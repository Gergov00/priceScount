//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSubscriptionSnapshotAtomic(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	user, product := seedProduct(t, st)
	var before int64
	if err := db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, product).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(ctx, `CREATE FUNCTION fail_gateway_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$; CREATE TRIGGER fail_gateway_outbox BEFORE INSERT ON gateway_outbox FOR EACH ROW EXECUTE FUNCTION fail_gateway_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSubscription(ctx, user, product, 100, 200); err == nil {
		t.Fatal("expected injected failure")
	}
	var subs int
	var version int64
	var outbox int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM subscriptions`).Scan(&subs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, product).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if subs != 0 || version != before || outbox != 0 {
		t.Fatalf("partial commit subs=%d version=%d/%d outbox=%d", subs, version, before, outbox)
	}
}

func TestPriceResultRollbackLeavesNoPartialEffects(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE subscriptions SET alert_state='down' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CREATE FUNCTION fail_gateway_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$; CREATE TRIGGER fail_gateway_outbox BEFORE INSERT ON gateway_outbox FOR EACH ROW EXECUTE FUNCTION fail_gateway_outbox()`); err != nil {
		t.Fatal(err)
	}
	r := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: p, Price: 250, Currency: "RUB", ScrapedAt: time.Now().UTC(), Success: true}
	if err = st.ProcessPriceResult(ctx, r); err == nil {
		t.Fatal("expected injected notification failure")
	}
	var history, processed, notifications int
	var state string
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE task_id=$1`, r.TaskID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_price_results WHERE task_id=$1`, r.TaskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key LIKE 'alert:%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT alert_state FROM subscriptions WHERE id=$1`, sub).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if history != 0 || processed != 0 || notifications != 0 || state != "down" {
		t.Fatalf("partial result history=%d processed=%d notifications=%d state=%s", history, processed, notifications, state)
	}
}

func TestLookupResultTaskIDDeduplicatesAtomically(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	lookupID := uuid.NewString()
	url := "https://www.wildberries.ru/catalog/123/detail.aspx"
	if err := st.CreateLookup(ctx, lookupID, url); err != nil {
		t.Fatal(err)
	}
	r := contracts.PriceResult{TaskID: uuid.NewString(), LookupID: lookupID, Success: true, Name: "Original", Price: 10}
	if err := st.ProcessPriceResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Name = "Changed"
	r.Price = 99
	if err := st.ProcessPriceResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	var name string
	var price float64
	var processed int
	if err := db.QueryRow(ctx, `SELECT name,price FROM lookup_requests WHERE id=$1`, lookupID).Scan(&name, &price); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_price_results WHERE task_id=$1`, r.TaskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if name != "Original" || price != 10 || processed != 1 {
		t.Fatalf("lookup name=%s price=%v processed=%d", name, price, processed)
	}
}

func TestDeleteRetryBrokerUnavailable(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	id, err := st.CreateSubscription(ctx, u, p, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteSubscription(ctx, id, u); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteSubscription(ctx, id, u); err != nil {
		t.Fatalf("owner retry delete: %v", err)
	}
	if err = st.DeleteSubscription(ctx, id, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete err=%v", err)
	}
	var n int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM subscriptions WHERE id=$1 AND NOT active`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("inactive rows=%d err=%v", n, err)
	}
}

func TestConcurrentResultSingleTransition(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	if _, err := st.CreateSubscription(ctx, u, p, 100, 200); err != nil {
		t.Fatal(err)
	}
	r := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: p, Price: 50, Currency: "RUB", ScrapedAt: time.Now().UTC(), Success: true}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- st.ProcessPriceResult(ctx, r) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var history, processed, alerts int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE task_id=$1`, r.TaskID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_price_results WHERE task_id=$1`, r.TaskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key LIKE 'alert:%'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if history != 1 || processed != 1 || alerts != 1 {
		t.Fatalf("history=%d processed=%d alerts=%d", history, processed, alerts)
	}
}

func TestNotificationOutboxSurvivesBrokerUnavailable(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	if _, err := st.CreateSubscription(ctx, u, p, 100, 200); err != nil {
		t.Fatal(err)
	}
	r := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: p, Price: 50, Currency: "RUB", ScrapedAt: time.Now().UTC(), Success: true}
	if err := st.ProcessPriceResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key LIKE 'alert:%' AND published_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("committed pending notifications=%d, want 1 while publisher is offline", pending)
	}
}

func TestOlderResultDoesNotRevertState(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	newer := time.Now().UTC().Truncate(time.Microsecond)
	for _, r := range []contracts.PriceResult{{TaskID: uuid.NewString(), ProductID: p, Price: 50, Currency: "RUB", ScrapedAt: newer, Success: true}, {TaskID: uuid.NewString(), ProductID: p, Price: 250, Currency: "RUB", ScrapedAt: newer.Add(-time.Minute), Success: true}} {
		if err := st.ProcessPriceResult(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var latest time.Time
	if err = db.QueryRow(ctx, `SELECT alert_state FROM subscriptions WHERE id=$1`, sub).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT latest_result_at FROM products WHERE id=$1`, p).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	var history int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE product_id=$1`, p).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if state != "down" || !latest.Equal(newer) || history != 2 {
		t.Fatalf("state=%s latest=%s history=%d", state, latest, history)
	}
}

func TestEqualTimestampDifferentTaskIDsKeepBothHistoryRowsAndTransitions(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	user, product := seedProduct(t, st)
	subID, err := st.CreateSubscription(ctx, user, product, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	scrapedAt := time.Now().UTC().Truncate(time.Microsecond)
	firstID, secondID := uuid.NewString(), uuid.NewString()
	for _, result := range []contracts.PriceResult{
		{TaskID: firstID, ProductID: product, Price: 50, Currency: "RUB", ScrapedAt: scrapedAt, Success: true},
		{TaskID: secondID, ProductID: product, Price: 250, Currency: "RUB", ScrapedAt: scrapedAt, Success: true},
	} {
		if err := st.ProcessPriceResult(ctx, result); err != nil {
			t.Fatal(err)
		}
	}
	var history, alerts int
	var state string
	var latest time.Time
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE product_id=$1 AND task_id IN ($2,$3)`, product, firstID, secondID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT alert_state FROM subscriptions WHERE id=$1`, subID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT latest_result_at FROM products WHERE id=$1`, product).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key LIKE 'alert:%'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if history != 2 || state != "up" || !latest.Equal(scrapedAt) || alerts != 2 {
		t.Fatalf("equal-time results history=%d state=%q latest=%s alerts=%d", history, state, latest, alerts)
	}
}

func TestConcurrentPauseAndAddKeepsLatestSnapshotActive(t *testing.T) {
	for _, first := range []string{"add", "pause"} {
		t.Run(first+"-first", func(t *testing.T) {
			st, db := integrationStore(t)
			ctx := t.Context()
			user1, product := seedProduct(t, st)
			if _, err := st.CreateSubscription(ctx, user1, product, 100, 200); err != nil {
				t.Fatal(err)
			}
			user2, err := st.UpsertUser(ctx, 202)
			if err != nil {
				t.Fatal(err)
			}
			subID := subscriptionID(t, db, user1, product)
			lock, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			committed := false
			defer func() {
				if !committed {
					if err := lock.Rollback(context.Background()); err != nil {
						t.Errorf("release held product lock: %v", err)
					}
				}
			}()
			if err = lock.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR UPDATE`, product).Scan(new(string)); err != nil {
				t.Fatal(err)
			}

			type outcome struct{ err error }
			started := make(chan outcome, 2)
			launch := func(action string) {
				go func() {
					started <- outcome{err: func() error {
						if action == "pause" {
							return st.PauseSubscription(ctx, subID, user1)
						}
						_, createErr := st.CreateSubscription(ctx, user2, product, 80, 220)
						return createErr
					}()}
				}()
			}
			second := "pause"
			if first == "pause" {
				second = "add"
			}
			launch(first)
			waitForBlockedProductLock(t, db, 1)
			launch(second)
			waitForBlockedProductLock(t, db, 2)
			if err = lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			committed = true
			for range 2 {
				if result := <-started; result.err != nil {
					t.Fatal(result.err)
				}
			}

			var active bool
			var watching int
			var version int64
			if err := db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, product).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM subscriptions WHERE product_id=$1 AND active AND NOT paused`, product).Scan(&watching); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(ctx, `SELECT (payload->>'active')::boolean FROM gateway_outbox WHERE event_key=$1`, fmt.Sprintf("track:%s:%d", product, version)).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if !active || watching != 1 {
				t.Fatalf("latest snapshot active=%v watching=%d version=%d", active, watching, version)
			}
		})
	}
}

func TestLegacySchemaUpgradeTwicePreservesUserSubscriptionHistoryAndSchedule(t *testing.T) {
	db := legacyIntegrationDB(t)
	ctx := t.Context()
	fixture, err := os.ReadFile("testdata/pre_audit_repair.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		INSERT INTO users(id,chat_id,created_at) VALUES('10000000-0000-4000-8000-000000000001',701,'2026-09-25 10:00:00+00');
		INSERT INTO products(id,name,url,platform,created_at) VALUES('20000000-0000-4000-8000-000000000001','legacy phone','https://www.wildberries.ru/catalog/701/detail.aspx','wb','2026-09-25 10:00:00+00');
		INSERT INTO subscriptions(id,user_id,product_id,min_price,max_price,paused,active,alert_state,created_at)
		VALUES('30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001',100,200,true,true,'down','2026-09-25 10:00:00+00');
		INSERT INTO price_history(id,product_id,price,currency,scraped_at)
		VALUES('40000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001',75,'RUB','2026-09-25 10:00:00+00');
		INSERT INTO scheduled_urls(id,product_id,url,platform,next_check_at,check_interval_hours,active)
		VALUES('50000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','https://www.wildberries.ru/catalog/701/detail.aspx','wb','2026-09-26 10:00:00+00',3,false)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = db.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}

	var userChat int64
	var productName, subscriptionState, historyCurrency, scheduledURL string
	var min, max, price float64
	var paused, active, scheduledActive bool
	var latest time.Time
	if err = db.QueryRow(ctx, `SELECT u.chat_id,p.name,s.alert_state,s.min_price,s.max_price,s.paused,s.active,h.price,h.currency,p.latest_result_at,sc.url,sc.active FROM users u JOIN subscriptions s ON s.user_id=u.id JOIN products p ON p.id=s.product_id JOIN price_history h ON h.product_id=p.id JOIN scheduled_urls sc ON sc.product_id=p.id WHERE u.id='10000000-0000-4000-8000-000000000001'`).Scan(&userChat, &productName, &subscriptionState, &min, &max, &paused, &active, &price, &historyCurrency, &latest, &scheduledURL, &scheduledActive); err != nil {
		t.Fatal(err)
	}
	if userChat != 701 || productName != "legacy phone" || subscriptionState != "down" || min != 100 || max != 200 || !paused || !active || price != 75 || historyCurrency != "RUB" || !latest.Equal(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)) || scheduledURL != "https://www.wildberries.ru/catalog/701/detail.aspx" || scheduledActive {
		t.Fatalf("legacy data changed: chat=%d product=%q state=%q range=%v-%v paused=%v active=%v history=%v %s latest=%s schedule=%q scheduled_active=%v", userChat, productName, subscriptionState, min, max, paused, active, price, historyCurrency, latest, scheduledURL, scheduledActive)
	}
	var users, subscriptions, history, schedules, missingTaskIDs int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM subscriptions`).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduled_urls`).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE task_id IS NULL`).Scan(&missingTaskIDs); err != nil {
		t.Fatal(err)
	}
	if users != 1 || subscriptions != 1 || history != 1 || schedules != 1 || missingTaskIDs != 1 {
		t.Fatalf("row preservation users=%d subscriptions=%d history=%d schedules=%d legacy task IDs=%d", users, subscriptions, history, schedules, missingTaskIDs)
	}
}

func TestLegacyDuplicateScheduledProductsFailWithoutDeletingData(t *testing.T) {
	db := legacyIntegrationDB(t)
	ctx := t.Context()
	fixture, err := os.ReadFile("testdata/pre_audit_repair.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO scheduled_urls(product_id,url,platform) VALUES
		('20000000-0000-4000-8000-000000000099','https://example.test/duplicate-a','wb'),
		('20000000-0000-4000-8000-000000000099','https://example.test/duplicate-b','wb')`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(migration)); err == nil || !strings.Contains(err.Error(), "resolve duplicates before applying scheduler identity constraint") {
		t.Fatalf("duplicate product migration error=%v, want explicit reconciliation instruction", err)
	}
	var rows int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduled_urls WHERE product_id='20000000-0000-4000-8000-000000000099'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	var productIndex bool
	if err = db.QueryRow(ctx, `SELECT to_regclass('idx_scheduled_urls_product_id') IS NOT NULL`).Scan(&productIndex); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || productIndex {
		t.Fatalf("duplicate data rows=%d, unique index created=%v", rows, productIndex)
	}
}

func legacyIntegrationDB(t *testing.T) *pgxpool.Pool {
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
	schema := "legacy_" + uuid.NewString()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop legacy schema: %v", err)
		}
	})
	return db
}

func subscriptionID(t *testing.T, db *pgxpool.Pool, userID, productID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(t.Context(), `SELECT id FROM subscriptions WHERE user_id=$1 AND product_id=$2`, userID, productID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitForBlockedProductLock(t *testing.T, db *pgxpool.Pool, want int) {
	t.Helper()
	var applicationName string
	if err := db.QueryRow(t.Context(), `SELECT current_setting('application_name')`).Scan(&applicationName); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		err := db.QueryRow(t.Context(), `SELECT COUNT(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query ILIKE '%from products where id=$1 for update%'`, applicationName).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d blocked product locks", want)
}

func TestForcePausedAndFailedFetch(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PauseSubscription(ctx, sub, u); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE subscriptions SET alert_state='down' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	var monitorVersion int64
	if err = db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, p).Scan(&monitorVersion); err != nil {
		t.Fatal(err)
	}
	taskID, err := st.QueueForce(ctx, sub, u, 101)
	if err != nil {
		t.Fatal(err)
	}
	r := contracts.PriceResult{TaskID: taskID, ProductID: p, Success: false, Force: true, Error: "fetch failed"}
	if err = st.ProcessPriceResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	var actualState string
	var paused bool
	var actualVersion int64
	if err = db.QueryRow(ctx, `SELECT alert_state,paused FROM subscriptions WHERE id=$1`, sub).Scan(&actualState, &paused); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, p).Scan(&actualVersion); err != nil {
		t.Fatal(err)
	}
	if actualState != "down" || !paused || actualVersion != monitorVersion {
		t.Fatalf("failed force changed periodic state=%s paused=%v version=%d want=%d", actualState, paused, actualVersion, monitorVersion)
	}
	var target, text string
	if err = db.QueryRow(ctx, `SELECT payload->>'target',payload->>'text' FROM gateway_outbox WHERE event_key=$1`, "force-result:"+taskID).Scan(&target, &text); err != nil {
		t.Fatal(err)
	}
	if target != "101" || !strings.Contains(text, "fetch failed") {
		t.Fatalf("force target=%s text=%s", target, text)
	}
	if err = st.ProcessPriceResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key=$1`, "force-result:"+taskID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("force notifications=%d err=%v", count, err)
	}
	var wrong []byte
	if err = db.QueryRow(ctx, `SELECT payload FROM gateway_outbox WHERE event_key=$1`, "force-result:"+taskID).Scan(&wrong); err != nil {
		t.Fatal(err)
	}
	var msg contracts.NotifyTask
	if err = json.Unmarshal(wrong, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Target != "101" {
		t.Fatalf("requester=%s", msg.Target)
	}
	unknown := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: p, Force: true, Error: "unknown request"}
	if err = st.ProcessPriceResult(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	var unknownNotifications int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key=$1`, "force-result:"+unknown.TaskID).Scan(&unknownNotifications); err != nil || unknownNotifications != 0 {
		t.Fatalf("unknown force notifications=%d err=%v", unknownNotifications, err)
	}
	successTask, err := st.QueueForce(ctx, sub, u, 101)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ProcessPriceResult(ctx, contracts.PriceResult{TaskID: successTask, ProductID: p, Force: true, Success: true, Price: 125, Currency: "RUB", ScrapedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var forcedHistory int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE task_id=$1`, successTask).Scan(&forcedHistory); err != nil || forcedHistory != 1 {
		t.Fatalf("successful force history=%d err=%v", forcedHistory, err)
	}
}

func TestMismatchedForceProductDoesNotConsumeTaskID(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, product := seedProduct(t, st)
	subID, err := st.CreateSubscription(ctx, u, product, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	otherProduct, err := st.EnsureProduct(ctx, uuid.NewString(), "Другой товар", "https://www.wildberries.ru/catalog/987654/detail.aspx", "wb")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := st.QueueForce(ctx, subID, u, 101)
	if err != nil {
		t.Fatal(err)
	}
	wrong := contracts.PriceResult{TaskID: taskID, ProductID: otherProduct, Force: true, Success: true, Price: 1, Currency: "RUB", ScrapedAt: time.Now().UTC()}
	if err = st.ProcessPriceResult(ctx, wrong); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("mismatched result error=%v", err)
	}
	valid := contracts.PriceResult{TaskID: taskID, ProductID: product, Force: true, Success: true, Price: 125, Currency: "RUB", ScrapedAt: time.Now().UTC()}
	if err = st.ProcessPriceResult(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if err = st.ProcessPriceResult(ctx, valid); err != nil {
		t.Fatal(err)
	}
	var processed, history, notifications int
	var target string
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM processed_price_results WHERE task_id=$1`, taskID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE task_id=$1`, taskID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*),MIN(payload->>'target') FROM gateway_outbox WHERE event_key=$1`, "force-result:"+taskID).Scan(&notifications, &target); err != nil {
		t.Fatal(err)
	}
	if processed != 1 || history != 1 || notifications != 1 || target != "101" {
		t.Fatalf("processed=%d history=%d notifications=%d target=%s", processed, history, notifications, target)
	}
}

func TestSuccessfulForceTimestampBlocksOlderPeriodicResult(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	forceTask, err := st.QueueForce(ctx, sub, u, 101)
	if err != nil {
		t.Fatal(err)
	}
	forcedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err = st.ProcessPriceResult(ctx, contracts.PriceResult{TaskID: forceTask, ProductID: p, Force: true, Success: true, Price: 150, Currency: "RUB", ScrapedAt: forcedAt}); err != nil {
		t.Fatal(err)
	}
	older := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: p, Success: true, Price: 250, Currency: "RUB", ScrapedAt: forcedAt.Add(-time.Minute)}
	if err = st.ProcessPriceResult(ctx, older); err != nil {
		t.Fatal(err)
	}
	var state string
	var latest time.Time
	var history, alerts int
	if err = db.QueryRow(ctx, `SELECT alert_state FROM subscriptions WHERE product_id=$1`, p).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT latest_result_at FROM products WHERE id=$1`, p).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM price_history WHERE product_id=$1`, p).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_outbox WHERE event_key LIKE 'alert:%'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if state != "" || !latest.Equal(forcedAt) || history != 2 || alerts != 0 {
		t.Fatalf("state=%q latest=%s history=%d alerts=%d", state, latest, history, alerts)
	}
}

func TestLeaseOwnership(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	if err := st.CreateLookup(ctx, uuid.NewString(), "https://www.wildberries.ru/catalog/123/detail.aspx"); err != nil {
		t.Fatal(err)
	}
	first, err := st.Claim(ctx, 1, time.Second)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err = db.Exec(ctx, `UPDATE gateway_outbox SET locked_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := st.Claim(ctx, 1, time.Second)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	if err = st.MarkPublished(ctx, first[0].ID, first[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	var published *time.Time
	if err = db.QueryRow(ctx, `SELECT published_at FROM gateway_outbox WHERE id=$1`, first[0].ID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != nil {
		t.Fatal("stale lease marked published")
	}
	if err = st.MarkPublished(ctx, second[0].ID, second[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT published_at FROM gateway_outbox WHERE id=$1`, first[0].ID).Scan(&published); err != nil || published == nil {
		t.Fatalf("current lease mark published=%v err=%v", published, err)
	}
}

func TestPausedIdenticalCreatePreservesAlertAndReactivatesSnapshot(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE subscriptions SET alert_state='down' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	if err = st.PauseSubscription(ctx, sub, u); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err = db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, p).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSubscription(ctx, u, p, 100, 200); err != nil {
		t.Fatal(err)
	}
	var state string
	var paused bool
	var version int64
	if err = db.QueryRow(ctx, `SELECT alert_state,paused FROM subscriptions WHERE id=$1`, sub).Scan(&state, &paused); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT monitor_version FROM products WHERE id=$1`, p).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err = db.QueryRow(ctx, `SELECT (payload->>'active')::boolean FROM gateway_outbox WHERE event_key=$1`, fmt.Sprintf("track:%s:%d", p, version)).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "down" || paused || version <= before || !active {
		t.Fatalf("state=%s paused=%v version=%d before=%d snapshot active=%v", state, paused, version, before, active)
	}
}

func TestRestoredSubscriptionWithChangedThresholdsClearsAlert(t *testing.T) {
	st, db := integrationStore(t)
	ctx := t.Context()
	u, p := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, u, p, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE subscriptions SET alert_state='up' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteSubscription(ctx, sub, u); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSubscription(ctx, u, p, 80, 150); err != nil {
		t.Fatal(err)
	}
	var state string
	var min, max float64
	if err = db.QueryRow(ctx, `SELECT alert_state,min_price,max_price FROM subscriptions WHERE id=$1`, sub).Scan(&state, &min, &max); err != nil {
		t.Fatal(err)
	}
	if state != "" || min != 80 || max != 150 {
		t.Fatalf("restored state=%q min=%v max=%v", state, min, max)
	}
}
