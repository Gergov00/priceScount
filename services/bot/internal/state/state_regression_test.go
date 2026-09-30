package state

import (
	"sync"
	"testing"
)

func TestConcurrentIndependentSessionUpdatesAreSafe(t *testing.T) {
	store := New()
	store.Set(7, &Session{Step: StepWaitingLookup, LookupID: "lookup"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				snapshot := store.Get(7)
				snapshot.Page++
				store.Update(7, func(current *Session) { current.Page++ })
			}
		}()
	}
	wg.Wait()
	if got := store.Get(7).Page; got != 8000 {
		t.Fatalf("atomic page count = %d, want 8000", got)
	}
}

func TestLookupCompletionAfterCancelAndNewLookup(t *testing.T) {
	store := New()
	store.Set(7, &Session{Step: StepWaitingLookup, LookupID: "old", Page: 3})
	if store.CompleteLookup(7, "old", Session{Step: StepWaitingMinPrice}) != true {
		t.Fatal("current lookup did not complete")
	}
	store.Set(7, &Session{Step: StepIdle})
	if store.CompleteLookup(7, "old", Session{Step: StepWaitingMinPrice}) {
		t.Fatal("cancelled lookup completed")
	}
	store.Set(7, &Session{Step: StepWaitingLookup, LookupID: "new"})
	if store.CompleteLookup(7, "old", Session{Step: StepWaitingMinPrice}) {
		t.Fatal("outdated lookup replaced new lookup")
	}
}
