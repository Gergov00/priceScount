package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Gergov00/pricescount/services/bot/internal/bot"
	"github.com/Gergov00/pricescount/services/bot/internal/config"
	"github.com/Gergov00/pricescount/services/bot/internal/discovery"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.Load()
	if cfg.TelegramToken == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Error("postgres unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	st, err := state.New(cfg.RedisURL)
	if err != nil {
		slog.Error("redis unavailable", "error", err)
		os.Exit(1)
	}

	b, err := bot.New(cfg.TelegramToken, discovery.New(cfg.DiscoveryURL), st, db)
	if err != nil {
		slog.Error("bot init failed", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := b.Run(ctx); err != nil {
			slog.Error("bot error", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()
	slog.Info("bot stopped")
}
