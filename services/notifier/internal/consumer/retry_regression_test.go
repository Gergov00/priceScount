//go:build regression

package consumer

import (
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
)

func TestTransientSendFailureMustRequeue(t *testing.T) {
	s := &fakeSender{err: errors.New("temporary network failure")}
	c := New(fakeMQ{}, s)
	a := &ackRecorder{}
	c.handle(amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"123","text":"hello"}`)})
	if !a.nack || !a.requeue {
		t.Fatal("transient Telegram failure must requeue notification")
	}
}
