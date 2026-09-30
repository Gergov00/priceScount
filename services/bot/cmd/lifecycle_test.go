package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	botservice "github.com/Gergov00/pricescount/services/bot/internal/bot"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func TestBotFxHookStopsAgainstControlledTelegramEndpoint(t *testing.T) {
	updatesStarted := make(chan struct{})
	releaseUpdates := make(chan struct{})
	var releaseOnce sync.Once
	var once sync.Once
	write := func(w http.ResponseWriter, body string) {
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write controlled Telegram response: %v", err)
		}
	}
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			write(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","username":"testbot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			once.Do(func() { close(updatesStarted) })
			select {
			case <-r.Context().Done():
			case <-releaseUpdates:
				write(w, `{"ok":true,"result":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			write(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`)
		default:
			write(w, `{"ok":true,"result":true}`)
		}
	}))
	defer telegram.Close()
	defer releaseOnce.Do(func() { close(releaseUpdates) })

	b, err := botservice.New("fake-ci-token", state.New(), nil,
		botservice.WithHTTPClient(&http.Client{}),
		botservice.WithAPIEndpoint(telegram.URL+"/bot%s/%s"),
	)
	if err != nil {
		t.Fatalf("initialize Bot against the controlled Telegram endpoint: %v", err)
	}
	app := fx.New(fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }), fx.Supply(b), fx.Invoke(runBot))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updatesStarted:
	case <-ctx.Done():
		t.Fatal("Bot did not start its controlled long-poll request")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Bot lifecycle did not cancel and wait for its Telegram request: %v", err)
	}
	releaseOnce.Do(func() { close(releaseUpdates) })
}
