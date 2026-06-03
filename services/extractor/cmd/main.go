package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	"github.com/Gergov00/pricescount/services/extractor/internal/config"
	"github.com/Gergov00/pricescount/services/extractor/internal/consumer"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		slog.Error("rabbitmq connect", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	for _, q := range []string{
		broker.QueueLookupTasks,
		broker.QueueScraperTasks,
		broker.QueuePriceResults,
	} {
		if err := conn.DeclareQueue(q); err != nil {
			slog.Error("declare queue", "queue", q, "error", err)
			os.Exit(1)
		}
	}

	wbClient := marketplace.NewWBClient()
	defer wbClient.Close()

	c := consumer.New(conn, wbClient)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := c.Run(ctx); err != nil {
			slog.Error("consumer error", "error", err)
		}
	}()

	slog.Info("extractor service started")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()
	slog.Info("extractor service stopped")
}
