package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/services/scheduler/internal/config"
	"github.com/Gergov00/pricescount/services/scheduler/internal/consumer"
	"github.com/Gergov00/pricescount/services/scheduler/internal/scheduler"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
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

	for _, q := range []string{broker.QueueDiscoveryURLs, broker.QueueScraperTasks} {
		if err := conn.DeclareQueue(q); err != nil {
			slog.Error("queue declare failed", "queue", q, "error", err)
			os.Exit(1)
		}
	}

	urlStore, err := store.NewURLStore(cfg.RedisURL, cfg.URLSetKey, cfg.URLMetaKey)
	if err != nil {
		slog.Error("failed to init redis store", "error", err)
		os.Exit(1)
	}
	defer urlStore.Close()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 2)
	go func() { errCh <- consumer.New(conn, urlStore).Run(ctx) }()
	go func() { errCh <- scheduler.New(conn, urlStore, cfg.CheckInterval).Run(ctx) }()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("received signal, shutting down", "signal", sig)
	case err := <-errCh:
		slog.Error("component error", "error", err)
	}

	cancel()
	slog.Info("scheduler service stopped")
}
