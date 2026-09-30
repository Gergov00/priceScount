package alert

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestTransportErrorDoesNotExposeBotToken(t *testing.T) {
	s := NewTelegramSender("secret-token", WithHTTPClient(client(roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }))))
	err := s.sendOnce(context.Background(), 12, "text")
	if err == nil {
		t.Fatal("expected transport failure")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("transport error exposed bot token: %v", err)
	}
}
