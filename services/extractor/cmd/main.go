package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/services/extractor/internal/config"
	"github.com/Gergov00/pricescount/services/extractor/internal/consumer"
	"github.com/Gergov00/pricescount/services/extractor/internal/dedup"
	"github.com/Gergov00/pricescount/services/extractor/internal/llm"
	"github.com/Gergov00/pricescount/services/extractor/internal/publisher"
	"github.com/Gergov00/pricescount/services/extractor/internal/scraper"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.Load()

	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		slog.Error("rabbitmq unavailable", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	for _, q := range []string{broker.QueueScraperTasks, broker.QueuePriceResults} {
		if err := conn.DeclareQueue(q); err != nil {
			slog.Error("queue declare failed", "queue", q, "error", err)
			os.Exit(1)
		}
	}

	dd, err := dedup.NewStore(cfg.RedisURL, cfg.ScrapedTTL)
	if err != nil {
		slog.Error("redis unavailable", "error", err)
		os.Exit(1)
	}
	defer dd.Close()

	c := consumer.New(
		conn,
		dd,
		scraper.New(),
		llm.New(cfg.LLMAPIKey, cfg.LLMModel),
		publisher.New(conn),
	)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := c.Run(ctx); err != nil {
			slog.Error("consumer error", "error", err)
		}
	}()

	slog.Info("extractor service started", "model", cfg.LLMModel)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()
	slog.Info("extractor service stopped")
}
