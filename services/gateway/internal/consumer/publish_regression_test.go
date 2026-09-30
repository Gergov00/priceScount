//go:build regression

package consumer

import (
	"context"
	"errors"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"testing"
)

func TestNotifyPublishFailureMustRequeue(t *testing.T) {
	a := &ackRecorder{}
	m := &fakeMQ{err: errors.New("broker unavailable")}
	s := &fakeStore{subs: []store.ProductSub{{SubID: "s", ChatID: 1, MinPrice: 100, MaxPrice: 200}}}
	c := New(m, s)
	c.handle(context.Background(), delivery(t, contracts.PriceResult{Success: true, ProductID: "p", Price: 50}, a))
	if !a.nack || !a.requeue {
		t.Fatal("notification publish failure must requeue result")
	}
}
