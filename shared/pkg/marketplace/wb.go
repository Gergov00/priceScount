package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

var wbProductIDRe = regexp.MustCompile(`wildberries\.ru/catalog/(\d+)`)

// WBClient fetches product data from Wildberries using a headless browser.
// Stores allocCtx as part of the service lifecycle — Chrome process runs until Close().
type WBClient struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
}

func NewWBClient() *WBClient {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.ExecPath("chromium-browser"),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	return &WBClient{allocCtx: allocCtx, allocCancel: allocCancel}
}

// Close shuts down the underlying Chrome process.
func (c *WBClient) Close() {
	c.allocCancel()
}

func (c *WBClient) FetchProduct(ctx context.Context, rawURL string) (*Product, error) {
	m := wbProductIDRe.FindStringSubmatch(rawURL)
	if m == nil {
		return nil, fmt.Errorf("invalid wildberries url")
	}
	nmID := m[1]
	pageURL := "https://www.wildberries.ru/catalog/" + nmID + "/detail.aspx"

	bCtx, bCancel := chromedp.NewContext(c.allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {}),
	)
	defer bCancel()

	tCtx, tCancel := context.WithTimeout(bCtx, 40*time.Second)
	defer tCancel()

	var jsResult string
	if err := chromedp.Run(tCtx,
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(7*time.Second),
		chromedp.Evaluate(`JSON.stringify((function() {
			var priceSelectors = [
				'h3[class*="mo-typography_color_danger"]',
				'[class*="mo-typography_color_danger"]',
				'ins.price-block__final-price',
				'.price-block__final-price',
				'[class*="price-block__final-price"]',
				'[class*="finalPrice"]'
			];
			var priceEl = null;
			for (var i = 0; i < priceSelectors.length; i++) {
				priceEl = document.querySelector(priceSelectors[i]);
				if (priceEl && priceEl.innerText.trim()) break;
			}
			var nameEl = document.querySelector('[class*="productTitle"]') ||
			             document.querySelector('h1') ||
			             document.querySelector('h2[class*="mo-typography_color_primary"]');
			return {
				price: priceEl ? priceEl.innerText.trim() : '',
				name:  nameEl ? nameEl.innerText.trim() : '',
				title: document.title,
				url:   location.href
			};
		})())`, &jsResult),
	); err != nil {
		return nil, fmt.Errorf("wb fetch: %w", err)
	}

	var data struct {
		Price string `json:"price"`
		Name  string `json:"name"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal([]byte(jsResult), &data); err != nil {
		return nil, fmt.Errorf("parse wb result: %w", err)
	}

	slog.Debug("wb headless result", "title", data.Title, "url", data.URL, "price", data.Price, "name", data.Name)

	// If WB redirected to the main page, the title won't contain the nmID or "купить за".
	if !strings.Contains(data.Title, nmID) && !strings.Contains(data.Title, "купить за") {
		return nil, fmt.Errorf("product page not found, got main page (nmID=%s title=%q)", nmID, data.Title)
	}

	price := parseWBPrice(data.Price)
	if price == 0 {
		return nil, fmt.Errorf("price not found on page (title=%q url=%q)", data.Title, data.URL)
	}

	name := extractWBName(data.Title, nmID)
	if name == "" {
		name = strings.TrimSpace(data.Name)
	}
	if name == "" {
		name = "WB товар"
	}

	return &Product{Name: name, Price: price, Currency: "RUB"}, nil
}

// extractWBName parses the product name from the WB page title.
// Title format: "{name} {nmID} купить за ... в интернет-магазине Wildberries"
func extractWBName(title, nmID string) string {
	if idx := strings.Index(title, " "+nmID); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	if idx := strings.Index(title, " купить за "); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	return ""
}

func parseWBPrice(s string) float64 {
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(digits.String(), 64)
	return f
}
