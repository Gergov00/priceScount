package contracts

import "time"

// DiscoveredURL is published to the discovery.urls queue by the Discovery Service.
type DiscoveredURL struct {
	ProductID    string    `json:"product_id"`
	ProductName  string    `json:"product_name"`
	URL          string    `json:"url"`
	Source       string    `json:"source"` // bare domain, e.g. "amazon.com"
	DiscoveredAt time.Time `json:"discovered_at"`
}

// ScraperTask is published to the scraper.tasks queue by the Scheduler Service.
type ScraperTask struct {
	TaskID      string    `json:"task_id"`
	ProductID   string    `json:"product_id"`
	URL         string    `json:"url"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

// PriceResult is published to the price.results queue by the Extractor Service.
type PriceResult struct {
	TaskID    string    `json:"task_id"`
	ProductID string    `json:"product_id"`
	URL       string    `json:"url"`
	Price     float64   `json:"price"`
	Currency  string    `json:"currency"` // ISO 4217, e.g. "USD"
	ScrapedAt time.Time `json:"scraped_at"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}
