package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ozonProductIDRe      = regexp.MustCompile(`ozon\.ru/product/(?:[^/?#]*?-)?(\d{5,})`)
	ozonPriceRe          = regexp.MustCompile(`"finalPrice"\s*:\s*"([\d\s\x{00a0}]+)`)
	ozonPriceIntRe       = regexp.MustCompile(`"price"\s*:\s*(\d{3,})`)
	ozonFinalPriceIntRe  = regexp.MustCompile(`"finalPrice"\s*:\s*(\d{3,})`)
	ozonNameRe           = regexp.MustCompile(`"name"\s*:\s*"([^"]{5,})"`)
	ozonH1Re             = regexp.MustCompile(`<h1[^>]*>([^<]+)</h1>`)
)

type OzonClient struct {
	http *http.Client
}

func NewOzonClient() *OzonClient {
	jar, _ := cookiejar.New(nil) // cookiejar.New(nil) never returns an error
	return &OzonClient{
		http: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				if len(via) > 0 {
					req.Header = via[0].Header.Clone()
				}
				return nil
			},
		},
	}
}

func NormalizeOzonURL(rawURL string) (string, error) {
	if !ozonProductIDRe.MatchString(rawURL) {
		return "", fmt.Errorf("no ozon product id in url")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	u.RawQuery = ""
	u.Fragment = ""
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func (c *OzonClient) FetchProduct(ctx context.Context, rawURL string) (*Product, error) {
	pageURL, err := NormalizeOzonURL(rawURL)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("parse normalized url: %w", err)
	}
	apiURL := "https://api.ozon.ru/composer-api.bx/page/json/v2?url=" + url.QueryEscape(u.Path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "ozone/3.61.0 (android 11; SDK 30; x86; google Android SDK built for x86; ru)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "ru-RU")
	req.Header.Set("x-o3-app-name", "ozonapp_android")
	req.Header.Set("x-o3-app-version", "3.61.0")
	req.Header.Set("x-o3-device-type", "mobile")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ozon fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ozon returned %d — may need session/proxy", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return extractOzonFromResponse(string(body))
}

func extractOzonFromResponse(body string) (*Product, error) {
	if p, ok := extractOzonFromWidgetStates(body); ok {
		return p, nil
	}

	name := extractOzonName(body)
	price := extractOzonPrice(body)

	if price == 0 {
		return nil, fmt.Errorf("price not found in ozon response")
	}
	if name == "" {
		name = "Ozon товар"
	}
	return &Product{Name: name, Price: price, Currency: "RUB"}, nil
}

// extractOzonFromWidgetStates parses Ozon's composer-api JSON.
// widgetStates values are often double-encoded JSON strings.
func extractOzonFromWidgetStates(body string) (*Product, bool) {
	var resp struct {
		WidgetStates map[string]json.RawMessage `json:"widgetStates"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, false
	}

	var bestName string
	var bestPrice float64

	for _, raw := range resp.WidgetStates {
		str := string(raw)
		if len(str) < 20 {
			continue
		}
		if str[0] == '"' {
			var inner string
			if json.Unmarshal(raw, &inner) == nil {
				str = inner
			}
		}

		if price := extractOzonPrice(str); price > 0 && (bestPrice == 0 || price < bestPrice) {
			bestPrice = price
			if name := extractOzonName(str); name != "" {
				bestName = name
			}
		}
	}

	if bestPrice == 0 {
		return nil, false
	}
	if bestName == "" {
		bestName = "Ozon товар"
	}
	return &Product{Name: bestName, Price: bestPrice, Currency: "RUB"}, true
}

func extractOzonPrice(s string) float64 {
	if m := ozonPriceRe.FindStringSubmatch(s); m != nil {
		raw := strings.NewReplacer(" ", "", " ", "").Replace(strings.TrimSpace(m[1]))
		if p, err := strconv.ParseFloat(raw, 64); err == nil && p > 0 {
			return p
		}
	}
	if m := ozonFinalPriceIntRe.FindStringSubmatch(s); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 0 {
			return p
		}
	}
	if m := ozonPriceIntRe.FindStringSubmatch(s); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

func extractOzonName(s string) string {
	if m := ozonNameRe.FindStringSubmatch(s); m != nil {
		candidate := strings.TrimSpace(m[1])
		if len([]rune(candidate)) > 5 && !strings.HasPrefix(candidate, "http") {
			return candidate
		}
	}
	if m := ozonH1Re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
