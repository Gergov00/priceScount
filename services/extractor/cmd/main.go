// Package main is the entry point for the Extractor Service.
//
// Responsibilities:
//   - Consumes ScraperTask messages from scraper.tasks.
//   - Checks Redis: if the URL was already scraped within its check interval, nack+drop.
//   - Fetches the product page HTML with a browser-like User-Agent.
//   - Sends the relevant HTML fragment (or full page) to an LLM with a structured prompt
//     to extract the numeric price and ISO currency code.
//   - Publishes a PriceResult (success or failure) to price.results.
//   - On success, writes the URL+timestamp to Redis to guard against re-scraping.
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("extractor service: not yet implemented")
}
