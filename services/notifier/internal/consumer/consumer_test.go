package consumer

import (
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
)

type ackRecorder struct{ ack, nack, requeue bool }

func (a *ackRecorder) Ack(_ uint64, _ bool) error          { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, r bool) error { a.nack = true; a.requeue = r; return nil }
func (a *ackRecorder) Reject(_ uint64, _ bool) error       { return nil }

type fakeMQ struct{}

func (fakeMQ) Consume(string, string) (<-chan amqp.Delivery, error) { panic("unused") }

type fakeSender struct {
	chat int64
	text string
	err  error
}

func (s *fakeSender) Send(id int64, text string) error { s.chat = id; s.text = text; return s.err }
func TestConsumerHandle(t *testing.T) {
	for _, tt := range []struct {
		name              string
		body              []byte
		wantAck, wantNack bool
		wantCalls         int
	}{{"telegram delivered", []byte(`{"channel":"telegram","target":"123","text":"hello"}`), true, false, 1}, {"invalid target dropped", []byte(`{"channel":"telegram","target":"x","text":"hello"}`), true, false, 0}, {"unknown channel dropped", []byte(`{"channel":"push","target":"123"}`), true, false, 0}, {"malformed dropped", []byte(`{`), false, true, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			s := &fakeSender{}
			c := New(fakeMQ{}, s)
			a := &ackRecorder{}
			c.handle(amqp.Delivery{Acknowledger: a, Body: tt.body})
			wantChat := int64(0)
			if tt.wantCalls > 0 {
				wantChat = 123
			}
			if a.ack != tt.wantAck || a.nack != tt.wantNack || s.chat != wantChat {
				t.Fatalf("ack/nack=%v/%v sender chat=%d", a.ack, a.nack, s.chat)
			}
			if tt.wantCalls == 1 && s.text != "hello" {
				t.Fatalf("sent text=%q", s.text)
			}
		})
	}
}
