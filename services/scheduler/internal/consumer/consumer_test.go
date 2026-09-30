package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
)

type ackRecorder struct{ ack, nack, requeue bool }

func (a *ackRecorder) Ack(_ uint64, _ bool) error          { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, r bool) error { a.nack = true; a.requeue = r; return nil }
func (a *ackRecorder) Reject(_ uint64, _ bool) error       { return nil }

type fakeMQ struct {
	published []any
	err       error
}

func (m *fakeMQ) Consume(string, string) (<-chan amqp.Delivery, error) { panic("unused") }
func (m *fakeMQ) Publish(_ context.Context, q string, v any) error {
	m.published = append(m.published, v)
	return m.err
}

type fakeStore struct {
	err      error
	actions  []string
	active   bool
	interval int
}

func (s *fakeStore) Add(_ context.Context, _, _, _ string, i int) error {
	s.actions = append(s.actions, "add")
	s.interval = i
	return s.err
}
func (s *fakeStore) SetActive(_ context.Context, _ string, a bool) error {
	s.actions = append(s.actions, "pause")
	s.active = a
	return s.err
}
func (s *fakeStore) SetNextCheck(context.Context, string) error {
	s.actions = append(s.actions, "resume")
	return s.err
}
func (s *fakeStore) Delete(context.Context, string) error {
	s.actions = append(s.actions, "delete")
	return s.err
}
func (s *fakeStore) AdvanceNextCheck(context.Context, string) error {
	s.actions = append(s.actions, "advance")
	return s.err
}
func makeDelivery(t *testing.T, v any, a *ackRecorder) amqp.Delivery {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return amqp.Delivery{Acknowledger: a, Body: b}
}
func TestConsumerHandle(t *testing.T) {
	tests := []struct {
		name                           string
		body                           []byte
		storeErr, publishErr           error
		wantAck, wantNack, wantRequeue bool
	}{
		{name: "success", body: []byte(`{"action":"add","product_id":"p","url":"u","platform":"wb"}`), wantAck: true},
		{name: "malformed is dropped", body: []byte(`{`), wantNack: true},
		{name: "unknown action is dropped", body: []byte(`{"action":"unknown"}`), wantNack: true},
		{name: "temporary store failure requeues", body: []byte(`{"action":"delete","url":"u"}`), storeErr: errors.New("db"), wantNack: true, wantRequeue: true},
		{name: "force publish failure requeues", body: []byte(`{"action":"force","product_id":"p","url":"u","platform":"wb"}`), publishErr: errors.New("mq"), wantNack: true, wantRequeue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &fakeMQ{err: tt.publishErr}
			s := &fakeStore{err: tt.storeErr}
			c := New(m, s)
			a := &ackRecorder{}
			d := amqp.Delivery{Acknowledger: a, Body: tt.body}
			c.handle(context.Background(), d)
			if a.ack != tt.wantAck || a.nack != tt.wantNack || a.requeue != tt.wantRequeue {
				t.Fatalf("ack/nack/requeue %v/%v/%v", a.ack, a.nack, a.requeue)
			}
			if tt.name == "success" && s.interval != 1 {
				t.Fatalf("default interval = %d, want 1", s.interval)
			}
			if tt.name == "force publish failure requeues" && len(m.published) != 1 {
				t.Fatalf("published tasks=%d", len(m.published))
			}
		})
	}
}
func TestConsumerForceRequestCarriesRequesterAndAdvancesSchedule(t *testing.T) {
	m := &fakeMQ{}
	s := &fakeStore{}
	c := New(m, s)
	req := contracts.TrackRequest{Action: "force", ProductID: "p", URL: "u", Platform: "wb", ChatID: 42}
	if err := c.handleForce(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	task, ok := m.published[0].(contracts.ScraperTask)
	if !ok || !task.Force || task.ChatID != 42 || task.ProductID != "p" {
		t.Fatalf("force task = %#v", m.published[0])
	}
	if len(s.actions) != 1 || s.actions[0] != "advance" {
		t.Fatalf("store effects = %v", s.actions)
	}
	_ = broker.QueueScraperTasks
}
