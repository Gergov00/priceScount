package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	apiURL       = "https://api.groq.com/openai/v1/chat/completions"
	maxHTMLChars = 12000
)

type PriceResult struct {
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
}

type Extractor struct {
	apiKey string
	model  string
	client *http.Client
}

func New(apiKey, model string) *Extractor {
	return &Extractor{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *Extractor) Extract(ctx context.Context, html string) (PriceResult, error) {
	if e.apiKey == "" {
		return PriceResult{}, fmt.Errorf("LLM_API_KEY is not configured")
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":       e.model,
		"max_tokens":  256,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "user", "content": buildPrompt(truncate(html, maxHTMLChars))},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return PriceResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return PriceResult{}, fmt.Errorf("groq request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PriceResult{}, fmt.Errorf("groq returned status %d", resp.StatusCode)
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return PriceResult{}, fmt.Errorf("decode response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return PriceResult{}, fmt.Errorf("empty response from Groq")
	}

	raw := apiResp.Choices[0].Message.Content
	result, err := parseResult(raw)
	if err != nil {
		return PriceResult{}, fmt.Errorf("%w (llm said: %q)", err, raw)
	}
	return result, nil
}

func buildPrompt(html string) string {
	return `Extract the current sale price of the product from this HTML page.

Rules:
- Return ONLY a JSON object, nothing else.
- Format: {"price": 999.99, "currency": "USD"}
- currency must be a 3-letter ISO 4217 code (USD, EUR, RUB, etc.).
- If multiple prices exist, use the lowest current price (sale price over original).
- If no price is found, return {"price": 0, "currency": "", "error": "price not found"}.

HTML:
` + html
}

func parseResult(text string) (PriceResult, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 {
		return PriceResult{}, fmt.Errorf("no JSON in response: %q", text)
	}
	text = text[start : end+1]

	var result struct {
		Price    float64 `json:"price"`
		Currency string  `json:"currency"`
		Error    string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return PriceResult{}, fmt.Errorf("parse JSON: %w", err)
	}
	if result.Error != "" {
		return PriceResult{}, fmt.Errorf("LLM could not extract price: %s", result.Error)
	}
	if result.Price <= 0 {
		return PriceResult{}, fmt.Errorf("invalid price: %v", result.Price)
	}

	return PriceResult{Price: result.Price, Currency: strings.ToUpper(result.Currency)}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
