package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/notifier/internal/config"
	"github.com/Gergov00/pricescount/services/notifier/internal/consumer"
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

	if err := conn.DeclareQueue(broker.QueueNotifyTasks); err != nil {
		slog.Error("declare queue", "queue", broker.QueueNotifyTasks, "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := consumer.New(conn, cfg.TelegramToken)

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
