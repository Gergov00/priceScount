package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/services/notifier/internal/alert"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type ackRecorder struct{ ack, nack, requeue bool }

func (a *ackRecorder) Ack(_ uint64, _ bool) error          { a.ack = true; return nil }
func (a *ackRecorder) Nack(_ uint64, _ bool, r bool) error { a.nack = true; a.requeue = r; return nil }
func (a *ackRecorder) Reject(_ uint64, _ bool) error       { return nil }

type fakeMQ struct {
	queue   string
	message any
	err     error
	consume <-chan amqp.Delivery
}

func (m *fakeMQ) Consume(string, string) (<-chan amqp.Delivery, error) { return m.consume, nil }
func (m *fakeMQ) Publish(_ context.Context, queue string, message any) error {
	m.queue, m.message = queue, message
	return m.err
}

type fakeSender struct {
	chat int64
	text string
	err  error
}

func (s *fakeSender) Send(_ context.Context, id int64, text string) error {
	s.chat, s.text = id, text
	return s.err
}

func TestConsumerHandle(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		sendErr   error
		wantAck   bool
		wantQueue string
		wantCalls int
	}{
		{name: "telegram delivered", body: []byte(`{"channel":"telegram","target":"123","text":"hello"}`), wantAck: true, wantCalls: 1},
		{name: "invalid target dead letters", body: []byte(`{"channel":"telegram","target":"x","text":"hello"}`), wantAck: true, wantQueue: "notify.dead"},
		{name: "unknown channel dead letters", body: []byte(`{"channel":"push","target":"123"}`), wantAck: true, wantQueue: "notify.dead"},
		{name: "malformed dead letters", body: []byte(`{`), wantAck: true, wantQueue: "notify.dead"},
		{name: "transient failure transfers then ack", body: []byte(`{"channel":"telegram","target":"123","text":"hello"}`), sendErr: errors.New("temporary"), wantAck: true, wantQueue: "notify.retry", wantCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mq := &fakeMQ{}
			sender := &fakeSender{err: tc.sendErr}
			c := New(mq, sender, 0)
			a := &ackRecorder{}
			c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: tc.body})
			if a.ack != tc.wantAck || a.nack {
				t.Fatalf("ack/nack=%v/%v", a.ack, a.nack)
			}
			if mq.queue != tc.wantQueue {
				t.Fatalf("published queue=%q want %q", mq.queue, tc.wantQueue)
			}
			if tc.wantQueue == "notify.dead" && tc.name == "malformed dead letters" {
				envelope, ok := mq.message.(malformedEnvelope)
				if !ok || envelope.Body != string(tc.body) {
					t.Fatalf("malformed DLQ envelope=%#v", mq.message)
				}
			}
			if tc.wantCalls == 1 && (sender.chat != 123 || sender.text != "hello") {
				t.Fatalf("sent chat/text=%d/%q", sender.chat, sender.text)
			}
		})
	}
}

func TestPermanentDeliveryDeadLetters(t *testing.T) {
	mq := &fakeMQ{}
	c := New(mq, &fakeSender{err: alert.ErrPermanent}, 0)
	a := &ackRecorder{}
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"7","text":"x"}`)})
	if mq.queue != "notify.dead" || !a.ack || a.nack {
		t.Fatalf("queue/ack/nack=%q/%v/%v", mq.queue, a.ack, a.nack)
	}
}

func TestRetryPublishFailureDoesNotAck(t *testing.T) {
	mq := &fakeMQ{err: errors.New("broker unavailable")}
	c := New(mq, &fakeSender{err: errors.New("network")}, 0)
	a := &ackRecorder{}
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"7","text":"x"}`)})
	if !a.nack || !a.requeue || a.ack {
		t.Fatalf("ack/nack/requeue=%v/%v/%v", a.ack, a.nack, a.requeue)
	}
}

func TestContextCancellationDoesNotAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mq := &fakeMQ{}
	sender := &fakeSender{}
	a := &ackRecorder{}
	New(mq, sender, 0).handle(ctx, amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"7","text":"x"}`)})
	if a.ack || a.nack || mq.message != nil {
		t.Fatalf("ack/nack/publish=%v/%v/%#v", a.ack, a.nack, mq.message)
	}
	if sender.chat != 0 {
		t.Fatal("cancelled delivery invoked sender")
	}
}

func TestNotBeforeTaskReturnsToDelayQueueWithoutBlocking(t *testing.T) {
	task := contracts.NotifyTask{Channel: "telegram", Target: "7", Text: "x", NotBefore: time.Now().Add(time.Hour)}
	body, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	mq := &fakeMQ{}
	a := &ackRecorder{}
	New(mq, &fakeSender{}, 0).handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: body})
	if mq.queue != broker.QueueNotifyRetry || !a.ack {
		t.Fatalf("queue/ack=%q/%v", mq.queue, a.ack)
	}
}

func TestLongRetryAfterPersistedBeforeRetry(t *testing.T) {
	mq := &fakeMQ{}
	delay := 2 * broker.NotifyRetryDelay
	c := New(mq, &fakeSender{err: &alert.RetryError{After: delay, Err: errors.New("rate limit")}}, 0)
	a := &ackRecorder{}
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, Body: []byte(`{"channel":"telegram","target":"7","text":"x"}`)})
	if mq.queue != "notify.retry" || !a.ack {
		t.Fatalf("queue/ack=%q/%v", mq.queue, a.ack)
	}
	var task struct {
		NotBefore time.Time `json:"not_before"`
	}
	b, err := json.Marshal(mq.message)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &task); err != nil {
		t.Fatal(err)
	}
	if time.Until(task.NotBefore) < delay-time.Second {
		t.Fatalf("not_before too soon: %v", task.NotBefore)
	}
}

func TestTransferFailureLogDoesNotExposeToken(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	const token = "secret-token"
	sender := alert.NewTelegramSender(token, alert.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"rejected secret-token"}`)), Header: make(http.Header), Request: r}, nil
	})}))
	mq := &fakeMQ{}
	a := &ackRecorder{}
	New(mq, sender, 0).handle(context.Background(), amqp.Delivery{
		Acknowledger: a,
		Body:         []byte(`{"channel":"telegram","target":"7","text":"x"}`),
	})
	encoded, err := json.Marshal(mq.message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(output.String(), token) {
		t.Fatalf("captured error/log contains token: %s %s", encoded, output.String())
	}
}
