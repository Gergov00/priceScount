package contracts

import "time"

// LookupTask is published to the lookup.tasks queue for a one-time product fetch.
type LookupTask struct {
	TaskID   string `json:"task_id"`
	LookupID string `json:"lookup_id"`
	URL      string `json:"url"`
	Platform string `json:"platform"` // "wb"
}

// ScraperTask is published to the scraper.tasks queue by the Scheduler for periodic monitoring.
type ScraperTask struct {
	TaskID      string    `json:"task_id"`
	ProductID   string    `json:"product_id"`
	URL         string    `json:"url"`
	Platform    string    `json:"platform"` // "wb"
	ScheduledAt time.Time `json:"scheduled_at"`
	Force       bool      `json:"force,omitempty"`
	ChatID      int64     `json:"chat_id,omitempty"` // set with Force: only this chat gets the result
}

// PriceResult is published to the price.results queue by the Extractor.
// Either LookupID or ProductID is set — never both.
type PriceResult struct {
	TaskID    string    `json:"task_id"`
	LookupID  string    `json:"lookup_id,omitempty"`  // set for lookup.tasks responses
	ProductID string    `json:"product_id,omitempty"` // set for scraper.tasks responses
	URL       string    `json:"url"`
	Name      string    `json:"name,omitempty"` // only populated for lookups
	Price     float64   `json:"price"`
	Currency  string    `json:"currency"`
	ScrapedAt time.Time `json:"scraped_at"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Force     bool      `json:"force,omitempty"`   // true when triggered by user force-check
	ChatID    int64     `json:"chat_id,omitempty"` // set with Force: only this chat gets the result
}

// TrackRequest is published to the track.requests queue to manage scheduled monitoring.
type TrackRequest struct {
	Action        string `json:"action"`                   // add | pause | resume | delete | force
	ProductID     string `json:"product_id"`
	URL           string `json:"url"`
	Platform      string `json:"platform"`
	IntervalHours int    `json:"interval_hours,omitempty"` // used with action=add
	ChatID        int64  `json:"chat_id,omitempty"`        // used with action=force: requester chat
}

// NotifyTask is published to the notify.tasks queue for delivery to the user.
type NotifyTask struct {
	Channel   string `json:"channel"`             // telegram | email | push
	Target    string `json:"target"`              // chat_id (as string), email, device token
	Text      string `json:"text"`
	Direction string `json:"direction,omitempty"` // up | down
}
