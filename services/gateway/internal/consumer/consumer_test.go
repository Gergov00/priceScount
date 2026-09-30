package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
)

type ackRecorder struct {
	ack     bool
	nack    bool
	requeue bool
}

func (a *ackRecorder) Ack(_ uint64, _ bool) error          { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, r bool) error { a.nack = true; a.requeue = r; return nil }
func (a *ackRecorder) Reject(_ uint64, _ bool) error       { return nil }

type fakeMQ struct {
	published []any
	err       error
}

func (m *fakeMQ) Consume(string, string) (<-chan amqp.Delivery, error) { panic("unused") }
func (m *fakeMQ) Publish(_ context.Context, _ string, v any) error {
	m.published = append(m.published, v)
	return m.err
}

type fakeStore struct {
	failErr        error
	completeErr    error
	saveErr        error
	subsErr        error
	stateErr       error
	subs           []store.ProductSub
	saved          int
	states         []string
	failedID       string
	failureMsg     string
	completedID    string
	completedName  string
	completedPrice float64
}

func (s *fakeStore) FailLookup(_ context.Context, id, message string) error {
	s.failedID = id
	s.failureMsg = message
	return s.failErr
}
func (s *fakeStore) CompleteLookup(_ context.Context, id, name string, price float64) error {
	s.completedID = id
	s.completedName = name
	s.completedPrice = price
	return s.completeErr
}
func (s *fakeStore) SavePrice(context.Context, string, float64, string, time.Time) error {
	s.saved++
	return s.saveErr
}
func (s *fakeStore) ProductSubscriptions(context.Context, string) ([]store.ProductSub, error) {
	return s.subs, s.subsErr
}
func (s *fakeStore) SetAlertState(_ context.Context, id string, v string) error {
	s.states = append(s.states, v)
	if s.stateErr != nil {
		return s.stateErr
	}
	for i := range s.subs {
		if s.subs[i].SubID == id {
			s.subs[i].AlertState = v
		}
	}
	return nil
}
func delivery(t *testing.T, v any, a *ackRecorder) amqp.Delivery {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return amqp.Delivery{Acknowledger: a, Body: b}
}
func TestConsumerHandleLookup(t *testing.T) {
	for _, tc := range []struct {
		name               string
		result             contracts.PriceResult
		st                 fakeStore
		ack, nack, requeue bool
		wantFailedID       string
		wantFailureMsg     string
		wantCompletedID    string
		wantCompletedName  string
		wantCompletedPrice float64
	}{
		{name: "success", result: contracts.PriceResult{LookupID: "l", Success: true, Name: "Lamp", Price: 12.5}, ack: true, wantCompletedID: "l", wantCompletedName: "Lamp", wantCompletedPrice: 12.5},
		{name: "unsuccessful result records lookup failure", result: contracts.PriceResult{LookupID: "l", Error: "unsupported product"}, ack: true, wantFailedID: "l", wantFailureMsg: "unsupported product"},
		{name: "store error requeues", result: contracts.PriceResult{LookupID: "l", Success: true, Name: "Fan", Price: 7.25}, st: fakeStore{completeErr: errors.New("db")}, nack: true, requeue: true, wantCompletedID: "l", wantCompletedName: "Fan", wantCompletedPrice: 7.25},
		{name: "malformed drops", result: contracts.PriceResult{}, nack: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &ackRecorder{}
			s := &tc.st
			c := New(&fakeMQ{}, s)
			d := delivery(t, tc.result, a)
			if tc.name == "malformed drops" {
				d.Body = []byte("{")
			}
			c.handle(context.Background(), d)
			if a.ack != tc.ack || a.nack != tc.nack || a.requeue != tc.requeue {
				t.Fatalf("ack/nack/requeue = %v/%v/%v", a.ack, a.nack, a.requeue)
			}
			if s.completedID != tc.wantCompletedID || s.completedName != tc.wantCompletedName || s.completedPrice != tc.wantCompletedPrice {
				t.Fatalf("CompleteLookup(%q, %q, %v), want (%q, %q, %v)", s.completedID, s.completedName, s.completedPrice, tc.wantCompletedID, tc.wantCompletedName, tc.wantCompletedPrice)
			}
			if s.failedID != tc.wantFailedID || s.failureMsg != tc.wantFailureMsg {
				t.Fatalf("FailLookup(%q, %q), want (%q, %q)", s.failedID, s.failureMsg, tc.wantFailedID, tc.wantFailureMsg)
			}
		})
	}
}
func TestConsumerHandleMonitoringDeduplicatesAndFiltersForcedRecipient(t *testing.T) {
	s := &fakeStore{subs: []store.ProductSub{{SubID: "a", ChatID: 11, ProductName: "P", ProductURL: "u", MinPrice: 100, MaxPrice: 200}, {SubID: "b", ChatID: 22, ProductName: "P", ProductURL: "u", MinPrice: 100, MaxPrice: 200}}}
	m := &fakeMQ{}
	c := New(m, s)
	a := &ackRecorder{}
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true, ProductID: "p", Price: 80, Currency: "RUB"}, a))
	if !a.ack || len(m.published) != 2 || len(s.states) != 2 {
		t.Fatalf("first alert: ack=%v messages=%d states=%v", a.ack, len(m.published), s.states)
	}
	a = &ackRecorder{}
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true, ProductID: "p", Price: 79, Currency: "RUB"}, a))
	if len(m.published) != 2 {
		t.Fatalf("same alert zone duplicated: %d messages", len(m.published))
	}
	a = &ackRecorder{}
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true, ProductID: "p", Price: 75, Currency: "RUB", Force: true, ChatID: 22}, a))
	if len(m.published) != 3 {
		t.Fatalf("forced check published %d total messages, want 3", len(m.published))
	}
	task := m.published[2].(contracts.NotifyTask)
	if task.Target != "22" || task.Direction != "" {
		t.Fatalf("forced notification = %#v", task)
	}
}
func TestConsumerStoreFailureRequeues(t *testing.T) {
	a := &ackRecorder{}
	c := New(&fakeMQ{}, &fakeStore{saveErr: errors.New("db")})
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true, ProductID: "p"}, a))
	if !a.nack || !a.requeue || a.ack {
		t.Fatalf("store failure ack/nack/requeue=%v/%v/%v", a.ack, a.nack, a.requeue)
	}
}
