package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
)

// Store is the persistence interface required by Consumer.
// Defined here, in the consumer, per Go convention.
type Store interface {
	FailLookup(ctx context.Context, lookupID, errMsg string) error
	CompleteLookup(ctx context.Context, lookupID, name string, price float64) error
	SavePrice(ctx context.Context, productID string, price float64, currency string, scrapedAt time.Time) error
	ActiveSubscriptions(ctx context.Context, productID string) ([]store.TriggeredSub, error)
	TriggeredSubscriptions(ctx context.Context, productID string, price float64) ([]store.TriggeredSub, error)
}

// MQ is the messaging interface required by Consumer.
type MQ interface {
	Consume(queue, consumer string) (<-chan amqp.Delivery, error)
	Publish(ctx context.Context, queue string, v any) error
}

// Consumer reads price.results and either:
//   - completes a pending lookup_request (if LookupID is set), or
//   - saves price history and fires threshold alerts (if ProductID is set).
type Consumer struct {
	mq    MQ
	store Store
}

func New(mq MQ, st Store) *Consumer {
	return &Consumer{mq: mq, store: st}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.mq.Consume(broker.QueuePriceResults, "gateway-consumer")
	if err != nil {
		return fmt.Errorf("consume %s: %w", broker.QueuePriceResults, err)
	}
	slog.Info("gateway price.results consumer started")

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
	var result contracts.PriceResult
	if err := json.Unmarshal(d.Body, &result); err != nil {
		slog.Error("malformed price result, dropping", "error", err)
		d.Nack(false, false)
		return
	}

	log := slog.With("task_id", result.TaskID, "url", result.URL)

	if result.LookupID != "" {
		c.handleLookup(ctx, d, result, log)
		return
	}
	c.handleMonitoring(ctx, d, result, log)
}

func (c *Consumer) handleLookup(ctx context.Context, d amqp.Delivery, result contracts.PriceResult, log *slog.Logger) {
	if !result.Success {
		if err := c.store.FailLookup(ctx, result.LookupID, result.Error); err != nil {
			log.Error("fail lookup in db", "lookup_id", result.LookupID, "error", err)
			d.Nack(false, true)
			return
		}
		log.Info("lookup failed", "lookup_id", result.LookupID, "error", result.Error)
		d.Ack(false)
		return
	}

	if err := c.store.CompleteLookup(ctx, result.LookupID, result.Name, result.Price); err != nil {
		log.Error("complete lookup in db", "lookup_id", result.LookupID, "error", err)
		d.Nack(false, true)
		return
	}
	log.Info("lookup completed", "lookup_id", result.LookupID, "name", result.Name, "price", result.Price)
	d.Ack(false)
}

func (c *Consumer) handleMonitoring(ctx context.Context, d amqp.Delivery, result contracts.PriceResult, log *slog.Logger) {
	if !result.Success {
		log.Info("price result unsuccessful, skipping", "product_id", result.ProductID, "error", result.Error)
		d.Ack(false)
		return
	}

	if err := c.store.SavePrice(ctx, result.ProductID, result.Price, result.Currency, result.ScrapedAt); err != nil {
		log.Error("save price failed, requeuing", "product_id", result.ProductID, "error", err)
		d.Nack(false, true)
		return
	}

	var subs []store.TriggeredSub
	var err error
	if result.Force {
		subs, err = c.store.ActiveSubscriptions(ctx, result.ProductID)
	} else {
		subs, err = c.store.TriggeredSubscriptions(ctx, result.ProductID, result.Price)
	}
	if err != nil {
		log.Error("subscriptions query failed", "product_id", result.ProductID, "error", err)
		d.Ack(false)
		return
	}

	for _, sub := range subs {
		var text string
		if result.Force {
			text = formatStatus(sub.ProductName, sub.ProductURL, result.Price, result.Currency, sub.MinPrice, sub.MaxPrice)
		} else {
			direction := "down"
			if result.Price > sub.MaxPrice {
				direction = "up"
			}
			text = formatAlert(sub.ProductName, sub.ProductURL, result.Price, result.Currency, direction, sub.MinPrice, sub.MaxPrice)
		}
		task := contracts.NotifyTask{
			Channel: "telegram",
			Target:  strconv.FormatInt(sub.ChatID, 10),
			Text:    text,
		}
		if !result.Force {
			if result.Price < sub.MinPrice {
				task.Direction = "down"
			} else {
				task.Direction = "up"
			}
		}
		if err := c.mq.Publish(ctx, broker.QueueNotifyTasks, task); err != nil {
			log.Error("publish notify task failed", "chat_id", sub.ChatID, "error", err)
		}
	}

	log.Info("monitoring result processed", "product_id", result.ProductID, "price", result.Price, "alerts", len(subs), "force", result.Force)
	d.Ack(false)
}

func formatAlert(name, url string, price float64, currency, direction string, minPrice, maxPrice float64) string {
	if direction == "down" {
		return fmt.Sprintf("📉 Цена упала!\n\n%s\n%s\n\nЦена: %.0f %s\nВаш минимум: %.0f %s",
			name, url, price, currency, minPrice, currency)
	}
	return fmt.Sprintf("📈 Цена выросла!\n\n%s\n%s\n\nЦена: %.0f %s\nВаш максимум: %.0f %s",
		name, url, price, currency, maxPrice, currency)
}

func formatStatus(name, url string, price float64, currency string, minPrice, maxPrice float64) string {
	return fmt.Sprintf("📊 Текущая цена\n\n%s\n%s\n\nЦена: %.0f %s\nДиапазон: %.0f — %.0f %s",
		name, url, price, currency, minPrice, maxPrice, currency)
}
