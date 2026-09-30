package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientMethodsSendExpectedHTTPRequests(t *testing.T) {
	type seenRequest struct{ method, path, token, contentType, body string }
	var seen []seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		seen = append(seen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Get("X-Internal-Token"), r.Header.Get("Content-Type"), string(requestBody)})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/lookup":
			_, _ = fmt.Fprint(w, `{"lookup_id":"lk-1"}`)
		case "/subscriptions":
			if r.Method == http.MethodPost {
				_, _ = fmt.Fprint(w, `{"subscription_id":"sub-1","product_id":"p-1"}`)
			} else {
				_, _ = fmt.Fprint(w, `[]`)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	c := New(server.URL, "secret")
	ctx := context.Background()
	if id, err := c.StartLookup(ctx, "https://www.wildberries.ru/catalog/123"); err != nil || id != "lk-1" {
		t.Fatalf("StartLookup = %q, %v", id, err)
	}
	created, err := c.CreateSubscription(ctx, CreateSubscriptionRequest{ChatID: 8, LookupID: "lk-1", MinPrice: 10, MaxPrice: 20})
	if err != nil || created.SubscriptionID != "sub-1" || created.ProductID != "p-1" {
		t.Fatalf("CreateSubscription = %#v, %v", created, err)
	}
	if _, err := c.ListSubscriptions(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if err := c.PauseSubscription(ctx, 8, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.ResumeSubscription(ctx, 8, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.EditSubscription(ctx, 8, "sub-1", 11, 21); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSubscription(ctx, 8, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.ForceCheck(ctx, 8, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetHistory(ctx, 8, "sub-1"); err != nil {
		t.Fatal(err)
	}
	wantRequests := []struct{ method, path string }{
		{"POST", "/lookup"},
		{"POST", "/subscriptions"},
		{"GET", "/subscriptions?chat_id=8"},
		{"PATCH", "/subscriptions/sub-1"},
		{"PATCH", "/subscriptions/sub-1"},
		{"PATCH", "/subscriptions/sub-1"},
		{"DELETE", "/subscriptions/sub-1"},
		{"POST", "/subscriptions/sub-1/check"},
		{"GET", "/subscriptions/sub-1/history?chat_id=8"},
	}
	if len(seen) != len(wantRequests) {
		t.Fatalf("got %d requests, want %d", len(seen), len(wantRequests))
	}
	for i, req := range seen {
		if req.method != wantRequests[i].method || req.path != wantRequests[i].path || req.token != "secret" {
			t.Errorf("request %d = %#v; want method %s, path %s, and auth token", i, req, wantRequests[i].method, wantRequests[i].path)
		}
	}
	for i, want := range map[int]map[string]any{
		0: {"url": "https://www.wildberries.ru/catalog/123"},
		1: {"chat_id": float64(8), "lookup_id": "lk-1", "min_price": float64(10), "max_price": float64(20)},
		3: {"chat_id": float64(8), "action": "pause"},
		4: {"chat_id": float64(8), "action": "resume"},
		5: {"chat_id": float64(8), "action": "edit", "min_price": float64(11), "max_price": float64(21)},
		6: {"chat_id": float64(8)},
		7: {"chat_id": float64(8)},
	} {
		if seen[i].contentType != "application/json" {
			t.Errorf("request %d content type = %q, want application/json", i, seen[i].contentType)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(seen[i].body), &got); err != nil {
			t.Errorf("request %d body is not JSON: %v", i, err)
		} else if !reflect.DeepEqual(got, want) {
			t.Errorf("request %d JSON = %#v, want %#v", i, got, want)
		}
	}
	if seen[2].body != "" || seen[8].body != "" {
		t.Errorf("GET requests carried bodies: list=%q history=%q", seen[2].body, seen[8].body)
	}
}

func TestClientReturnsGatewayErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid bounds"}`)
	}))
	defer server.Close()
	err := New(server.URL, "t").PauseSubscription(context.Background(), 8, "sub")
	if err == nil || !strings.Contains(err.Error(), "gateway error 400: invalid bounds") {
		t.Fatalf("error = %v, want status and gateway message", err)
	}
}

func TestClientReturnsGatewayErrorWithoutMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	_, err := New(server.URL, "t").ListSubscriptions(context.Background(), 8)
	if err == nil || !strings.Contains(err.Error(), "gateway error 503") {
		t.Fatalf("error = %v, want status", err)
	}
}

func TestClientHonorsCanceledContext(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := New(server.URL, "t").ListSubscriptions(ctx, 1); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive request")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("request succeeded after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not return after cancellation")
	}
}

func TestPollLookupReturnsTerminalStatuses(t *testing.T) {
	for _, status := range []string{"done", "failed"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(LookupResult{Status: status, Name: "Widget", Price: 42, URL: "https://example.test/p"})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := New(server.URL, "t").PollLookup(ctx, "lk")
			if err != nil || got.Status != status || got.Price != 42 {
				t.Fatalf("PollLookup = %#v, %v", got, err)
			}
		})
	}
}

func TestPollLookupStopsWhenContextExpires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `{"status":"pending"}`) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := New(server.URL, "t").PollLookup(ctx, "lk")
	if err == nil || !strings.Contains(err.Error(), "lookup timed out") {
		t.Fatalf("PollLookup error = %v", err)
	}
}
