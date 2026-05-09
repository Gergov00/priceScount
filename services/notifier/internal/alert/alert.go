package alert

import "log/slog"

type Alert struct {
	Channel     string
	UserID      string
	ProductID   string
	URL         string
	Price       float64
	Currency    string
	TargetPrice float64
}

// Fire logs a price drop alert. Telegram and email are mocked as structured
// log lines until real integrations are wired.
func Fire(a Alert) {
	slog.Info("PRICE ALERT",
		"channel", a.Channel,
		"user_id", a.UserID,
		"product_id", a.ProductID,
		"url", a.URL,
		"price", a.Price,
		"currency", a.Currency,
		"target_price", a.TargetPrice,
	)
}
