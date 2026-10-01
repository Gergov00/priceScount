package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
)

type ackRecorder struct{ ack, nack, requeue bool }

func (a *ackRecorder) Ack(uint64, bool) error { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, requeue bool) error {
	a.nack = true
	a.requeue = requeue
	return nil
}
func (a *ackRecorder) Reject(uint64, bool) error { return nil }

type fakeMQ struct{ deliveries <-chan amqp.Delivery }

func (m *fakeMQ) Consume(string, string) (<-chan amqp.Delivery, error) { return m.deliveries, nil }

type fakeStore struct {
	err             error
	snapshot, force int
	invalid         bool
}

func (s *fakeStore) ApplySnapshot(context.Context, contracts.TrackRequest) error {
	s.snapshot++
	if s.invalid {
		return store.ErrInvalidRequest
	}
	return s.err
}
func (s *fakeStore) EnqueueForce(context.Context, contracts.TrackRequest) error {
	s.force++
	if s.invalid {
		return store.ErrInvalidRequest
	}
	return s.err
}

func TestConsumerHandleCommitAndErrorClassification(t *testing.T) {
	tests := []struct {
		name, body              string
		err                     error
		ack, nack, requeue      bool
		wantSnapshot, wantForce int
	}{{name: "snapshot committed", body: `{"action":"set_state"}`, ack: true, wantSnapshot: 1}, {name: "force committed", body: `{"action":"force"}`, ack: true, wantForce: 1}, {name: "malformed dropped", body: `{`, nack: true}, {name: "unknown action dropped", body: `{"action":"add"}`, nack: true}, {name: "invalid command dropped", body: `{"action":"set_state"}`, err: store.ErrInvalidRequest, nack: true, wantSnapshot: 1}, {name: "database failure requeued", body: `{"action":"force"}`, err: errors.New("database unavailable"), nack: true, requeue: true, wantForce: 1}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req contracts.TrackRequest
			if tt.body != "{" {
				_ = json.Unmarshal([]byte(tt.body), &req)
			}
			st := &fakeStore{err: tt.err, invalid: tt.name == "invalid command dropped"}
			a := &ackRecorder{}
			raw := []byte(tt.body)
			c := New(&fakeMQ{}, st)
			c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: raw})
			if a.ack != tt.ack || a.nack != tt.nack || a.requeue != tt.requeue {
				t.Fatalf("ack/nack/requeue=%v/%v/%v", a.ack, a.nack, a.requeue)
			}
			if st.snapshot != tt.wantSnapshot || st.force != tt.wantForce {
				t.Fatalf("store calls snapshot=%d force=%d", st.snapshot, st.force)
			}
		})
	}
}
