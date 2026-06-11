package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client is an HTTP client for the Gateway REST API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		base:  baseURL,
		token: token,
		// Every Gateway endpoint is fast (lookups are async, polled via GET);
		// a short timeout keeps a stuck Gateway from blocking the bot.
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// ─── Lookup ──────────────────────────────────────────────────────────────────

type LookupResponse struct {
	LookupID string `json:"lookup_id"`
}

func (c *Client) StartLookup(ctx context.Context, rawURL string) (string, error) {
	var resp LookupResponse
	if err := c.post(ctx, "/lookup", map[string]string{"url": rawURL}, &resp); err != nil {
		return "", fmt.Errorf("start lookup: %w", err)
	}
	return resp.LookupID, nil
}

type LookupResult struct {
	Status string  `json:"status"` // pending | done | failed
	Name   string  `json:"name"`
	Price  float64 `json:"price"`
	URL    string  `json:"url"`
	Error  string  `json:"error"`
}

// PollLookup polls GET /lookup/:id every 2s until done/failed or ctx expires.
func (c *Client) PollLookup(ctx context.Context, lookupID string) (*LookupResult, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("lookup timed out")
		case <-ticker.C:
			var result LookupResult
			if err := c.get(ctx, "/lookup/"+lookupID, &result); err != nil {
				return nil, fmt.Errorf("poll lookup: %w", err)
			}
			if result.Status == "done" || result.Status == "failed" {
				return &result, nil
			}
		}
	}
}

// ─── Subscriptions ────────────────────────────────────────────────────────────

type Subscription struct {
	ID          string   `json:"ID"`
	ProductID   string   `json:"ProductID"`
	ProductName string   `json:"ProductName"`
	ProductURL  string   `json:"ProductURL"`
	MinPrice    *float64 `json:"MinPrice"`
	MaxPrice    *float64 `json:"MaxPrice"`
	Paused      bool     `json:"Paused"`
}

type CreateSubscriptionRequest struct {
	ChatID   int64   `json:"chat_id"`
	LookupID string  `json:"lookup_id"`
	MinPrice float64 `json:"min_price"`
	MaxPrice float64 `json:"max_price"`
}

type CreateSubscriptionResponse struct {
	SubscriptionID string `json:"subscription_id"`
	ProductID      string `json:"product_id"`
}

func (c *Client) CreateSubscription(ctx context.Context, req CreateSubscriptionRequest) (*CreateSubscriptionResponse, error) {
	var resp CreateSubscriptionResponse
	if err := c.post(ctx, "/subscriptions", req, &resp); err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	return &resp, nil
}

func (c *Client) ListSubscriptions(ctx context.Context, chatID int64) ([]Subscription, error) {
	var subs []Subscription
	path := "/subscriptions?chat_id=" + url.QueryEscape(strconv.FormatInt(chatID, 10))
	if err := c.get(ctx, path, &subs); err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	return subs, nil
}

func (c *Client) PauseSubscription(ctx context.Context, chatID int64, subID string) error {
	return c.patchAction(ctx, chatID, subID, "pause", nil, nil)
}

func (c *Client) ResumeSubscription(ctx context.Context, chatID int64, subID string) error {
	return c.patchAction(ctx, chatID, subID, "resume", nil, nil)
}

func (c *Client) EditSubscription(ctx context.Context, chatID int64, subID string, minPrice, maxPrice float64) error {
	return c.patchAction(ctx, chatID, subID, "edit", &minPrice, &maxPrice)
}

func (c *Client) DeleteSubscription(ctx context.Context, chatID int64, subID string) error {
	return c.do(ctx, http.MethodDelete, "/subscriptions/"+subID, map[string]int64{"chat_id": chatID}, nil)
}

func (c *Client) ForceCheck(ctx context.Context, chatID int64, subID string) error {
	return c.post(ctx, "/subscriptions/"+subID+"/check", map[string]int64{"chat_id": chatID}, nil)
}

type PricePoint struct {
	Price     float64   `json:"Price"`
	Currency  string    `json:"Currency"`
	ScrapedAt time.Time `json:"ScrapedAt"`
}

func (c *Client) GetHistory(ctx context.Context, chatID int64, subID string) ([]PricePoint, error) {
	var points []PricePoint
	path := fmt.Sprintf("/subscriptions/%s/history?chat_id=%d", subID, chatID)
	if err := c.get(ctx, path, &points); err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	return points, nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *Client) patchAction(ctx context.Context, chatID int64, subID, action string, minPrice, maxPrice *float64) error {
	body := map[string]any{"chat_id": chatID, "action": action}
	if minPrice != nil {
		body["min_price"] = *minPrice
	}
	if maxPrice != nil {
		body["max_price"] = *maxPrice
	}
	return c.do(ctx, http.MethodPatch, "/subscriptions/"+subID, body, nil)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Internal-Token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "" {
			return fmt.Errorf("gateway error %d: %s", resp.StatusCode, errBody.Error)
		}
		return fmt.Errorf("gateway error %d", resp.StatusCode)
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
