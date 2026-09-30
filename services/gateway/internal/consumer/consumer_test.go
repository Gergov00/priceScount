package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
)

type ackRecorder struct{ ack, nack, requeue bool }

func (a *ackRecorder) Ack(uint64, bool) error              { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, r bool) error { a.nack = true; a.requeue = r; return nil }
func (a *ackRecorder) Reject(uint64, bool) error           { return nil }

type fakeStore struct {
	result contracts.PriceResult
	err    error
	calls  int
}

func (s *fakeStore) ProcessPriceResult(_ context.Context, r contracts.PriceResult) error {
	s.result = r
	s.calls++
	return s.err
}
func delivery(t *testing.T, v any, a *ackRecorder) amqp.Delivery {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return amqp.Delivery{Acknowledger: a, Body: b}
}
func TestConsumerAckOnlyAfterAtomicStoreSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		err                            error
		wantAck, wantNack, wantRequeue bool
	}{{"success", nil, true, false, false}, {"transient database failure", errors.New("db"), false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeStore{err: tc.err}
			a := &ackRecorder{}
			c := New(nil, s)
			c.handle(context.Background(), delivery(t, contracts.PriceResult{TaskID: "d5f77b4d-f7db-41df-bf36-bbec28430284", LookupID: "lookup", Success: true}, a))
			if a.ack != tc.wantAck || a.nack != tc.wantNack || a.requeue != tc.wantRequeue || s.calls != 1 || s.result.LookupID != "lookup" {
				t.Fatalf("ack=%v nack=%v requeue=%v calls=%d result=%+v", a.ack, a.nack, a.requeue, s.calls, s.result)
			}
		})
	}
}
func TestConsumerDropsResultMissingTaskIDWithoutRequeue(t *testing.T) {
	s := &fakeStore{err: store.ErrInvalidResult}
	a := &ackRecorder{}
	c := New(nil, s)
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true}, a))
	if a.ack || !a.nack || a.requeue {
		t.Fatalf("ack=%v nack=%v requeue=%v", a.ack, a.nack, a.requeue)
	}
}
func TestConsumerDropsMalformedJSON(t *testing.T) {
	a := &ackRecorder{}
	c := New(nil, &fakeStore{})
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte("{")})
	if a.ack || !a.nack || a.requeue {
		t.Fatalf("ack=%v nack=%v requeue=%v", a.ack, a.nack, a.requeue)
	}
}
