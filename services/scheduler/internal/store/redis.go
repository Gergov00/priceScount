package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// URLMeta holds a URL and its associated product ID, retrieved together when dispatching.
type URLMeta struct {
	URL       string
	ProductID string
}

// URLStore manages the URL pool using two Redis structures:
//   - Sorted set (setKey):  member=url, score=next_check_unix_timestamp
//   - Hash      (metaKey):  field=url,  value=product_id
type URLStore struct {
	client  *redis.Client
	setKey  string
	metaKey string
}

func NewURLStore(redisURL, setKey, metaKey string) (*URLStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &URLStore{
		client:  redis.NewClient(opts),
		setKey:  setKey,
		metaKey: metaKey,
	}, nil
}

func (s *URLStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Add registers a URL in the pool.
// ZADD NX ensures the URL is only added once (deduplication).
// The score is set to now so it gets checked on the very next tick.
func (s *URLStore) Add(ctx context.Context, url, productID string) error {
	added, err := s.client.ZAddNX(ctx, s.setKey, redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: url,
	}).Result()
	if err != nil {
		return fmt.Errorf("zadd: %w", err)
	}
	if added == 0 {
		slog.Debug("URL already in pool, skipping", "url", url)
		return nil
	}
	// Store product_id only for newly added URLs.
	if err := s.client.HSet(ctx, s.metaKey, url, productID).Err(); err != nil {
		return fmt.Errorf("hset meta: %w", err)
	}
	return nil
}

// DueURLs returns all URLs whose next check timestamp is ≤ now.
func (s *URLStore) DueURLs(ctx context.Context) ([]URLMeta, error) {
	now := fmt.Sprintf("%.0f", float64(time.Now().Unix()))
	urls, err := s.client.ZRangeByScore(ctx, s.setKey, &redis.ZRangeBy{
		Min: "0",
		Max: now,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("zrangebyscore: %w", err)
	}
	if len(urls) == 0 {
		return nil, nil
	}

	metas := make([]URLMeta, 0, len(urls))
	for _, url := range urls {
		productID, _ := s.client.HGet(ctx, s.metaKey, url).Result()
		metas = append(metas, URLMeta{URL: url, ProductID: productID})
	}
	return metas, nil
}

// Reschedule sets the next check time for a URL to now + interval.
func (s *URLStore) Reschedule(ctx context.Context, url string, interval time.Duration) error {
	next := float64(time.Now().Add(interval).Unix())
	return s.client.ZAdd(ctx, s.setKey, redis.Z{
		Score:  next,
		Member: url,
	}).Err()
}

func (s *URLStore) Close() error {
	return s.client.Close()
}
