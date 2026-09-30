package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type testStore struct {
	mu      sync.Mutex
	events  []Event
	retried []retryCall
	marked  []string
	claimed bool
}
type retryCall struct{ id, token, message string }

func (s *testStore) Claim(context.Context, int, time.Duration) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return s.events, nil
}
func (s *testStore) MarkPublished(_ context.Context, id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, id+":"+token)
	return nil
}
func (s *testStore) Retry(_ context.Context, id, token, message string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, retryCall{id, token, message})
	s.claimed = false
	return nil
}

type testPublisher struct {
	failures int
	calls    int
}

func (p *testPublisher) Publish(context.Context, string, any) error {
	p.calls++
	if p.failures > 0 {
		p.failures--
		return errors.New("temporary publish failure")
	}
	return nil
}

func TestWorkerRetriesThenPublishes(t *testing.T) {
	store := &testStore{events: []Event{{ID: "event", LeaseToken: "lease-1", Queue: "q", Payload: json.RawMessage(`{"value":1}`), Attempts: 1}}}
	publisher := &testPublisher{failures: 1}
	worker := New(store, publisher)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		marked := len(store.marked) > 0
		store.mu.Unlock()
		if marked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v, want canceled", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.retried) != 1 {
		t.Fatalf("Retry calls=%d, want 1", len(store.retried))
	}
	if len(store.marked) != 1 || store.marked[0] != "event:lease-1" {
		t.Fatalf("MarkPublished=%v, want same lease", store.marked)
	}
	if publisher.calls != 2 {
		t.Fatalf("Publish calls=%d, want 2", publisher.calls)
	}
}

func TestWorkerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(&testStore{}, &testPublisher{}).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v, want canceled", err)
	}
}
