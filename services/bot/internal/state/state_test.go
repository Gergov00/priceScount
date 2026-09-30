package state

import (
	"sync"
	"testing"
)

func TestGetMissingChatReturnsIdleSession(t *testing.T) {
	store := New()
	if got := store.Get(41); got.Step != StepIdle {
		t.Fatalf("missing chat step = %q, want %q", got.Step, StepIdle)
	}
}

func TestSessionLifecycleIsScopedToChat(t *testing.T) {
	store := New()
	first := &Session{Step: StepWaitingMinPrice, LookupID: "lookup-a"}
	second := &Session{Step: StepWaitingMaxPrice, LookupID: "lookup-b"}
	store.Set(41, first)
	store.Set(42, second)
	first.LookupID = "caller mutation"

	if got := store.Get(41); got == first || got.LookupID != "lookup-a" {
		t.Fatalf("chat 41 session = %#v, want independent copy of %#v", got, first)
	}
	snapshot := store.Get(41)
	snapshot.LookupID = "snapshot mutation"
	if got := store.Get(41); got.LookupID != "lookup-a" {
		t.Fatalf("mutating returned copy changed stored session: %#v", got)
	}
	if got := store.Get(42); got == second || got.LookupID != "lookup-b" {
		t.Fatalf("chat 42 session = %#v, want independent copy of %#v", got, second)
	}
	store.Clear(41)
	if got := store.Get(41); got.Step != StepIdle {
		t.Fatalf("cleared chat step = %q, want %q", got.Step, StepIdle)
	}
	if got := store.Get(42); got == second || got.LookupID != "lookup-b" {
		t.Fatalf("clearing chat 41 changed chat 42 session: %#v", got)
	}
}

func TestConcurrentDistinctChatsRemainIsolated(t *testing.T) {
	store := New()
	const chats = 64
	var wg sync.WaitGroup
	for id := int64(1); id <= chats; id++ {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Set(id, &Session{Step: StepWaitingLookup, LookupID: "lookup"})
			if got := store.Get(id); got.Step != StepWaitingLookup {
				t.Errorf("chat %d step = %q, want %q", id, got.Step, StepWaitingLookup)
			}
		}()
	}
	wg.Wait()
}
