package marketplace

import (
	"fmt"
	"strings"
)

// Product holds the basic info fetched from a marketplace.
type Product struct {
	Name     string
	Price    float64
	Currency string
}

// DetectPlatform returns "wb" or "ozon" based on the URL host.
func DetectPlatform(rawURL string) (string, error) {
	lower := strings.ToLower(rawURL)
	switch {
	case strings.Contains(lower, "wildberries.ru"):
		return "wb", nil
	case strings.Contains(lower, "ozon.ru"):
		return "ozon", nil
	}
	return "", fmt.Errorf("unsupported platform: must be wildberries.ru or ozon.ru")
}
