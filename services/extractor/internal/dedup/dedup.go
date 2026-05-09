package dedup

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "pricescount:scraped:"

// Store uses Redis SET with TTL to prevent scraping the same URL twice within a window.
type Store struct {
	client *redis.Client
	ttl    time.Duration
}

func NewStore(redisURL string, ttl time.Duration) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Store{client: redis.NewClient(opts), ttl: ttl}, nil
}

// IsDuplicate returns true if the URL was already scraped within the TTL window.
func (s *Store) IsDuplicate(ctx context.Context, url string) (bool, error) {
	res, err := s.client.Exists(ctx, key(url)).Result()
	if err != nil {
		return false, fmt.Errorf("redis exists: %w", err)
	}
	return res > 0, nil
}

// Mark records the URL as scraped. Expires after the configured TTL.
func (s *Store) Mark(ctx context.Context, url string) error {
	return s.client.Set(ctx, key(url), 1, s.ttl).Err()
}

func (s *Store) Close() error {
	return s.client.Close()
}

func key(url string) string {
	return keyPrefix + url
}
