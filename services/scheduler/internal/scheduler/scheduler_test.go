package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	count int
	err   error
	calls int
}

func (s *fakeStore) EnqueueDue(context.Context) (int, error) { s.calls++; return s.count, s.err }

func TestDispatchEnqueuesDueWork(t *testing.T) {
	st := &fakeStore{count: 7}
	s := New(st, time.Minute)
	s.dispatch(context.Background())
	if st.calls != 1 {
		t.Fatalf("EnqueueDue calls=%d", st.calls)
	}
}
func TestDispatchLogsStoreFailureWithoutPublishing(t *testing.T) {
	st := &fakeStore{err: errors.New("db unavailable")}
	s := New(st, time.Minute)
	s.dispatch(context.Background())
	if st.calls != 1 {
		t.Fatalf("EnqueueDue calls=%d", st.calls)
	}
}
