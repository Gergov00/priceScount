package headless

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

// Scraper uses a headless Chrome browser to fetch pages that block regular HTTP clients.
// A single Chrome process is reused across requests (one tab per request).
type Scraper struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
}

func New() (*Scraper, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),            // required inside Docker
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true), // prevents /dev/shm OOM in containers
		chromedp.Flag("disable-extensions", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
	)

	// Allow overriding the Chrome binary path via env (needed for Alpine).
	if path := os.Getenv("CHROMIUM_PATH"); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	return &Scraper{allocCtx: allocCtx, allocCancel: cancel}, nil
}

// Fetch navigates to url in a new browser tab, waits for the page to render,
// and returns the outer HTML. Each call opens and closes its own tab.
func (s *Scraper) Fetch(url string) (string, error) {
	ctx, cancel := chromedp.NewContext(s.allocCtx)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var html string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`body`, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second), // allow JS-rendered prices to appear
		chromedp.OuterHTML(`html`, &html),
	)
	if err != nil {
		return "", fmt.Errorf("headless fetch: %w", err)
	}
	return html, nil
}

func (s *Scraper) Close() {
	s.allocCancel()
}
