package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// ─── Lookup requests ─────────────────────────────────────────────────────────

type LookupStatus string

const (
	LookupPending LookupStatus = "pending"
	LookupDone    LookupStatus = "done"
	LookupFailed  LookupStatus = "failed"
)

type LookupResult struct {
	ID     string
	URL    string
	Status LookupStatus
	Name   string
	Price  float64
	Error  string
}

func (s *Store) GetLookup(ctx context.Context, id string) (*LookupResult, error) {
	var r LookupResult
	var name, errStr *string
	var price *float64
	err := s.db.QueryRow(ctx,
		`SELECT id, url, status, name, price, error FROM lookup_requests WHERE id = $1`,
		id,
	).Scan(&r.ID, &r.URL, &r.Status, &name, &price, &errStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get lookup %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get lookup: %w", err)
	}
	if name != nil {
		r.Name = *name
	}
	if price != nil {
		r.Price = *price
	}
	if errStr != nil {
		r.Error = *errStr
	}
	return &r, nil
}

func (s *Store) CompleteLookup(ctx context.Context, lookupID, name string, price float64) error {
	// Completed lookups get a longer TTL: the user may take a while to answer
	// the min/max price questions before the subscription is created.
	_, err := s.db.Exec(ctx,
		`UPDATE lookup_requests
		 SET status='done', name=$2, price=$3, expires_at = NOW() + INTERVAL '1 hour'
		 WHERE id=$1`,
		lookupID, name, price,
	)
	if err != nil {
		return fmt.Errorf("complete lookup: %w", err)
	}
	return nil
}

func (s *Store) FailLookup(ctx context.Context, lookupID, errMsg string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE lookup_requests SET status='failed', error=$2 WHERE id=$1`,
		lookupID, errMsg,
	)
	if err != nil {
		return fmt.Errorf("fail lookup: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpiredLookups(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM lookup_requests WHERE expires_at < NOW()`,
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired lookups: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PendingLookupCount returns the number of lookups still waiting for the extractor.
// Used as backpressure: each lookup costs a headless-Chrome page load.
func (s *Store) PendingLookupCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM lookup_requests WHERE status='pending'`,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("pending lookup count: %w", err)
	}
	return n, nil
}

// ─── Users ───────────────────────────────────────────────────────────────────

func (s *Store) UpsertUser(ctx context.Context, chatID int64) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO users(chat_id) VALUES($1)
		 ON CONFLICT(chat_id) DO UPDATE SET chat_id=EXCLUDED.chat_id
		 RETURNING id`,
		chatID,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert user: %w", err)
	}
	return id, nil
}

// UserIDByChatID resolves an existing user without creating one.
// Read-only endpoints use this so unauthenticated chat_id probing
// does not fill the users table.
func (s *Store) UserIDByChatID(ctx context.Context, chatID int64) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM users WHERE chat_id=$1`, chatID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("user by chat_id %d: %w", chatID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("user by chat_id: %w", err)
	}
	return id, nil
}

// ─── Products ────────────────────────────────────────────────────────────────

// EnsureProduct inserts a product with the given candidate id, or — when a row
// with the same URL already exists — refreshes its name and returns the existing
// id. One atomic statement, so two concurrent subscriptions to the same new URL
// cannot race into a foreign-key violation.
func (s *Store) EnsureProduct(ctx context.Context, candidateID, name, url, platform string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO products(id, name, url, platform) VALUES($1, $2, $3, $4)
		 ON CONFLICT(url) DO UPDATE SET name=EXCLUDED.name
		 RETURNING id`,
		candidateID, name, url, platform,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure product: %w", err)
	}
	return id, nil
}

// ─── Subscriptions ────────────────────────────────────────────────────────────

type Subscription struct {
	ID          string
	ProductID   string
	ProductName string
	ProductURL  string
	MinPrice    *float64
	MaxPrice    *float64
	Paused      bool
}

func (s *Store) UserSubscriptions(ctx context.Context, userID string) ([]Subscription, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s.id, s.product_id, p.name, p.url, s.min_price, s.max_price, s.paused
		 FROM subscriptions s
		 JOIN products p ON p.id = s.product_id
		 WHERE s.user_id = $1 AND s.active = true
		 ORDER BY s.created_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.ProductID, &sub.ProductName, &sub.ProductURL,
			&sub.MinPrice, &sub.MaxPrice, &sub.Paused); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (s *Store) GetSubscription(ctx context.Context, subID, userID string) (*Subscription, error) {
	var sub Subscription
	err := s.db.QueryRow(ctx,
		`SELECT s.id, s.product_id, p.name, p.url, s.min_price, s.max_price, s.paused
		 FROM subscriptions s
		 JOIN products p ON p.id = s.product_id
		 WHERE s.id = $1 AND s.user_id = $2 AND s.active = true`,
		subID, userID,
	).Scan(&sub.ID, &sub.ProductID, &sub.ProductName, &sub.ProductURL,
		&sub.MinPrice, &sub.MaxPrice, &sub.Paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get subscription %s: %w", subID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return &sub, nil
}

// ─── Price history ────────────────────────────────────────────────────────────

type PricePoint struct {
	Price     float64
	Currency  string
	ScrapedAt time.Time
}

// DeleteOldPriceHistory removes price points older than the retention window.
func (s *Store) DeleteOldPriceHistory(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM price_history WHERE scraped_at < NOW() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())),
	)
	if err != nil {
		return 0, fmt.Errorf("delete old price history: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) PriceHistory(ctx context.Context, productID string, limit int) ([]PricePoint, error) {
	rows, err := s.db.Query(ctx,
		`SELECT price, currency, scraped_at FROM price_history
		 WHERE product_id=$1 ORDER BY scraped_at DESC LIMIT $2`,
		productID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("price history: %w", err)
	}
	defer rows.Close()

	var points []PricePoint
	for rows.Next() {
		var p PricePoint
		if err := rows.Scan(&p.Price, &p.Currency, &p.ScrapedAt); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}
