package jsonld

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var scriptRe = regexp.MustCompile(
	`(?i)<script[^>]+type=["']application/ld\+json["'][^>]*>([\s\S]*?)</script>`,
)

// Result holds the price extracted from JSON-LD structured data.
type Result struct {
	Price    float64
	Currency string
}

// Extract scans html for JSON-LD <script> blocks and returns the first
// product price it finds. Returns false if none found.
func Extract(html string) (Result, bool) {
	for _, match := range scriptRe.FindAllStringSubmatch(html, -1) {
		if len(match) < 2 {
			continue
		}
		if r, ok := parseBlock(strings.TrimSpace(match[1])); ok {
			return r, true
		}
	}
	return Result{}, false
}

func parseBlock(raw string) (Result, bool) {
	// Some pages put an array of schemas in one block.
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var schemas []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &schemas); err != nil {
			return Result{}, false
		}
		for _, s := range schemas {
			if r, ok := parseObject(s); ok {
				return r, true
			}
		}
		return Result{}, false
	}
	return parseObject(json.RawMessage(raw))
}

func parseObject(raw json.RawMessage) (Result, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Result{}, false
	}

	typ := stringField(obj, "@type")

	// Direct Offer / AggregateOffer block.
	if strings.Contains(strings.ToLower(typ), "offer") {
		return extractOffer(obj)
	}

	// Product block — price lives inside "offers".
	if strings.Contains(strings.ToLower(typ), "product") {
		offersRaw, ok := obj["offers"]
		if !ok {
			return Result{}, false
		}
		return parseOffers(offersRaw)
	}

	return Result{}, false
}

func parseOffers(raw json.RawMessage) (Result, bool) {
	// offers can be a single object or an array.
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return Result{}, false
		}
		for _, item := range arr {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(item, &obj); err != nil {
				continue
			}
			if r, ok := extractOffer(obj); ok {
				return r, true
			}
		}
		return Result{}, false
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Result{}, false
	}
	return extractOffer(obj)
}

// extractOffer reads price/lowPrice and priceCurrency from an Offer object.
func extractOffer(obj map[string]json.RawMessage) (Result, bool) {
	currency := stringField(obj, "priceCurrency")

	// Try "price" first, then "lowPrice" (AggregateOffer).
	for _, field := range []string{"price", "lowPrice"} {
		price, ok := parsePrice(obj, field)
		if ok && price > 0 {
			return Result{Price: price, Currency: strings.ToUpper(currency)}, true
		}
	}
	return Result{}, false
}

func parsePrice(obj map[string]json.RawMessage, field string) (float64, bool) {
	raw, ok := obj[field]
	if !ok {
		return 0, false
	}

	// Price can be a JSON number or a quoted string.
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.ReplaceAll(s, ",", ".")
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func stringField(obj map[string]json.RawMessage, key string) string {
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
