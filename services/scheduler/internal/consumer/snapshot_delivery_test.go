package consumer

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestSnapshotStoreFailureRemainsDeliverable(t *testing.T) {
	a := &ackRecorder{}
	s := &fakeStore{err: errors.New("temporary database outage")}
	c := New(&fakeMQ{}, s)
	body := []byte(`{"action":"set_state","product_id":"product","url":"https://item","platform":"wb","version":2,"active":true}`)
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: body})
	if !a.nack || !a.requeue || a.ack {
		t.Fatal("temporary snapshot DB failure must be requeued for delivery")
	}
	if s.snapshot != 1 || s.force != 0 {
		t.Fatalf("store calls snapshot=%d force=%d", s.snapshot, s.force)
	}
}
