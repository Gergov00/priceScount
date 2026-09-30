package main

import (
	"context"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/extractor/internal/consumer"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

type extractorLifecycleMQ struct {
	started chan struct{}
	items   chan amqp.Delivery
}

func (m *extractorLifecycleMQ) ConsumeWithPrefetch(string, string, int) (<-chan amqp.Delivery, error) {
	m.started <- struct{}{}
	return m.items, nil
}
func (*extractorLifecycleMQ) Publish(context.Context, string, any) error { return nil }

type unusedFetcher struct{}

func (unusedFetcher) FetchProduct(context.Context, string) (*marketplace.Product, error) {
	return nil, nil
}

func TestExtractorConsumerHookStopsBothControlledQueueWorkers(t *testing.T) {
	mq := &extractorLifecycleMQ{started: make(chan struct{}, 2), items: make(chan amqp.Delivery)}
	c := consumer.New(mq, unusedFetcher{})
	app := fx.New(fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }), fx.Supply(c), fx.Invoke(runConsumer))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-mq.started:
		case <-ctx.Done():
			t.Fatal("Extractor did not start both controlled queue workers")
		}
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Extractor consumer did not stop both workers before browser/broker cleanup: %v", err)
	}
}
