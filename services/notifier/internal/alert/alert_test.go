package alert

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func withTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = old })
}
func TestSendClassifiesTelegramResponses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		permanent bool
		ok        bool
	}{{"accepted", 200, false, true}, {"bad request is permanent", 400, true, false}, {"rate limit is transient", 429, false, false}, {"server error is transient", 503, false, false}} {
		t.Run(tt.name, func(t *testing.T) {
			withTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
					t.Fatalf("request = %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(`{"ok":false}`)), Header: make(http.Header), Request: r}, nil
			}))
			err := send("unit-token", 123, "hello")
			if tt.ok && err != nil {
				t.Fatal(err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected error")
			}
			if tt.permanent && !errors.Is(err, errPermanent) {
				t.Fatalf("error %v is not permanent", err)
			}
			if !tt.permanent && !tt.ok && errors.Is(err, errPermanent) {
				t.Fatalf("transient response marked permanent: %v", err)
			}
		})
	}
}
func TestSendEncodesTargetAndText(t *testing.T) {
	withTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(b), `"chat_id":456`) || !strings.Contains(string(b), `"text":"price update"`) {
			t.Fatalf("request body %s", b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header), Request: r}, nil
	}))
	if err := send("token", 456, "price update"); err != nil {
		t.Fatal(err)
	}
}
