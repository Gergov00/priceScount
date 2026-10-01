package main

import (
	"context"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/gateway/internal/consumer"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

type lifecycleMQ struct {
	started chan struct{}
	items   chan amqp.Delivery
}

func (m *lifecycleMQ) Consume(string, string) (<-chan amqp.Delivery, error) {
	close(m.started)
	return m.items, nil
}

func TestConsumerHookStopsWithControlledBroker(t *testing.T) {
	mq := &lifecycleMQ{started: make(chan struct{}), items: make(chan amqp.Delivery)}
	c := consumer.New(mq, nil)
	app := fx.New(fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }), fx.Supply(c), fx.Invoke(runConsumer))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mq.started:
	case <-ctx.Done():
		t.Fatal("Gateway consumer did not start")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Gateway consumer did not stop before its controlled dependencies: %v", err)
	}
}
