package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pg ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// SavePrice upserts products + tracked_urls and inserts a price_history row.
func (s *Store) SavePrice(ctx context.Context, productID, url string, price float64, currency string, scrapedAt time.Time) error {
	// Upsert product — name is seeded from product_id until discovery writes the real name.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO products(id, name) VALUES($1, $2)
		ON CONFLICT (id) DO NOTHING
	`, productID, productID); err != nil {
		return fmt.Errorf("upsert product: %w", err)
	}

	// Upsert tracked_url → get its id.
	var urlID string
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO tracked_urls(product_id, url, source, last_checked_at)
		VALUES($1, $2, $3, NOW())
		ON CONFLICT (url) DO UPDATE SET last_checked_at = NOW()
		RETURNING id
	`, productID, url, domainOf(url)).Scan(&urlID); err != nil {
		return fmt.Errorf("upsert tracked_url: %w", err)
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO price_history(url_id, price, currency, scraped_at)
		VALUES($1, $2, $3, $4)
	`, urlID, price, currency, scrapedAt); err != nil {
		return fmt.Errorf("insert price_history: %w", err)
	}

	return nil
}

type Subscription struct {
	ID          string
	UserID      string
	Channel     string
	TargetPrice float64
}

// TriggeredSubscriptions returns active subscriptions for productID where
// target_price >= currentPrice (i.e. the current price is at or below the target).
func (s *Store) TriggeredSubscriptions(ctx context.Context, productID string, currentPrice float64) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, notification_channel, target_price
		FROM subscriptions
		WHERE product_id = $1
		  AND active = true
		  AND target_price >= $2
	`, productID, currentPrice)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.Channel, &sub.TargetPrice); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func domainOf(rawURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexByte(s, '/'); i != -1 {
		s = s[:i]
	}
	return strings.ToLower(s)
}
