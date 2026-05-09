package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Item struct {
	URL    string `json:"url"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Price  string `json:"price"`
}

type Result struct {
	ProductID string `json:"product_id"`
	Items     []Item `json:"items"`
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Discover(ctx context.Context, productName string) (*Result, error) {
	body, _ := json.Marshal(map[string]string{
		"product_name": productName,
		"locale":       "ru",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/discover", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery returned %d", resp.StatusCode)
	}

	var result Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}
