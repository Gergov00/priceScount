package main

import (
	"context"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/notifier/internal/consumer"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

type notifierLifecycleMQ struct {
	started chan struct{}
	items   chan amqp.Delivery
}

func (m *notifierLifecycleMQ) Consume(string, string) (<-chan amqp.Delivery, error) {
	close(m.started)
	return m.items, nil
}
func (*notifierLifecycleMQ) Publish(context.Context, string, any) error { return nil }

type blockingLifecycleSender struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	finished  chan struct{}
}

func (s *blockingLifecycleSender) Send(ctx context.Context, _ int64, _ string) error {
	close(s.entered)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	close(s.finished)
	return ctx.Err()
}

func TestNotifierFxShutdownWaitsForInFlightDeliveryBeforeDependencyStop(t *testing.T) {
	sender := &blockingLifecycleSender{
		entered: make(chan struct{}), cancelled: make(chan struct{}),
		release: make(chan struct{}), finished: make(chan struct{}),
	}
	mq := &notifierLifecycleMQ{started: make(chan struct{}), items: make(chan amqp.Delivery, 1)}
	mq.items <- amqp.Delivery{Body: []byte(`{"channel":"telegram","target":"7","text":"test"}`)}
	c := consumer.New(mq, sender, 0)
	app := fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Supply(c),
		fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.Hook{OnStop: func(context.Context) error {
				select {
				case <-sender.finished:
					return nil
				default:
					return context.Canceled
				}
			}})
		}),
		fx.Invoke(runConsumer),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mq.started:
	case <-ctx.Done():
		t.Fatal("Notifier consumer did not start")
	}
	select {
	case <-sender.entered:
	case <-ctx.Done():
		t.Fatal("Notifier did not enter its controlled in-flight delivery")
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- app.Stop(ctx) }()
	select {
	case <-sender.cancelled:
	case <-ctx.Done():
		t.Fatal("Notifier shutdown did not cancel the in-flight delivery")
	}
	select {
	case err := <-stopResult:
		t.Fatalf("Fx shutdown returned before the in-flight delivery finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(sender.release)
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("Notifier Fx shutdown failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Notifier Fx shutdown did not finish after the delivery unwound")
	}
}
