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
	"github.com/Gergov00/pricescount/services/extractor/internal/dedup"
	"github.com/Gergov00/pricescount/services/extractor/internal/headless"
	"github.com/Gergov00/pricescount/services/extractor/internal/jsonld"
	"github.com/Gergov00/pricescount/services/extractor/internal/llm"
	"github.com/Gergov00/pricescount/services/extractor/internal/nextdata"
	"github.com/Gergov00/pricescount/services/extractor/internal/publisher"
	"github.com/Gergov00/pricescount/services/extractor/internal/scraper"
)

// Consumer orchestrates: dedup → fetch → extract price → publish result.
//
// Fetch strategy (in order):
//  1. Regular HTTP scraper
//  2. Headless browser (if HTTP returns access-denied)
//
// Extraction strategy (in order):
//  1. JSON-LD structured data (no LLM cost)
//  2. Groq LLM on raw HTML
type Consumer struct {
	conn      *broker.Connection
	dedup     *dedup.Store
	scraper   *scraper.Scraper
	headless  *headless.Scraper
	extractor *llm.Extractor
	publisher *publisher.Publisher
}

func New(
	conn *broker.Connection,
	dd *dedup.Store,
	sc *scraper.Scraper,
	hs *headless.Scraper,
	ex *llm.Extractor,
	pub *publisher.Publisher,
) *Consumer {
	return &Consumer{
		conn:      conn,
		dedup:     dd,
		scraper:   sc,
		headless:  hs,
		extractor: ex,
		publisher: pub,
	}
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

	log := slog.With("task_id", task.TaskID, "url", task.URL)

	if !task.Force {
		dup, err := c.dedup.IsDuplicate(ctx, task.URL)
		if err != nil {
			log.Error("dedup check failed, requeuing", "error", err)
			d.Nack(false, true)
			return
		}
		if dup {
			log.Info("URL scraped recently, skipping")
			d.Ack(false)
			return
		}
	}

	result := c.process(ctx, task)
	result.TaskID = task.TaskID
	result.ProductID = task.ProductID
	result.URL = task.URL
	result.ScrapedAt = time.Now().UTC()

	if err := c.publisher.PublishResult(ctx, result); err != nil {
		log.Error("failed to publish result, requeuing", "error", err)
		d.Nack(false, true)
		return
	}

	if err := c.dedup.Mark(ctx, task.URL); err != nil {
		log.Error("failed to mark dedup, continuing", "error", err)
	}

	d.Ack(false)
}

func (c *Consumer) process(ctx context.Context, task contracts.ScraperTask) contracts.PriceResult {
	log := slog.With("task_id", task.TaskID, "url", task.URL)

	// Step 1: regular HTTP fetch.
	html, httpErr := c.scraper.Fetch(task.URL)
	if httpErr != nil {
		log.Info("HTTP fetch failed, trying headless", "error", httpErr)
	} else {
		// Step 2: extract from HTTP response (JSON-LD → __NEXT_DATA__ → LLM).
		if result, ok := c.extractFromHTML(ctx, log, html, "http"); ok {
			return result
		}
		log.Info("price not found via HTTP, trying headless")
	}

	// Step 3: headless Chrome fallback (handles JS-rendered pages, TLS blocks, access denied).
	headlessHTML, err := c.headless.Fetch(task.URL)
	if err != nil {
		log.Error("headless fetch failed", "error", err)
		errMsg := err.Error()
		if httpErr != nil {
			errMsg = fmt.Sprintf("http: %s; headless: %s", httpErr, err)
		}
		return contracts.PriceResult{Success: false, Error: errMsg}
	}
	log.Info("headless fetch succeeded")

	if result, ok := c.extractFromHTML(ctx, log, headlessHTML, "headless"); ok {
		return result
	}

	return contracts.PriceResult{Success: false, Error: "all extraction methods exhausted"}
}

// extractFromHTML tries JSON-LD → __NEXT_DATA__ → LLM in order.
func (c *Consumer) extractFromHTML(ctx context.Context, log *slog.Logger, html, source string) (contracts.PriceResult, bool) {
	if r, ok := jsonld.Extract(html); ok {
		log.Info("price extracted via JSON-LD", "source", source, "price", r.Price, "currency", r.Currency)
		return contracts.PriceResult{Success: true, Price: r.Price, Currency: r.Currency}, true
	}

	if r, ok := nextdata.Extract(html); ok {
		log.Info("price extracted via __NEXT_DATA__", "source", source, "price", r.Price, "currency", r.Currency)
		return contracts.PriceResult{Success: true, Price: r.Price, Currency: r.Currency}, true
	}

	extracted, err := c.extractor.Extract(ctx, html)
	if err == nil {
		log.Info("price extracted via LLM", "source", source, "price", extracted.Price, "currency", extracted.Currency)
		return contracts.PriceResult{Success: true, Price: extracted.Price, Currency: extracted.Currency}, true
	}
	log.Info("LLM extraction failed", "source", source, "error", err)
	return contracts.PriceResult{}, false
}
