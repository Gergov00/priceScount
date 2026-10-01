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

// Fetcher is the scraping interface required by Consumer.
type Fetcher interface {
	FetchProduct(ctx context.Context, url string) (*marketplace.Product, error)
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	ConsumeWithPrefetch(queue, consumer string, prefetch int) (<-chan amqp.Delivery, error)
	Publish(ctx context.Context, queue string, v any) error
}

// Consumer runs two goroutines consuming from separate queues:
//   - lookup.tasks  (prefetch=1): one-time product fetches from Gateway
//   - scraper.tasks (prefetch=2): periodic monitoring from Scheduler
type Consumer struct {
	mq MQ
	wb Fetcher
}

func New(mq MQ, wb Fetcher) *Consumer {
	return &Consumer{mq: mq, wb: wb}
}

// Run starts both consumer goroutines and blocks until ctx is cancelled or either fails.
func (c *Consumer) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := c.runQueue(runCtx, broker.QueueLookupTasks, "extractor-lookup", 1); err != nil {
			errCh <- fmt.Errorf("lookup consumer: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := c.runQueue(runCtx, broker.QueueScraperTasks, "extractor-scraper", 2); err != nil {
			errCh <- fmt.Errorf("scraper consumer: %w", err)
		}
	}()

	var firstErr error
	select {
	case firstErr = <-errCh:
		if firstErr != nil {
			cancel()
		}
	case <-ctx.Done():
		cancel()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return firstErr
}

func (c *Consumer) runQueue(ctx context.Context, queue, consumerTag string, prefetch int) error {
	deliveries, err := c.mq.ConsumeWithPrefetch(queue, consumerTag, prefetch)
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
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("delivery channel closed: %s", queue)
			}
			if ctx.Err() != nil {
				return nil
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
	if ctx.Err() != nil {
		return
	}
	var task contracts.LookupTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed lookup task, dropping", "error", err)
		if err := d.Nack(false, false); err != nil {
			slog.Warn("nack malformed lookup failed", "error", err)
		}
		return
	}

	log := slog.With("task_id", task.TaskID, "lookup_id", task.LookupID, "url", task.URL)

	product, err := c.fetch(ctx, task.Platform, task.URL)
	if ctx.Err() != nil {
		return
	}
	result := contracts.PriceResult{
		TaskID:    task.TaskID,
		LookupID:  task.LookupID,
		URL:       task.URL,
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

	if err := c.mq.Publish(ctx, broker.QueuePriceResults, result); err != nil {
		log.Error("publish lookup result failed, requeuing", "error", err)
		if ctx.Err() == nil {
			if nackErr := d.Nack(false, true); nackErr != nil {
				log.Error("nack lookup task failed", "error", nackErr)
			}
		}
		return
	}
	if ctx.Err() == nil {
		if ackErr := d.Ack(false); ackErr != nil {
			log.Error("ack lookup task failed", "error", ackErr)
		}
	}
}

func (c *Consumer) handleScraper(ctx context.Context, d amqp.Delivery) {
	if ctx.Err() != nil {
		return
	}
	var task contracts.ScraperTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed scraper task, dropping", "error", err)
		if err := d.Nack(false, false); err != nil {
			slog.Warn("nack malformed scraper task failed", "error", err)
		}
		return
	}

	log := slog.With("task_id", task.TaskID, "product_id", task.ProductID, "url", task.URL)

	product, err := c.fetch(ctx, task.Platform, task.URL)
	if ctx.Err() != nil {
		return
	}
	result := contracts.PriceResult{
		TaskID:    task.TaskID,
		ProductID: task.ProductID,
		URL:       task.URL,
		ScrapedAt: time.Now().UTC(),
		Force:     task.Force,
		ChatID:    task.ChatID,
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

	if err := c.mq.Publish(ctx, broker.QueuePriceResults, result); err != nil {
		log.Error("publish scraper result failed, requeuing", "error", err)
		if ctx.Err() == nil {
			if nackErr := d.Nack(false, true); nackErr != nil {
				log.Error("nack scraper task failed", "error", nackErr)
			}
		}
		return
	}
	if ctx.Err() == nil {
		if ackErr := d.Ack(false); ackErr != nil {
			log.Error("ack scraper task failed", "error", ackErr)
		}
	}
}

func (c *Consumer) fetch(ctx context.Context, plat, url string) (*marketplace.Product, error) {
	switch plat {
	case "wb":
		return c.wb.FetchProduct(ctx, url)
	default:
		return nil, fmt.Errorf("unknown platform: %s", plat)
	}
}
