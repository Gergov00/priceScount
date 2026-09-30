//go:build regression

package consumer

import (
	"context"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
)

func TestDeleteStoreFailureRemainsDeliverable(t *testing.T) {
	a := &ackRecorder{}
	s := &fakeStore{err: errors.New("temporary database outage")}
	c := New(&fakeMQ{}, s)
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte(`{"action":"delete","url":"https://item"}`)})
	if !a.nack || !a.requeue {
		t.Fatal("temporary DELETE failure must be requeued for delivery")
	}
}
