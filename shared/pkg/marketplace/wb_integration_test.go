//go:build integration && browser

package marketplace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestBrowserSelectsCurrentWildberriesPriceDOM(t *testing.T) {
	chromePath := os.Getenv("TEST_CHROME_PATH")
	if chromePath == "" {
		t.Skip("TEST_CHROME_PATH is not set")
	}
	cases := []struct {
		name string
		dom  string
		want string
	}{
		{
			name: "red sale price wins over regular and old prices",
			dom: `<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_secondary mo-typography_modifier_strikethrough priceBlockOldPrice--WPpN1">9 999 ₽</span>
				<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_primary priceBlockFinalPrice--yEKcc">8 765 ₽</span>
				<h3 class="mo-typography mo-typography_variant_title2 mo-typography_variable-weight_title2 mo-typography_variable mo-typography_colors_danger">1 234 ₽</h3>`,
			want: "1 234 ₽",
		},
		{
			name: "regular final price skips old price before it",
			dom: `<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_secondary mo-typography_modifier_strikethrough priceBlockOldPrice--WPpN1">7 654 ₽</span>
				<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_primary priceBlockFinalPrice--yEKcc">2 345 ₽</span>`,
			want: "2 345 ₽",
		},
		{
			name: "strikethrough final-price-prefix candidate is excluded",
			dom: `<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_secondary mo-typography_modifier_strikethrough priceBlockFinalPrice--old">9 876 ₽</span>
				<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_primary priceBlockFinalPrice--yEKcc">1 111 ₽</span>`,
			want: "1 111 ₽",
		},
		{
			name: "old price alone is not a candidate",
			dom:  `<span class="mo-typography mo-typography_variant_body mo-typography_variable-weight_body mo-typography_variable mo-typography_colors_secondary mo-typography_modifier_strikethrough priceBlockOldPrice--WPpN1">7 654 ₽</span>`,
			want: "",
		},
		{
			name: "legacy danger selector remains supported",
			dom: `<ins class="price-block__final-price">4 321 ₽</ins>
				<h3 class="mo-typography mo-typography_color_danger">3 210 ₽</h3>`,
			want: "3 210 ₽",
		},
		{
			name: "legacy final-price selector remains supported",
			dom:  `<ins class="price-block__final-price">4 321 ₽</ins>`,
			want: "4 321 ₽",
		},
	}
	client := newWBClient(chromePath)
	defer client.Close()
	ctx, cancel := chromedp.NewContext(client.allocCtx)
	defer cancel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<!doctype html><html><head><meta charset=\"utf-8\"></head><body>"+tc.dom+"</body></html>"))
			var result string
			if err := chromedp.Run(ctx,
				chromedp.Navigate(dataURL),
				chromedp.Evaluate(wbPageDataJS, &result),
			); err != nil {
				t.Fatalf("evaluate production price selectors: %v", err)
			}
			var page struct {
				Price string `json:"price"`
			}
			if err := json.Unmarshal([]byte(result), &page); err != nil {
				t.Fatalf("decode production page result %q: %v", result, err)
			}
			if page.Price != tc.want {
				t.Fatalf("selected price = %q, want %q", page.Price, tc.want)
			}
			if tc.want == "" && parseWBPrice(page.Price) != 0 {
				t.Fatalf("old-only fixture parsed as price %v", parseWBPrice(page.Price))
			}
		})
	}
}

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
