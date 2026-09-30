package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

type delayedLookupGateway struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *delayedLookupGateway) StartLookup(context.Context, string) (string, error) {
	return "", nil
}

func (g *delayedLookupGateway) PollLookup(context.Context, string) (*gateway.LookupResult, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release // Deliberately ignore cancellation to model a late Gateway response.
	return &gateway.LookupResult{Status: "done", Name: "stale product", URL: "https://example.test/stale", Price: 12}, nil
}

func (*delayedLookupGateway) CreateSubscription(context.Context, gateway.CreateSubscriptionRequest) (*gateway.CreateSubscriptionResponse, error) {
	return nil, nil
}
func (*delayedLookupGateway) ListSubscriptions(context.Context, int64) ([]gateway.Subscription, error) {
	return nil, nil
}
func (*delayedLookupGateway) PauseSubscription(context.Context, int64, string) error  { return nil }
func (*delayedLookupGateway) ResumeSubscription(context.Context, int64, string) error { return nil }
func (*delayedLookupGateway) EditSubscription(context.Context, int64, string, float64, float64) error {
	return nil
}
func (*delayedLookupGateway) DeleteSubscription(context.Context, int64, string) error { return nil }
func (*delayedLookupGateway) ForceCheck(context.Context, int64, string) error         { return nil }
func (*delayedLookupGateway) GetHistory(context.Context, int64, string) ([]gateway.PricePoint, error) {
	return nil, nil
}

func TestDelayedPollLookupAfterCancelOrReplacementDoesNotSendOrOverwrite(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "cancelled session"
		if replacement {
			name = "replacement lookup"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var sent []string
			write := func(w http.ResponseWriter, body string) {
				if _, err := io.WriteString(w, body); err != nil {
					t.Errorf("write controlled Telegram response: %v", err)
				}
			}
			telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse Telegram request: %v", err)
					}
					mu.Lock()
					sent = append(sent, r.Form.Get("text"))
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/getMe") {
					write(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","username":"testbot"}}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					write(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":42,"type":"private"},"text":"ok"}}`)
					return
				}
				write(w, `{"ok":true,"result":true}`)
			}))
			defer telegram.Close()

			store := state.New()
			gw := &delayedLookupGateway{entered: make(chan struct{}), release: make(chan struct{})}
			bot, err := New("test-token", store, gw,
				WithHTTPClient(&http.Client{}),
				WithAPIEndpoint(telegram.URL+"/bot%s/%s"),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.Set(42, &state.Session{Step: state.StepWaitingLookup, LookupID: "local-old"})
			bot.lookupMu.Lock()
			bot.activeLookups[42] = lookupTask{id: "local-old", cancel: cancel}
			bot.lookupMu.Unlock()
			done := make(chan struct{})
			go func() {
				defer close(done)
				bot.pollLookup(ctx, 42, "local-old", "gateway-old")
			}()
			select {
			case <-gw.entered:
			case <-time.After(time.Second):
				t.Fatal("mock lookup did not start")
			}

			bot.cancelLookup(42)
			if replacement {
				store.Set(42, &state.Session{Step: state.StepWaitingLookup, LookupID: "local-new"})
			} else {
				store.Clear(42)
			}
			close(gw.release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("late lookup did not return")
			}

			session := store.Get(42)
			if replacement && (session.Step != state.StepWaitingLookup || session.LookupID != "local-new") {
				t.Fatalf("late lookup overwrote replacement session: %+v", session)
			}
			if !replacement && (session.Step != state.StepIdle || session.LookupID != "") {
				t.Fatalf("late lookup restored cancelled session: %+v", session)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, text := range sent {
				if strings.Contains(text, "stale product") || strings.Contains(text, "Текущая цена") {
					t.Fatalf("late lookup sent completion message: %q", text)
				}
			}
		})
	}
}

var _ Gateway = (*delayedLookupGateway)(nil)
