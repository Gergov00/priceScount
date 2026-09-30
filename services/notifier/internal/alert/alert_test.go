package alert

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func client(rt http.RoundTripper) *http.Client { return &http.Client{Transport: rt} }

func TestSendClassifiesTelegramResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		permanent bool
		wantErr   bool
	}{
		{"accepted", 200, `{"ok":true}`, false, false},
		{"bad request permanent", 400, `{"ok":false,"description":"bad request"}`, true, true},
		{"rate limit transient", 429, `{"ok":false,"parameters":{"retry_after":1}}`, false, true},
		{"server error transient", 503, `{"ok":false}`, false, true},
		{"malformed response transient", 200, `not json`, false, true},
		{"ok false not success", 200, `{"ok":false}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTelegramSender("unit-token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: r}, nil
			}))))
			err := s.sendOnce(context.Background(), 123, "hello")
			if (err != nil) != tc.wantErr {
				t.Fatalf("sendOnce error = %v", err)
			}
			if tc.permanent && !errors.Is(err, ErrPermanent) {
				t.Fatalf("%v is not permanent", err)
			}
		})
	}
}

func TestSendEncodesTargetAndText(t *testing.T) {
	s := NewTelegramSender("token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"chat_id":456`) || !strings.Contains(string(b), `"text":"price update"`) {
			t.Fatalf("request body %s", b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header), Request: r}, nil
	}))))
	if err := s.Send(context.Background(), 456, "price update"); err != nil {
		t.Fatal(err)
	}
}

func TestSendCancelled(t *testing.T) {
	started := make(chan struct{})
	s := NewTelegramSender("token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Send(ctx, 1, "hello") }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not stop after cancellation")
	}
}

func TestInjectedClientGetsDeadline(t *testing.T) {
	s := NewTelegramSender("token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > requestTimeout {
			t.Fatalf("request deadline missing or too long")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header), Request: r}, nil
	}))))
	if err := s.Send(context.Background(), 1, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfter(t *testing.T) {
	calls := 0
	s := NewTelegramSender("token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"ok":true}`
		if calls == 1 {
			body = `{"ok":false,"parameters":{"retry_after":1}}`
		}
		return &http.Response{StatusCode: func() int {
			if calls == 1 {
				return 429
			}
			return 200
		}(), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	}))))
	start := time.Now()
	if err := s.Send(context.Background(), 1, "x"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("retry_after ignored: waited %v", elapsed)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestLongRetryAfterReturnsForDurableRetry(t *testing.T) {
	s := NewTelegramSender("token", WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"ok":false,"parameters":{"retry_after":120}}`)), Header: make(http.Header), Request: r}, nil
	}))))
	start := time.Now()
	err := s.Send(context.Background(), 1, "x")
	var retryErr *RetryError
	if !errors.As(err, &retryErr) || retryErr.After != 120*time.Second {
		t.Fatalf("Send error=%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("long retry_after blocked sender: %v", elapsed)
	}
}

func TestTokenEchoInDescriptionIsSanitized(t *testing.T) {
	token := "secret-token"
	s := NewTelegramSender(token, WithHTTPClient(client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":false,"description":"rejected secret-token"}`
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	}))))
	err := s.sendOnce(context.Background(), 1, "x")
	if strings.Contains(err.Error(), token) {
		t.Fatalf("API error exposed token: %v", err)
	}
}
