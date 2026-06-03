package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/scheduler/internal/config"
	"github.com/Gergov00/pricescount/services/scheduler/internal/consumer"
	"github.com/Gergov00/pricescount/services/scheduler/internal/scheduler"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	db, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		slog.Error("postgres connect", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		slog.Error("rabbitmq connect", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	for _, q := range []string{broker.QueueTrackRequests, broker.QueueScraperTasks} {
		if err := conn.DeclareQueue(q); err != nil {
			slog.Error("declare queue", "queue", q, "error", err)
			os.Exit(1)
		}
	}

	st := store.New(db)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 2)
	go func() { errCh <- consumer.New(conn, st).Run(ctx) }()
	go func() { errCh <- scheduler.New(conn, st, cfg.CheckInterval).Run(ctx) }()

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
