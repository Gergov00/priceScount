package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gergov00/pricescount/services/bot/internal/bot"
	"github.com/Gergov00/pricescount/services/bot/internal/config"
	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	cfg := config.Load()
	if cfg.TelegramToken == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	gw := gateway.New(cfg.GatewayURL)
	st := state.New()

	b, err := bot.New(cfg.TelegramToken, st, gw)
	if err != nil {
		slog.Error("bot init failed", "error", err)
		os.Exit(1)
	}

	if err := b.Run(ctx); err != nil {
		slog.Error("bot error", "error", err)
	}
	slog.Info("bot stopped")
}
