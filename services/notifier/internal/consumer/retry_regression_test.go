package consumer

import (
	"context"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
)

func TestTransientSendFailureMustRequeue(t *testing.T) {
	s := &fakeSender{err: errors.New("temporary network failure")}
	mq := &fakeMQ{}
	c := New(mq, s, 0)
	a := &ackRecorder{}
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"123","text":"hello"}`)})
	if mq.queue != "notify.retry" || !a.ack || a.nack {
		t.Fatal("transient Telegram failure must transfer to durable retry queue before Ack")
	}
}
