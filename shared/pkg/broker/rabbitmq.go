package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	QueueLookupTasks   = "lookup.tasks"
	QueueScraperTasks  = "scraper.tasks"
	QueuePriceResults  = "price.results"
	QueueTrackRequests = "track.requests"
	QueueNotifyTasks   = "notify.tasks"
)

// Connection wraps an AMQP connection and a default channel.
type Connection struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewConnection dials RabbitMQ and opens a default channel with prefetch=10.
func NewConnection(rawURL string) (*Connection, error) {
	c := &Connection{url: rawURL}
	return c, c.dial()
}

// ConnectWithRetry calls NewConnection up to maxAttempts times with linear backoff.
func ConnectWithRetry(rawURL string, maxAttempts int) (*Connection, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		c, err := NewConnection(rawURL)
		if err == nil {
			return c, nil
		}
		lastErr = err
		wait := time.Duration(i*2) * time.Second
		slog.Warn("rabbitmq not ready, retrying", "attempt", i, "max", maxAttempts, "wait", wait)
		time.Sleep(wait)
	}
	return nil, fmt.Errorf("rabbitmq: failed after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Connection) dial() error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}
	if err := ch.Qos(10, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("qos: %w", err)
	}
	c.conn = conn
	c.ch = ch
	return nil
}

// DeclareQueue declares a durable, non-auto-delete queue.
func (c *Connection) DeclareQueue(name string) error {
	_, err := c.ch.QueueDeclare(name, true, false, false, false, nil)
	return err
}

// Publish JSON-encodes v and publishes it as a persistent message to queue.
func (c *Connection) Publish(ctx context.Context, queue string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}
	return c.ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}

// Consume registers a consumer on queue using the default channel (prefetch=10).
// Messages must be acked/nacked by the caller.
func (c *Connection) Consume(queue, consumer string) (<-chan amqp.Delivery, error) {
	return c.ch.Consume(queue, consumer, false, false, false, false, nil)
}

// ConsumeWithPrefetch opens a dedicated channel with the given prefetch count and
// registers a consumer. Use this when different consumers need different prefetch
// values (e.g. lookup.tasks prefetch=1, scraper.tasks prefetch=2).
func (c *Connection) ConsumeWithPrefetch(queue, consumer string, prefetch int) (<-chan amqp.Delivery, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel for %s: %w", queue, err)
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("qos for %s: %w", queue, err)
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("declare queue %s: %w", queue, err)
	}
	deliveries, err := ch.Consume(queue, consumer, false, false, false, false, nil)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("consume %s: %w", queue, err)
	}
	return deliveries, nil
}

func (c *Connection) Close() {
	if c.ch != nil {
		c.ch.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
}
