package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestTimeout = 10 * time.Second

// ErrPermanent marks a notification error that cannot be fixed by retrying.
var ErrPermanent = errors.New("permanent telegram error")

// RetryError reports a transient Telegram error and any requested retry delay.
type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string {
	if e.Err == nil {
		return "transient telegram error"
	}
	return e.Err.Error()
}

func (e *RetryError) Unwrap() error { return e.Err }

// Option configures a TelegramSender.
type Option func(*TelegramSender)

// WithHTTPClient sets the HTTP client used for Telegram requests.
func WithHTTPClient(client *http.Client) Option {
	return func(s *TelegramSender) {
		if client != nil {
			s.client = client
		}
	}
}

// TelegramSender delivers messages to Telegram with bounded retry.
type TelegramSender struct {
	token  string
	client *http.Client
}

// NewTelegramSender creates a sender. Each request is bounded to ten seconds,
// including when the injected client has no timeout.
func NewTelegramSender(token string, opts ...Option) *TelegramSender {
	s := &TelegramSender{token: token, client: &http.Client{Timeout: requestTimeout}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Send posts one notification, retrying transient failures with cancellable waits.
func (s *TelegramSender) Send(ctx context.Context, chatID int64, text string) error {
	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	var lastErr error
	for attempt, delay := range delays {
		if err := wait(ctx, delay); err != nil {
			return err
		}
		lastErr = s.sendOnce(ctx, chatID, text)
		if lastErr == nil || errors.Is(lastErr, ErrPermanent) {
			return lastErr
		}
		var retryErr *RetryError
		if errors.As(lastErr, &retryErr) && retryErr.After > 30*time.Second {
			return lastErr
		}
		if errors.As(lastErr, &retryErr) && retryErr.After > 0 && attempt < len(delays)-1 {
			nextDelay := delays[attempt+1]
			if retryErr.After > nextDelay {
				if err := wait(ctx, retryErr.After-nextDelay); err != nil {
					return err
				}
			}
		}
	}
	return lastErr
}

func wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *TelegramSender) sendOnce(ctx context.Context, chatID int64, text string) error {
	body, err := json.Marshal(struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{chatID, text})
	if err != nil {
		return fmt.Errorf("encode telegram message: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := "https://api.telegram.org/bot" + s.token + "/sendMessage"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %s", sanitize(err.Error(), s.token))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return &RetryError{Err: safeTransportError(err, s.token)}
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if readErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &RetryError{Err: fmt.Errorf("read telegram response: %s", sanitize(readErr.Error(), s.token))}
	}
	if len(responseBody) > 4096 {
		responseBody = responseBody[:4096]
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		apiErr := fmt.Errorf("invalid telegram response (HTTP %d)", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("%w: %v", ErrPermanent, apiErr)
		}
		return &RetryError{Err: apiErr}
	}
	if resp.StatusCode == http.StatusOK && result.OK {
		return nil
	}
	description := sanitize(result.Description, s.token)
	if description == "" {
		description = "telegram request rejected"
	}
	apiErr := fmt.Errorf("telegram API HTTP %d: %s", resp.StatusCode, description)
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RetryError{After: time.Duration(result.Parameters.RetryAfter) * time.Second, Err: apiErr}
	}
	if resp.StatusCode >= 500 || resp.StatusCode < 400 {
		return &RetryError{Err: apiErr}
	}
	return fmt.Errorf("%w: %v", ErrPermanent, apiErr)
}

func safeTransportError(err error, token string) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("telegram transport: %s", sanitize(err.Error(), token))
}

func sanitize(message, token string) string {
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	return message
}
