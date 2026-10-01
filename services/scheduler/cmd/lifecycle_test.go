package main

import (
	"context"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/scheduler/internal/consumer"
	"github.com/Gergov00/pricescount/services/scheduler/internal/scheduler"
	"github.com/Gergov00/pricescount/shared/pkg/outbox"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

type schedulerLifecycleMQ struct {
	started chan struct{}
	items   chan amqp.Delivery
}

func (m *schedulerLifecycleMQ) Consume(string, string) (<-chan amqp.Delivery, error) {
	close(m.started)
	return m.items, nil
}

type schedulerLifecycleStore struct{ dispatched chan struct{} }

func (s *schedulerLifecycleStore) EnqueueDue(context.Context) (int, error) {
	select {
	case s.dispatched <- struct{}{}:
	default:
	}
	return 0, nil
}

type emptyOutboxStore struct{}

func (emptyOutboxStore) Claim(context.Context, int, time.Duration) ([]outbox.Event, error) {
	return nil, nil
}
func (emptyOutboxStore) MarkPublished(context.Context, string, string) error { return nil }
func (emptyOutboxStore) Retry(context.Context, string, string, string, time.Duration) error {
	return nil
}

type unusedPublisher struct{}

func (unusedPublisher) Publish(context.Context, string, any) error { return nil }

func TestConsumerSchedulerAndOutboxHooksStopWithControlledBroker(t *testing.T) {
	mq := &schedulerLifecycleMQ{started: make(chan struct{}), items: make(chan amqp.Delivery)}
	c := consumer.New(mq, nil)
	scheduleStore := &schedulerLifecycleStore{dispatched: make(chan struct{}, 1)}
	sc := scheduler.New(scheduleStore, time.Hour)
	worker := outbox.New(emptyOutboxStore{}, unusedPublisher{})
	app := fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Supply(c, sc, worker),
		fx.Invoke(runConsumer, runScheduler, runOutboxWorker),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mq.started:
	case <-ctx.Done():
		t.Fatal("Scheduler consumer did not start")
	}
	select {
	case <-scheduleStore.dispatched:
	case <-ctx.Done():
		t.Fatal("Scheduler did not dispatch its controlled initial tick")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Scheduler hooks did not stop before their controlled dependencies: %v", err)
	}
}
