package consumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	amqp "github.com/rabbitmq/amqp091-go"
)

type queueLifecycleMQ struct {
	mu             sync.Mutex
	called         map[string]bool
	siblingStarted chan struct{}
}

func (m *queueLifecycleMQ) ConsumeWithPrefetch(queue, _ string, _ int) (<-chan amqp.Delivery, error) {
	m.mu.Lock()
	m.called[queue] = true
	m.mu.Unlock()
	if queue == broker.QueueLookupTasks {
		return nil, errors.New("lookup queue unavailable")
	}
	close(m.siblingStarted)
	return make(chan amqp.Delivery), nil
}
func (*queueLifecycleMQ) Publish(context.Context, string, any) error { return nil }

func TestRunQueueFailureCancelsAndWaitsForSibling(t *testing.T) {
	mq := &queueLifecycleMQ{called: make(map[string]bool), siblingStarted: make(chan struct{})}
	c := New(mq, &fakeFetcher{})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case <-mq.siblingStarted:
	case <-time.After(time.Second):
		t.Fatal("sibling queue did not start")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded after queue setup failure")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel and wait for sibling")
	}
	mq.mu.Lock()
	defer mq.mu.Unlock()
	if !mq.called[broker.QueueLookupTasks] || !mq.called[broker.QueueScraperTasks] {
		t.Fatalf("queue setup calls = %#v", mq.called)
	}
}

type blockingFetcher struct{ entered chan struct{} }

func (f *blockingFetcher) FetchProduct(ctx context.Context, _ string) (*marketplace.Product, error) {
	close(f.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCancelledFetchDoesNotPublishOrAcknowledge(t *testing.T) {
	mq := &fakeMQ{}
	fetcher := &blockingFetcher{entered: make(chan struct{})}
	c := New(mq, fetcher)
	ack := &ackRecorder{}
	delivery := makeDelivery(t, contracts.LookupTask{TaskID: "task", LookupID: "lookup", URL: "url", Platform: "wb"}, ack)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.handle(ctx, delivery, broker.QueueLookupTasks); close(done) }()
	select {
	case <-fetcher.entered:
	case <-time.After(time.Second):
		t.Fatal("fetch did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled fetch did not return")
	}
	if len(mq.published) != 0 {
		t.Fatalf("published %d results after cancellation", len(mq.published))
	}
	if ack.ack || ack.nack {
		t.Fatalf("delivery acknowledged after cancellation: ack=%v nack=%v", ack.ack, ack.nack)
	}
}
