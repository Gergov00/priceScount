package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
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

func (m *fakeMQ) ConsumeWithPrefetch(string, string, int) (<-chan amqp.Delivery, error) {
	panic("unused")
}
func (m *fakeMQ) Publish(_ context.Context, q string, v any) error {
	m.published = append(m.published, v)
	return m.err
}

type fakeFetcher struct {
	p   *marketplace.Product
	err error
	url string
}

func (f *fakeFetcher) FetchProduct(_ context.Context, u string) (*marketplace.Product, error) {
	f.url = u
	return f.p, f.err
}
func makeDelivery(t *testing.T, v any, a *ackRecorder) amqp.Delivery {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return amqp.Delivery{Acknowledger: a, Body: b}
}
func TestConsumerHandleLookup(t *testing.T) {
	for _, tt := range []struct {
		name                                        string
		body                                        any
		fetchErr, pubErr                            error
		malformed                                   bool
		wantAck, wantNack, wantRequeue, wantSuccess bool
	}{{name: "success", body: contracts.LookupTask{TaskID: "t", LookupID: "l", URL: "u", Platform: "wb"}, wantAck: true, wantSuccess: true}, {name: "fetch failure becomes result", body: contracts.LookupTask{LookupID: "l", Platform: "wb"}, fetchErr: errors.New("offline"), wantAck: true}, {name: "publish failure requeues", body: contracts.LookupTask{LookupID: "l", Platform: "wb"}, pubErr: errors.New("mq"), wantNack: true, wantRequeue: true, wantSuccess: true}, {name: "malformed drops", malformed: true, wantNack: true}} {
		t.Run(tt.name, func(t *testing.T) {
			m := &fakeMQ{err: tt.pubErr}
			f := &fakeFetcher{p: &marketplace.Product{Name: "Lamp", Price: 12.5, Currency: "RUB"}, err: tt.fetchErr}
			c := New(m, f)
			a := &ackRecorder{}
			d := makeDelivery(t, tt.body, a)
			if tt.malformed {
				d.Body = []byte("{")
			}
			c.handle(context.Background(), d, broker.QueueLookupTasks)
			if a.ack != tt.wantAck || a.nack != tt.wantNack || a.requeue != tt.wantRequeue {
				t.Fatalf("ack/nack/requeue=%v/%v/%v", a.ack, a.nack, a.requeue)
			}
			if len(m.published) > 0 {
				r := m.published[0].(contracts.PriceResult)
				if r.LookupID != "l" || r.Success != tt.wantSuccess || (tt.fetchErr != nil && r.Error != "offline") {
					t.Fatalf("price result = %#v", r)
				}
			}
		})
	}
}
func TestConsumerHandleScraperPreservesForceRecipient(t *testing.T) {
	m := &fakeMQ{}
	f := &fakeFetcher{p: &marketplace.Product{Name: "Lamp", Price: 39, Currency: "RUB"}}
	c := New(m, f)
	a := &ackRecorder{}
	c.handle(context.Background(), makeDelivery(t, contracts.ScraperTask{TaskID: "t", ProductID: "p", URL: "u", Platform: "wb", Force: true, ChatID: 77}, a), broker.QueueScraperTasks)
	r := m.published[0].(contracts.PriceResult)
	if !r.Force || r.ChatID != 77 || r.ProductID != "p" || r.Price != 39 || !a.ack {
		t.Fatalf("result=%#v ack=%v", r, a.ack)
	}
}
func TestConsumerUnknownPlatformProducesFailureResult(t *testing.T) {
	m := &fakeMQ{}
	f := &fakeFetcher{}
	c := New(m, f)
	a := &ackRecorder{}
	c.handle(context.Background(), makeDelivery(t, contracts.LookupTask{LookupID: "l", Platform: "ozon"}, a), broker.QueueLookupTasks)
	r := m.published[0].(contracts.PriceResult)
	if r.Success || r.Error != "unknown platform: ozon" || !a.ack {
		t.Fatalf("result=%#v ack=%v", r, a.ack)
	}
}
