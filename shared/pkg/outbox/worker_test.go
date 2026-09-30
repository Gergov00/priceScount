package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type leaseTestStore struct {
	mu        sync.Mutex
	events    []Event
	leases    map[string]string
	expires   map[string]time.Duration
	clock     *testClock
	leaseSeq  int
	completed map[string]bool
	marked    chan string
}

func (s *leaseTestStore) Claim(ctx context.Context, limit int, lease time.Duration) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := make([]Event, 0, limit)
	now := s.clock.Now()
	for _, event := range s.events {
		if s.completed[event.ID] {
			continue
		}
		if s.leases[event.ID] != "" && s.expires[event.ID] > now {
			continue
		}
		s.leaseSeq++
		event.LeaseToken = fmt.Sprintf("lease-%d", s.leaseSeq)
		s.leases[event.ID] = event.LeaseToken
		s.expires[event.ID] = now + lease
		claimed = append(claimed, event)
		if len(claimed) == limit {
			break
		}
	}
	return claimed, nil
}

func (s *leaseTestStore) MarkPublished(_ context.Context, id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases[id] != token {
		return fmt.Errorf("unexpected lease for %s", id)
	}
	delete(s.leases, id)
	delete(s.expires, id)
	s.completed[id] = true
	s.marked <- id
	return nil
}

func (s *leaseTestStore) Retry(_ context.Context, id, token, _ string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases[id] == token {
		delete(s.leases, id)
		delete(s.expires, id)
	}
	return nil
}

type testClock struct {
	mu  sync.Mutex
	now time.Duration
}

func (c *testClock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	c.mu.Unlock()
}

type heldPublisher struct {
	mu            sync.Mutex
	eighthStarted chan struct{}
	releaseEighth chan struct{}
	concurrentDup chan string
	clock         *testClock
	active        map[string]int
	calls         map[string]int
}

func (p *heldPublisher) Publish(ctx context.Context, _ string, value any) error {
	payload, ok := value.(json.RawMessage)
	if !ok {
		return fmt.Errorf("unexpected payload type %T", value)
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return err
	}
	p.mu.Lock()
	p.calls[body.ID]++
	p.active[body.ID]++
	if p.active[body.ID] > 1 {
		select {
		case p.concurrentDup <- body.ID:
		default:
		}
	}
	if body.ID == "event-8" {
		select {
		case p.eighthStarted <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active[body.ID]--
		p.mu.Unlock()
	}()
	if body.ID == "event-8" {
		select {
		case <-p.releaseEighth:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var index int
	if _, err := fmt.Sscanf(body.ID, "event-%d", &index); err == nil && index < 8 {
		// Model a slow but successful confirm that remains within the worker's
		// 10-second publish timeout while the old batch lease continues aging.
		p.clock.Advance(9 * time.Second)
	}
	return nil
}

func (p *heldPublisher) isActive(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active[id] > 0
}

func TestWorkerClaimsNextEventOnlyWhenReadyToPublish(t *testing.T) {
	clock := &testClock{}
	events := make([]Event, 9)
	for i := range events {
		id := fmt.Sprintf("event-%d", i+1)
		events[i] = Event{ID: id, Queue: "q", Payload: json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))}
	}
	store := &leaseTestStore{
		events:    events,
		leases:    make(map[string]string),
		expires:   make(map[string]time.Duration),
		clock:     clock,
		completed: make(map[string]bool),
		marked:    make(chan string, 9),
	}
	publisher := &heldPublisher{
		eighthStarted: make(chan struct{}, 1),
		releaseEighth: make(chan struct{}),
		concurrentDup: make(chan string, 1),
		clock:         clock,
		active:        make(map[string]int),
		calls:         make(map[string]int),
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- New(store, publisher).Run(firstCtx) }()
	defer cancelFirst()

	select {
	case <-publisher.eighthStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("eighth event did not begin publishing")
	}
	for i := 1; i <= 7; i++ {
		select {
		case id := <-store.marked:
			if want := fmt.Sprintf("event-%d", i); id != want {
				t.Fatalf("early event marked %q, want %q", id, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("early confirm %d was not recorded", i)
		}
	}
	if clock.Now() < leaseDuration {
		t.Fatalf("controlled clock=%s, want at least one old 60-second batch lease elapsed", clock.Now())
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- New(store, publisher).Run(secondCtx) }()
	defer cancelSecond()

	var failure string
	select {
	case id := <-publisher.concurrentDup:
		failure = fmt.Sprintf("same outbox event published concurrently: %q", id)
	case id := <-store.marked:
		if id != "event-9" {
			failure = fmt.Sprintf("second worker marked %q, want unclaimed event-9", id)
		}
	case <-time.After(2 * time.Second):
		failure = "second worker neither claimed the next unclaimed event nor reported a duplicate"
	}
	if !publisher.isActive("event-8") && failure == "" {
		failure = "event-8 publish completed before it was released"
	}

	if failure != "" {
		cancelFirst()
		cancelSecond()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Errorf("first worker Run() error=%v, want canceled", err)
		}
		if err := <-secondDone; !errors.Is(err, context.Canceled) {
			t.Errorf("second worker Run() error=%v, want canceled", err)
		}
		close(publisher.releaseEighth)
		t.Fatal(failure)
	}

	close(publisher.releaseEighth)
	cancelSecond()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second worker Run() error=%v, want canceled", err)
	}
	select {
	case id := <-store.marked:
		if id != "event-8" {
			failure = fmt.Sprintf("first worker marked %q after release, want event-8", id)
		}
	case <-time.After(2 * time.Second):
		failure = "first worker did not finish its held event after release"
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first worker Run() error=%v, want canceled", err)
	}
	if failure != "" {
		t.Fatal(failure)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.calls["event-8"] != 1 || publisher.calls["event-9"] != 1 {
		t.Fatalf("publish calls for held/next events = (%d,%d), want (1,1)", publisher.calls["event-8"], publisher.calls["event-9"])
	}
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
