package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	LocaleRU  = "ru"
	LocaleUS  = "us"
	LocaleAll = "all"
)

type Result struct {
	URL    string
	Source string // shop name or domain, e.g. "Amazon", "ozon.ru"
	Title  string
	Price  string // raw price string from Google Shopping, e.g. "₽89 990"
}

type Agent struct {
	serperKey string
	client    *http.Client
}

func New(serperKey string) *Agent {
	return &Agent{
		serperKey: serperKey,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Discover returns up to 10 product URLs from Google Shopping (all internet, no whitelist).
// locale: "ru", "us", or "all" (parallel search in both markets). Empty defaults to "all".
func (a *Agent) Discover(ctx context.Context, productName, locale string) ([]Result, error) {
	if a.serperKey == "" {
		return nil, fmt.Errorf("SERPER_API_KEY is not configured")
	}
	if locale == "" {
		locale = LocaleAll
	}

	switch locale {
	case LocaleRU:
		return a.discoverLocale(ctx, productName, "ru", "ru")
	case LocaleUS:
		return a.discoverLocale(ctx, productName, "us", "en")
	case LocaleAll:
		return a.discoverAll(ctx, productName)
	default:
		return nil, fmt.Errorf("unsupported locale %q: use \"ru\", \"us\", or \"all\"", locale)
	}
}

// discoverAll runs RU and US searches in parallel and merges results.
// If one market fails, results from the other are still returned.
func (a *Agent) discoverAll(ctx context.Context, productName string) ([]Result, error) {
	type outcome struct {
		results []Result
		err     error
	}

	ch := make(chan outcome, 2)
	var wg sync.WaitGroup

	for _, loc := range []struct{ gl, hl string }{{"ru", "ru"}, {"us", "en"}} {
		wg.Add(1)
		go func(gl, hl string) {
			defer wg.Done()
			r, err := a.discoverLocale(ctx, productName, gl, hl)
			ch <- outcome{r, err}
		}(loc.gl, loc.hl)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	seen := make(map[string]struct{})
	merged := make([]Result, 0, 20)
	var lastErr error

	for out := range ch {
		if out.err != nil {
			lastErr = out.err
			continue
		}
		for _, r := range out.results {
			if _, dup := seen[r.URL]; dup {
				continue
			}
			seen[r.URL] = struct{}{}
			merged = append(merged, r)
		}
	}

	if len(merged) == 0 && lastErr != nil {
		return nil, lastErr
	}
	if len(merged) > 10 {
		merged = merged[:10]
	}
	return merged, nil
}

func (a *Agent) discoverLocale(ctx context.Context, productName, gl, hl string) ([]Result, error) {
	// Primary: Google Shopping — results are inherently product pages from any shop.
	results, err := a.shoppingSearch(ctx, productName, gl, hl)
	if err != nil || len(results) < 3 {
		// Fallback to organic search if Shopping returns too few results.
		fallback, ferr := a.organicSearch(ctx, productName, gl, hl)
		if ferr == nil && len(fallback) > len(results) {
			results = fallback
		}
	}
	if len(results) > 10 {
		results = results[:10]
	}
	return results, nil
}

// shoppingSearch queries the Serper /shopping endpoint.
// All returned URLs are product pages — no further filtering needed.
func (a *Agent) shoppingSearch(ctx context.Context, query, gl, hl string) ([]Result, error) {
	payload := map[string]any{"q": query, "gl": gl, "hl": hl, "num": 20}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/shopping", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", a.serperKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serper/shopping returned status %d", resp.StatusCode)
	}

	var sr struct {
		Shopping []struct {
			Title  string `json:"title"`
			Link   string `json:"link"`
			Price  string `json:"price"`
			Source string `json:"source"` // shop name, e.g. "Amazon", "Ozon"
		} `json:"shopping"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	seen := make(map[string]struct{})
	results := make([]Result, 0, len(sr.Shopping))
	for _, item := range sr.Shopping {
		if item.Link == "" || strings.HasPrefix(item.Link, "https://www.google.com/") {
			// Google Shopping redirect — not a scrapable merchant URL.
			continue
		}
		if _, dup := seen[item.Link]; dup {
			continue
		}
		seen[item.Link] = struct{}{}
		results = append(results, Result{
			URL:    item.Link,
			Source: item.Source,
			Title:  item.Title,
			Price:  item.Price,
		})
	}
	return results, nil
}

// organicSearch is a fallback that uses the regular /search endpoint
// and applies a basic heuristic to keep likely product pages.
func (a *Agent) organicSearch(ctx context.Context, query, gl, hl string) ([]Result, error) {
	var q string
	if hl == "ru" {
		q = fmt.Sprintf("%s купить цена", query)
	} else {
		q = fmt.Sprintf("buy %s price", query)
	}
	payload := map[string]any{"q": q, "gl": gl, "hl": hl, "num": 20}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", a.serperKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serper/search returned status %d", resp.StatusCode)
	}

	var sr struct {
		Organic []struct {
			Title string `json:"title"`
			Link  string `json:"link"`
		} `json:"organic"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	seen := make(map[string]struct{})
	results := make([]Result, 0, 10)
	for _, item := range sr.Organic {
		if _, dup := seen[item.Link]; dup {
			continue
		}
		seen[item.Link] = struct{}{}
		if isNonShopURL(item.Link) || isCatalogURL(item.Link) {
			continue
		}
		results = append(results, Result{
			URL:    item.Link,
			Source: domainOf(item.Link),
			Title:  item.Title,
		})
	}
	return results, nil
}

// isNonShopURL filters out pages that are clearly not product listings.
var nonShopKeywords = []string{
	"wikipedia.org", "reddit.com", "youtube.com", "facebook.com",
	"twitter.com", "instagram.com", "tiktok.com",
	"gsmarena.com", "rtings.com", "techradar.com", "theverge.com",
	"tomshardware.com", "anandtech.com", "ixbt.com", "3dnews.ru",
	"obzor", "review", "otzyv",
}

func isNonShopURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, kw := range nonShopKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// catalogSegments are URL path segments that indicate a category/listing page.
var catalogSegments = []string{
	"/catalog/", "/category/", "/categories/", "/collection/",
	"/collections/", "/cat/", "/c/", "/search", "?q=", "&q=",
	"/brand/", "/brands/", "/tag/", "/tags/", "/product-tag/",
	"/filter/", "/sort/", "/page/",
}

func isCatalogURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, seg := range catalogSegments {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	// URL ends with just a domain or a very short path — likely a homepage or brand page.
	path := lower
	if i := strings.Index(path, "://"); i != -1 {
		path = path[i+3:]
	}
	if i := strings.IndexByte(path, '/'); i != -1 {
		path = path[i:]
	} else {
		return true // no path at all
	}
	// Strip trailing slash and check if path is empty or just "/"
	if path == "/" || path == "" {
		return true
	}
	return false
}

func domainOf(rawURL string) string {
	// Quick domain extraction without net/url overhead.
	s := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexByte(s, '/'); i != -1 {
		s = s[:i]
	}
	return strings.ToLower(s)
}
