package nextdata

import (
	"encoding/json"
	"regexp"
	"strings"
)

var scriptRe = regexp.MustCompile(
	`(?i)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>([\s\S]*?)</script>`,
)

// priceKeys are JSON field names that typically hold a product price.
var priceKeys = []string{
	"price", "finalPrice", "salePrice", "currentPrice", "sellPrice",
	"minPrice", "basePrice", "discountedPrice", "offerPrice",
}

// currencyKeys are JSON field names that typically hold a currency code.
var currencyKeys = []string{"currency", "priceCurrency", "currencyCode"}

// Result holds the extracted price and currency.
type Result struct {
	Price    float64
	Currency string
}

// Extract looks for a Next.js __NEXT_DATA__ JSON block in html and recursively
// searches it for a product price. Returns false if not found.
func Extract(html string) (Result, bool) {
	match := scriptRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return Result{}, false
	}
	raw := strings.TrimSpace(match[1])

	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return Result{}, false
	}

	price, currency := searchJSON(data)
	if price <= 0 {
		return Result{}, false
	}
	if currency == "" {
		currency = "RUB"
	}
	return Result{Price: price, Currency: strings.ToUpper(currency)}, true
}

// searchJSON recursively walks arbitrary JSON looking for price/currency fields.
func searchJSON(v any) (price float64, currency string) {
	switch node := v.(type) {
	case map[string]any:
		// Collect currency from this object if present.
		for _, key := range currencyKeys {
			if s, ok := node[key].(string); ok && s != "" {
				currency = s
				break
			}
		}
		// Check price fields in this object.
		for _, key := range priceKeys {
			if p := toFloat(node[key]); p > 0 {
				return p, currency
			}
		}
		// Recurse into child objects.
		for _, child := range node {
			if p, c := searchJSON(child); p > 0 {
				if currency == "" {
					currency = c
				}
				return p, currency
			}
		}
	case []any:
		for _, item := range node {
			if p, c := searchJSON(item); p > 0 {
				return p, c
			}
		}
	}
	return 0, ""
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		var f float64
		if err := json.Unmarshal([]byte(n), &f); err == nil {
			return f
		}
	}
	return 0
}
