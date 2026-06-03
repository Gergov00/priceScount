package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
)

// Consumer runs two goroutines consuming from separate queues:
//   - lookup.tasks  (prefetch=1): one-time product fetches from Gateway
//   - scraper.tasks (prefetch=2): periodic monitoring from Scheduler
type Consumer struct {
	conn *broker.Connection
	wb   *marketplace.WBClient
	pub  *publisher
}

func New(conn *broker.Connection, wb *marketplace.WBClient) *Consumer {
	return &Consumer{
		conn: conn,
		wb:   wb,
		pub:  &publisher{conn: conn},
	}
}

// Run starts both consumer goroutines and blocks until ctx is cancelled or either fails.
func (c *Consumer) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := c.runQueue(ctx, broker.QueueLookupTasks, "extractor-lookup", 1); err != nil {
			errCh <- fmt.Errorf("lookup consumer: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := c.runQueue(ctx, broker.QueueScraperTasks, "extractor-scraper", 2); err != nil {
			errCh <- fmt.Errorf("scraper consumer: %w", err)
		}
	}()

	go func() {
		wg.Wait()
		close(errCh)
	}()

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Consumer) runQueue(ctx context.Context, queue, consumerTag string, prefetch int) error {
	deliveries, err := c.conn.ConsumeWithPrefetch(queue, consumerTag, prefetch)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}
	slog.Info("extractor consumer started", "queue", queue, "prefetch", prefetch)

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed: %s", queue)
			}
			c.handle(ctx, d, queue)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery, queue string) {
	switch queue {
	case broker.QueueLookupTasks:
		c.handleLookup(ctx, d)
	case broker.QueueScraperTasks:
		c.handleScraper(ctx, d)
	}
}

func (c *Consumer) handleLookup(ctx context.Context, d amqp.Delivery) {
	var task contracts.LookupTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed lookup task, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("task_id", task.TaskID, "lookup_id", task.LookupID, "url", task.URL)

	product, err := c.fetch(ctx, task.Platform, task.URL)
	result := contracts.PriceResult{
		TaskID:   task.TaskID,
		LookupID: task.LookupID,
		URL:      task.URL,
		ScrapedAt: time.Now().UTC(),
	}
	if err != nil {
		log.Error("lookup fetch failed", "error", err)
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
		result.Name = product.Name
		result.Price = product.Price
		result.Currency = product.Currency
		log.Info("lookup fetch completed", "name", product.Name, "price", product.Price)
	}

	if err := c.pub.publish(ctx, result); err != nil {
		log.Error("publish lookup result failed, requeuing", "error", err)
		d.Nack(false, true)
		return
	}
	d.Ack(false)
}

func (c *Consumer) handleScraper(ctx context.Context, d amqp.Delivery) {
	var task contracts.ScraperTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed scraper task, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("task_id", task.TaskID, "product_id", task.ProductID, "url", task.URL)

	product, err := c.fetch(ctx, task.Platform, task.URL)
	result := contracts.PriceResult{
		TaskID:    task.TaskID,
		ProductID: task.ProductID,
		URL:       task.URL,
		ScrapedAt: time.Now().UTC(),
	}
	if err != nil {
		log.Error("scraper fetch failed", "error", err)
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
		result.Price = product.Price
		result.Currency = product.Currency
		log.Info("scraper fetch completed", "price", product.Price)
	}

	if err := c.pub.publish(ctx, result); err != nil {
		log.Error("publish scraper result failed, requeuing", "error", err)
		d.Nack(false, true)
		return
	}
	d.Ack(false)
}

func (c *Consumer) fetch(ctx context.Context, plat, url string) (*marketplace.Product, error) {
	switch plat {
	case "wb":
		return c.wb.FetchProduct(ctx, url)
	default:
		return nil, fmt.Errorf("unknown platform: %s", plat)
	}
}

// publisher wraps the broker connection for publishing price results.
type publisher struct {
	conn *broker.Connection
}

func (p *publisher) publish(ctx context.Context, result contracts.PriceResult) error {
	return p.conn.Publish(ctx, broker.QueuePriceResults, result)
}
