//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	priceconsumer "github.com/Gergov00/pricescount/services/gateway/internal/consumer"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	sharedoutbox "github.com/Gergov00/pricescount/shared/pkg/outbox"
)

// testRoutingBroker exercises the real broker with test-owned queue names.
type testRoutingBroker struct {
	conn   *broker.Connection
	queues map[string]string
}

func (b *testRoutingBroker) Publish(ctx context.Context, queue string, v any) error {
	return b.conn.Publish(ctx, b.queues[queue], v)
}
func (b *testRoutingBroker) Consume(queue, tag string) (<-chan amqp.Delivery, error) {
	return b.conn.Consume(b.queues[queue], tag)
}

func integrationGateway(t *testing.T) (*store.Store, *testRoutingBroker, *pgxpool.Pool) {
	t.Helper()
	dsn, rabbitURL := os.Getenv("TEST_POSTGRES_DSN"), os.Getenv("TEST_RABBITMQ_URL")
	if dsn == "" || rabbitURL == "" {
		t.Fatal("TEST_POSTGRES_DSN and TEST_RABBITMQ_URL are required")
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
	conn, err := broker.NewConnection(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	routing := &testRoutingBroker{conn: conn, queues: map[string]string{}}
	for _, queue := range []string{broker.QueueLookupTasks, broker.QueueTrackRequests, broker.QueuePriceResults, broker.QueueNotifyTasks} {
		name := "pricescount.integration." + uuid.NewString() + "." + queue
		routing.queues[queue] = name
		if err := conn.DeclareQueue(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, err := amqp.Dial(rabbitURL)
			if err != nil {
				t.Errorf("queue cleanup dial: %v", err)
				return
			}
			defer cleanup.Close()
			ch, err := cleanup.Channel()
			if err != nil {
				t.Errorf("queue cleanup channel: %v", err)
				return
			}
			defer ch.Close()
			if _, err := ch.QueueDelete(name, false, false, false); err != nil {
				t.Errorf("delete queue: %v", err)
			}
		})
	}
	return store.New(db), routing, db
}

func integrationDelivery(t *testing.T, deliveries <-chan amqp.Delivery) amqp.Delivery {
	t.Helper()
	select {
	case d, ok := <-deliveries:
		if !ok {
			t.Fatal("delivery channel closed")
		}
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for pipeline delivery")
		return amqp.Delivery{}
	}
}

func TestHTTPSubscriptionsToPriceAlertsIntegration(t *testing.T) {
	st, mq, db := integrationGateway(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Auth("test-secret"), BodyLimit(1024))
	New(st).Register(router)
	workerCtx, stopWorker := context.WithCancel(t.Context())
	workerDone := make(chan error, 1)
	go func() { workerDone <- sharedoutbox.New(st, mq).Run(workerCtx) }()
	t.Cleanup(func() {
		stopWorker()
		select {
		case err := <-workerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("outbox worker shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("outbox worker did not stop")
		}
	})
	request := func(method, path, body, token string, wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, wantStatus, w.Body.String())
		}
		return w
	}
	lookupMessages, err := mq.Consume(broker.QueueLookupTasks, "test-lookup")
	if err != nil {
		t.Fatal(err)
	}
	trackMessages, err := mq.Consume(broker.QueueTrackRequests, "test-track")
	if err != nil {
		t.Fatal(err)
	}
	notifyMessages, err := mq.Consume(broker.QueueNotifyTasks, "test-notify")
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/subscriptions?chat_id=101", "", "wrong", http.StatusUnauthorized)
	w := request(http.MethodPost, "/lookup", `{"url":"https://www.wildberries.ru/catalog/123/detail.aspx?extra=1"}`, "test-secret", http.StatusAccepted)
	var lookupResponse struct {
		LookupID string `json:"lookup_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lookupResponse); err != nil {
		t.Fatal(err)
	}
	d := integrationDelivery(t, lookupMessages)
	var task contracts.LookupTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		t.Fatal(err)
	}
	if task.LookupID != lookupResponse.LookupID || task.URL != "https://www.wildberries.ru/catalog/123/detail.aspx" || task.Platform != "wb" || task.TaskID == "" {
		t.Fatalf("lookup task=%+v", task)
	}
	if err := d.Ack(false); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/lookup/"+lookupResponse.LookupID, "", "test-secret", http.StatusAccepted)
	// The external scraper boundary is controlled; HTTP/store/broker are real.
	if err := st.CompleteLookup(t.Context(), task.LookupID, "Телефон", 150); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/lookup/"+task.LookupID, "", "test-secret", http.StatusOK)
	createJSON := `{"chat_id":101,"lookup_id":"` + task.LookupID + `","min_price":100,"max_price":200}`
	w = request(http.MethodPost, "/subscriptions", createJSON, "test-secret", http.StatusOK)
	var created struct {
		SubscriptionID string `json:"subscription_id"`
		ProductID      string `json:"product_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	d = integrationDelivery(t, trackMessages)
	var track contracts.TrackRequest
	if err := json.Unmarshal(d.Body, &track); err != nil {
		t.Fatal(err)
	}
	if track.Action != "set_state" || !track.Active || track.Version == 0 || track.ProductID != created.ProductID || track.URL != task.URL || track.IntervalHours != 1 {
		t.Fatalf("track=%+v", track)
	}
	if err := d.Ack(false); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/subscriptions/"+created.SubscriptionID+"/history?chat_id=202", "", "test-secret", http.StatusNotFound)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- priceconsumer.New(mq, st).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("consumer shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("consumer did not stop")
		}
	})
	result := contracts.PriceResult{TaskID: uuid.NewString(), ProductID: created.ProductID, URL: task.URL, Price: 250, Currency: "RUB", ScrapedAt: time.Now().UTC(), Success: true}
	if err := mq.Publish(t.Context(), broker.QueuePriceResults, result); err != nil {
		t.Fatal(err)
	}
	d = integrationDelivery(t, notifyMessages)
	var notify contracts.NotifyTask
	if err := json.Unmarshal(d.Body, &notify); err != nil {
		t.Fatal(err)
	}
	if notify.Channel != "telegram" || notify.Target != "101" || notify.Direction != "up" || !strings.Contains(notify.Text, "Телефон") || !strings.Contains(notify.Text, "250") {
		t.Fatalf("notification=%+v", notify)
	}
	if err := d.Ack(false); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodGet, "/subscriptions/"+created.SubscriptionID+"/history?chat_id=101", "", "test-secret", http.StatusOK)
	var history []store.PricePoint
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Price != 250 || history[0].Currency != "RUB" {
		t.Fatalf("history=%+v", history)
	}
	request(http.MethodPatch, "/subscriptions/"+created.SubscriptionID, `{"chat_id":101,"action":"pause"}`, "test-secret", http.StatusNoContent)
	d = integrationDelivery(t, trackMessages)
	if err := json.Unmarshal(d.Body, &track); err != nil {
		t.Fatal(err)
	}
	if track.Action != "set_state" || track.Active || track.ProductID != created.ProductID || track.Version == 0 {
		var events []string
		rows, queryErr := db.Query(t.Context(), `SELECT payload::text FROM gateway_outbox WHERE event_key LIKE 'track:%' ORDER BY created_at`)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var e string
				_ = rows.Scan(&e)
				events = append(events, e)
			}
		}
		t.Fatalf("pause track=%+v queued=%v", track, events)
	}
	if err := d.Ack(false); err != nil {
		t.Fatal(err)
	}
}
