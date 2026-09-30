//go:build integration

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/google/uuid"
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
