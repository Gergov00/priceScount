package platform

import (
	"fmt"
	"regexp"
	"strings"
)

var wbProductIDRe = regexp.MustCompile(`wildberries\.ru/catalog/(\d+)`)

// Detect returns "wb" based on the URL host. Only Wildberries is supported.
func Detect(rawURL string) (string, error) {
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "wildberries.ru") {
		return "wb", nil
	}
	return "", fmt.Errorf("unsupported platform: must be wildberries.ru")
}

// NormalizeWB extracts the canonical Wildberries product URL.
func NormalizeWB(rawURL string) (string, error) {
	m := wbProductIDRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", fmt.Errorf("no wildberries product id in url")
	}
	return "https://www.wildberries.ru/catalog/" + m[1] + "/detail.aspx", nil
}
