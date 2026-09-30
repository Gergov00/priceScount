package scheduler

import (
	"context"
	"errors"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"testing"
)

type fakeDueStore struct {
	rows []store.URLEntry
	err  error
}

func (s fakeDueStore) DueURLs(context.Context) ([]store.URLEntry, error) { return s.rows, s.err }

type fakePublisher struct {
	queues  []string
	tasks   []any
	failURL string
}

func (p *fakePublisher) Publish(_ context.Context, q string, v any) error {
	p.queues = append(p.queues, q)
	p.tasks = append(p.tasks, v)
	if p.failURL != "" && v.(contracts.ScraperTask).URL == p.failURL {
		return errors.New("offline")
	}
	return nil
}
func TestSchedulerDispatchPublishesDueEntries(t *testing.T) {
	p := &fakePublisher{}
	s := New(p, fakeDueStore{rows: []store.URLEntry{{ProductID: "p1", URL: "u1", Platform: "wb"}, {ProductID: "p2", URL: "u2", Platform: "wb"}}}, 0)
	s.dispatch(context.Background())
	if len(p.tasks) != 2 || p.queues[0] != broker.QueueScraperTasks {
		t.Fatalf("published queues/tasks=%v/%d", p.queues, len(p.tasks))
	}
	task := p.tasks[0].(contracts.ScraperTask)
	if task.ProductID != "p1" || task.URL != "u1" || task.TaskID == "" {
		t.Fatalf("published task = %#v", task)
	}
}
func TestSchedulerDispatchContinuesAfterPublishError(t *testing.T) {
	p := &fakePublisher{failURL: "u1"}
	s := New(p, fakeDueStore{rows: []store.URLEntry{{ProductID: "p1", URL: "u1"}, {ProductID: "p2", URL: "u2"}}}, 0)
	s.dispatch(context.Background())
	if len(p.tasks) != 2 {
		t.Fatalf("attempted %d publishes, want 2", len(p.tasks))
	}
}
