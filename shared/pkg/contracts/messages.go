package contracts

import "time"

// DiscoveredURL is published to the discovery.urls queue when a user submits a product URL.
type DiscoveredURL struct {
	ProductID    string    `json:"product_id"`
	ProductName  string    `json:"product_name"`
	URL          string    `json:"url"`
	Platform     string    `json:"platform"`  // "wb" or "ozon"
	Source       string    `json:"source"`    // same as Platform, kept for DB compatibility
	DiscoveredAt time.Time `json:"discovered_at"`
}

// ScraperTask is published to the scraper.tasks queue by the Scheduler Service.
type ScraperTask struct {
	TaskID      string    `json:"task_id"`
	ProductID   string    `json:"product_id"`
	URL         string    `json:"url"`
	Platform    string    `json:"platform"`              // "wb" or "ozon"
	ScheduledAt time.Time `json:"scheduled_at"`
	Force       bool      `json:"force,omitempty"`
}

// PriceResult is published to the price.results queue by the Extractor Service.
type PriceResult struct {
	TaskID    string    `json:"task_id"`
	ProductID string    `json:"product_id"`
	URL       string    `json:"url"`
	Price     float64   `json:"price"`
	Currency  string    `json:"currency"`
	ScrapedAt time.Time `json:"scraped_at"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Force     bool      `json:"force,omitempty"`
}
