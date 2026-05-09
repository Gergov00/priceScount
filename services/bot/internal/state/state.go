package state

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	StepIdle            = "idle"
	StepSelectingURLs   = "selecting_urls"
	StepWaitingMinPrice = "waiting_min_price"
	StepWaitingMaxPrice = "waiting_max_price"
	StepEditingMinPrice = "editing_min_price"
	StepEditingMaxPrice = "editing_max_price"

	keyPrefix = "bot:state:"
	ttl       = 30 * time.Minute
)

type URLItem struct {
	URL    string `json:"url"`
	Source string `json:"source"`
	Price  string `json:"price"`
}

type Session struct {
	Step         string    `json:"step"`
	ProductID    string    `json:"product_id"`
	ProductName  string    `json:"product_name"`
	URLs         []URLItem `json:"urls"`
	SelectedIdxs []int     `json:"selected_idxs"`
	MinPrice     float64   `json:"min_price"`
	// used during edit flow
	EditingSubID string  `json:"editing_sub_id,omitempty"`
	OldMinPrice  float64 `json:"old_min_price,omitempty"`
	OldMaxPrice  float64 `json:"old_max_price,omitempty"`
}

type Store struct {
	client *redis.Client
}

func New(redisURL string) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Store{client: redis.NewClient(opts)}, nil
}

func (s *Store) Get(ctx context.Context, chatID int64) (*Session, error) {
	data, err := s.client.Get(ctx, key(chatID)).Bytes()
	if err == redis.Nil {
		return &Session{Step: StepIdle}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &sess, nil
}

func (s *Store) Set(ctx context.Context, chatID int64, sess *Session) error {
	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.client.Set(ctx, key(chatID), data, ttl).Err()
}

func (s *Store) Clear(ctx context.Context, chatID int64) error {
	return s.client.Del(ctx, key(chatID)).Err()
}

func key(chatID int64) string {
	return fmt.Sprintf("%s%d", keyPrefix, chatID)
}
