package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
)

// Consumer reads price.results and either:
//   - completes a pending lookup_request (if LookupID is set), or
//   - saves price history and fires threshold alerts (if ProductID is set).
type Consumer struct {
	conn  *broker.Connection
	store *store.Store
	mq    *broker.Connection
}

func New(conn *broker.Connection, st *store.Store) *Consumer {
	return &Consumer{conn: conn, store: st, mq: conn}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.conn.Consume(broker.QueuePriceResults, "gateway-consumer")
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

	subs, err := c.store.TriggeredSubscriptions(ctx, result.ProductID, result.Price)
	if err != nil {
		log.Error("triggered subscriptions query failed", "product_id", result.ProductID, "error", err)
		d.Ack(false)
		return
	}

	for _, sub := range subs {
		direction := "down"
		if result.Price > sub.MaxPrice {
			direction = "up"
		}

		text := formatAlert(sub.ProductName, sub.ProductURL, result.Price, result.Currency, direction, sub.MinPrice, sub.MaxPrice)
		task := contracts.NotifyTask{
			Channel:   "telegram",
			Target:    strconv.FormatInt(sub.ChatID, 10),
			Text:      text,
			Direction: direction,
		}
		if err := c.mq.Publish(ctx, broker.QueueNotifyTasks, task); err != nil {
			log.Error("publish notify task failed", "chat_id", sub.ChatID, "error", err)
		}
	}

	log.Info("monitoring result processed", "product_id", result.ProductID, "price", result.Price, "alerts", len(subs))
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
