package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/gateway/internal/cleaner"
	"github.com/Gergov00/pricescount/services/gateway/internal/config"
	"github.com/Gergov00/pricescount/services/gateway/internal/consumer"
	"github.com/Gergov00/pricescount/services/gateway/internal/handler"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
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

	mq, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 15)
	if err != nil {
		slog.Error("rabbitmq connect", "error", err)
		os.Exit(1)
	}
	defer mq.Close()

	for _, q := range []string{
		broker.QueueLookupTasks,
		broker.QueuePriceResults,
		broker.QueueTrackRequests,
		broker.QueueNotifyTasks,
	} {
		if err := mq.DeclareQueue(q); err != nil {
			slog.Error("declare queue", "queue", q, "error", err)
			os.Exit(1)
		}
	}

	st := store.New(db)
	h := handler.New(st, mq)
	c := consumer.New(mq, st)

	mux := http.NewServeMux()
	h.Register(mux)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		slog.Info("gateway started", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "error", err)
			cancel()
		}
	}()

	go func() {
		if err := c.Run(ctx); err != nil {
			slog.Error("consumer stopped", "error", err)
		}
	}()

	go cleaner.Run(ctx, st, 5*time.Minute)

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown", "error", err)
	}
}
