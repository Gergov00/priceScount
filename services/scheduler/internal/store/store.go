package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type URLEntry struct {
	ProductID string
	URL       string
	Platform  string
}

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// Add inserts or reactivates a URL in scheduled_urls. On conflict it reactivates the entry
// and resets next_check_at to NOW() so the scheduler picks it up on the next tick.
func (s *Store) Add(ctx context.Context, productID, url, platform string, intervalHours int) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO scheduled_urls(product_id, url, platform, next_check_at, check_interval_hours)
		 VALUES($1, $2, $3, NOW(), $4)
		 ON CONFLICT(url) DO UPDATE SET
		     active = true,
		     next_check_at = NOW()`,
		productID, url, platform, intervalHours,
	)
	if err != nil {
		return fmt.Errorf("add url: %w", err)
	}
	return nil
}

// SetActive enables or disables monitoring for a URL.
func (s *Store) SetActive(ctx context.Context, url string, active bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE scheduled_urls SET active=$2 WHERE url=$1`,
		url, active,
	)
	return err
}

// SetNextCheck sets next_check_at to NOW() so the URL is picked up on the next tick.
func (s *Store) SetNextCheck(ctx context.Context, url string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE scheduled_urls SET next_check_at=NOW(), active=true WHERE url=$1`,
		url,
	)
	return err
}

// AdvanceNextCheck moves next_check_at forward by the configured interval so the
// tick does not immediately redispatch after a force-publish.
func (s *Store) AdvanceNextCheck(ctx context.Context, url string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE scheduled_urls
		 SET next_check_at = NOW() + (check_interval_hours * interval '1 hour'), active = true
		 WHERE url = $1`,
		url,
	)
	return err
}

// Delete removes a URL from the schedule entirely.
func (s *Store) Delete(ctx context.Context, url string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM scheduled_urls WHERE url=$1`, url)
	return err
}

// DueURLs returns all active URLs whose next_check_at is in the past,
// locking them with SKIP LOCKED to prevent double-dispatch across replicas.
// It immediately reschedules them to avoid re-selection on the next tick.
func (s *Store) DueURLs(ctx context.Context, interval time.Duration) ([]URLEntry, error) {
	rows, err := s.db.Query(ctx,
		`UPDATE scheduled_urls
		 SET next_check_at = NOW() + $1::interval
		 WHERE id IN (
		     SELECT id FROM scheduled_urls
		     WHERE next_check_at <= NOW() AND active = true
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING product_id, url, platform`,
		fmt.Sprintf("%d seconds", int(interval.Seconds())),
	)
	if err != nil {
		return nil, fmt.Errorf("due urls: %w", err)
	}
	defer rows.Close()

	var entries []URLEntry
	for rows.Next() {
		var e URLEntry
		if err := rows.Scan(&e.ProductID, &e.URL, &e.Platform); err != nil {
			return nil, fmt.Errorf("scan url entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
