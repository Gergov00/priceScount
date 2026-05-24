package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	"github.com/Gergov00/pricescount/services/extractor/internal/dedup"
	"github.com/Gergov00/pricescount/services/extractor/internal/publisher"
)

// Consumer orchestrates: dedup → platform fetch → publish result.
type Consumer struct {
	conn      *broker.Connection
	dedup     *dedup.Store
	wb        *marketplace.WBClient
	ozon      *marketplace.OzonClient
	publisher *publisher.Publisher
}

func New(
	conn *broker.Connection,
	dd *dedup.Store,
	wb *marketplace.WBClient,
	ozon *marketplace.OzonClient,
	pub *publisher.Publisher,
) *Consumer {
	return &Consumer{conn: conn, dedup: dd, wb: wb, ozon: ozon, publisher: pub}
}

// Run blocks consuming from scraper.tasks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueueScraperTasks, "extractor-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueueScraperTasks, err)
	}
	slog.Info("extractor consumer started", "queue", broker.QueueScraperTasks)

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed unexpectedly")
			}
			c.handle(ctx, d)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var task contracts.ScraperTask
	if err := json.Unmarshal(d.Body, &task); err != nil {
		slog.Error("malformed scraper task, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("task_id", task.TaskID, "url", task.URL, "platform", task.Platform)

	if !task.Force {
		dup, err := c.dedup.IsDuplicate(ctx, task.URL)
		if err != nil {
			log.Error("dedup check failed, requeuing", "error", err)
			d.Nack(false, true)
			return
		}
		if dup {
			log.Debug("URL scraped recently, skipping")
			d.Ack(false)
			return
		}
	}

	result := c.process(ctx, task)
	result.TaskID = task.TaskID
	result.ProductID = task.ProductID
	result.URL = task.URL
	result.ScrapedAt = time.Now().UTC()
	result.Force = task.Force

	if err := c.publisher.PublishResult(ctx, result); err != nil {
		log.Error("failed to publish result, requeuing", "error", err)
		d.Nack(false, true)
		return
	}

	if result.Success {
		if err := c.dedup.Mark(ctx, task.URL); err != nil {
			log.Error("failed to mark dedup, continuing", "error", err)
		}
	}

	d.Ack(false)
}

func (c *Consumer) process(ctx context.Context, task contracts.ScraperTask) contracts.PriceResult {
	log := slog.With("task_id", task.TaskID, "url", task.URL)

	var product *marketplace.Product
	var err error

	switch task.Platform {
	case "wb":
		product, err = c.wb.FetchProduct(ctx, task.URL)
	case "ozon":
		product, err = c.ozon.FetchProduct(ctx, task.URL)
	default:
		log.Error("unknown platform, dropping task", "platform", task.Platform)
		return contracts.PriceResult{Success: false, Error: "unknown platform: " + task.Platform}
	}

	if err != nil {
		log.Error("fetch product failed", "platform", task.Platform, "error", err)
		return contracts.PriceResult{Success: false, Error: err.Error()}
	}

	log.Info("price fetched", "platform", task.Platform, "price", product.Price, "currency", product.Currency)
	return contracts.PriceResult{Success: true, Price: product.Price, Currency: product.Currency}
}
