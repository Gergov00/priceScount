//go:build regression

package alert

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestTransportErrorDoesNotExposeBotToken(t *testing.T) {
	withTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }))
	err := send("secret-token", 12, "text")
	if err == nil {
		t.Fatal("expected transport failure")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("transport error exposed bot token: %v", err)
	}
}
