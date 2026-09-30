//go:build integration && browser

package marketplace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBrowserFetchCancelsInFlightLocalPage(t *testing.T) {
	chromePath := os.Getenv("TEST_CHROME_PATH")
	if chromePath == "" {
		t.Skip("TEST_CHROME_PATH is not set")
	}
	requestStarted := make(chan struct{})
	var requestOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		requestOnce.Do(func() { close(requestStarted) })
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newWBClient(chromePath)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.fetchPage(ctx, server.URL, "123"); done <- err }()
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Chrome did not request local page")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("fetch returned nil error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("browser fetch did not stop promptly")
	}
}
