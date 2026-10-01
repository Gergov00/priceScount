package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func TestBotInitializationErrorRedactsToken(t *testing.T) {
	const token = "123456:secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"error_code":401,"description":"invalid token 123456:secret-token"}`))
	}))
	defer server.Close()
	_, err := New(token, state.New(), nil, WithHTTPClient(&http.Client{}), WithAPIEndpoint(server.URL+"/bot%s/%s"))
	if err == nil {
		t.Fatal("New succeeded with rejected bot token")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("initialization error exposed token: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("initialization error omitted safe diagnostic: %v", err)
	}
}
