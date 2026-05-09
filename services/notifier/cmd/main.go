package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/services/notifier/internal/config"
	"github.com/Gergov00/pricescount/services/notifier/internal/consumer"
	"github.com/Gergov00/pricescount/services/notifier/internal/store"
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

	if err := conn.DeclareQueue(broker.QueuePriceResults); err != nil {
		slog.Error("queue declare failed", "queue", broker.QueuePriceResults, "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.New(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Error("postgres unavailable", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	c := consumer.New(conn, st, cfg.TelegramToken)

	go func() {
		if err := c.Run(ctx); err != nil {
			slog.Error("consumer error", "error", err)
		}
	}()

	slog.Info("notifier service started")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()
	slog.Info("notifier service stopped")
}
