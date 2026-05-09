// Package main is the entry point for the Notification & Engine Service.
//
// Responsibilities:
//   - Consumes PriceResult messages from price.results.
//   - Upserts each result into price_history (PostgreSQL).
//   - Loads active subscriptions for the product; if new_price ≤ target_price, fires an alert.
//   - Alert channel is determined by subscriptions.notification_channel ("telegram" / "email").
//     Both channels are mocked as structured log lines until real integrations are wired.
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("notifier service: not yet implemented")
}
