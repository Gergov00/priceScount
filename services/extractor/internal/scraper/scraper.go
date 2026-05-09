package scraper

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	maxBodyBytes = 512 * 1024
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// ErrAccessDenied is returned when the server blocks the request (401, 403, 498, etc.).
// The consumer uses this to trigger a headless browser fallback.
type ErrAccessDenied struct {
	StatusCode int
}

func (e *ErrAccessDenied) Error() string {
	return fmt.Sprintf("access denied (status %d)", e.StatusCode)
}

type Scraper struct {
	client *http.Client
}

func New() *Scraper {
	return &Scraper{
		client: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// Fetch downloads the HTML at url and returns its content (capped at maxBodyBytes).
func (s *Scraper) Fetch(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", fmt.Errorf("page not found (status %d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == 498 { // Wildberries bot-detection code
		return "", &ErrAccessDenied{StatusCode: resp.StatusCode}
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}
