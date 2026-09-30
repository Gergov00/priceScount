//go:build regression

package state

import (
	"sync"
	"testing"
)

// Regression for PROJECT_REVIEW.md S1: Get returns a mutable pointer after
// releasing the store lock, so concurrent dialog access can race.
func TestConcurrentMutationOfSharedChatSessionIsSafe(t *testing.T) {
	store := New()
	store.Set(7, &Session{Step: StepWaitingLookup})
	session := store.Get(7)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 10000; n++ {
				if i == 0 {
					session.Step = StepWaitingMaxPrice
				} else {
					_ = session.Step
				}
			}
		}(i)
	}
	wg.Wait()
}
