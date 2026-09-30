package bot

import (
	"context"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestDispatcherPreservesChatFIFOAndRunsOtherChats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan tgbotapi.Update, 4)
	firstStarted := make(chan struct{})
	otherChatDone := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var mu sync.Mutex
	var got []string
	d := &dispatcher{handle: func(_ context.Context, update tgbotapi.Update) {
		mu.Lock()
		got = append(got, update.Message.Text)
		mu.Unlock()
		if update.Message.Text == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		if update.Message.Text == "other" {
			close(otherChatDone)
		}
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx, updates) }()
	updates <- dispatchUpdate(1, "first")
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first update did not start")
	}
	updates <- dispatchUpdate(1, "second")
	updates <- dispatchUpdate(1, "third")
	otherChat := int64(2)
	for workerHash(otherChat) == workerHash(1) {
		otherChat++
	}
	updates <- dispatchUpdate(otherChat, "other")
	close(updates)
	select {
	case <-otherChatDone:
	case <-time.After(time.Second):
		t.Fatal("another chat was blocked by the first")
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	positions := map[string]int{}
	for i, value := range got {
		positions[value] = i
	}
	if len(got) != 4 || !(positions["first"] < positions["second"] && positions["second"] < positions["third"]) {
		t.Fatalf("update order = %v", got)
	}
}

func dispatchUpdate(chatID int64, text string) tgbotapi.Update {
	return tgbotapi.Update{Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}, Text: text}}
}

func workerHash(chatID int64) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatInt(chatID, 10)))
	return h.Sum32() % dispatcherWorkers
}

func TestCancelLookupCancelsItsNetworkContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bot := &Bot{activeLookups: map[int64]lookupTask{42: {id: "lookup-a", cancel: cancel}}}
	bot.cancelLookup(42)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop active lookup request")
	}
	bot.lookupMu.Lock()
	defer bot.lookupMu.Unlock()
	if _, ok := bot.activeLookups[42]; ok {
		t.Fatal("cancelled lookup remained registered")
	}
}

func TestContextHTTPClientKeepsResponseBodyUntilReadOrCancel(t *testing.T) {
	bodyStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBody) }) }
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(bodyStarted)
		select {
		case <-releaseBody:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("ok"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &contextHTTPClient{client: &http.Client{}, ctx: ctx}
	resp, err := client.Do(mustRequest(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-bodyStarted:
	case <-time.After(time.Second):
		t.Fatal("server body did not start")
	}
	release()
	first := make([]byte, 2)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(first) != "ok" {
		t.Fatalf("body prefix = %q, want ok", first)
	}
	cancel()
	_ = resp.Body.Close()
}

func TestContextHTTPClientCancelsInflightRequest(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &contextHTTPClient{client: &http.Client{}, ctx: ctx}
	done := make(chan error, 1)
	req := mustRequest(t, server.URL)
	go func() { _, err := client.Do(req); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("request completed without cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight request was not cancelled")
	}
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	return req
}
