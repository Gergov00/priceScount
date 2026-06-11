package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Gergov00/pricescount/services/bot/internal/bot"
	"github.com/Gergov00/pricescount/services/bot/internal/config"
	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
	"github.com/Gergov00/pricescount/services/bot/internal/state"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Provide(
			config.Load,
			state.New,
			newGatewayClient,
			newBot,
		),
		fx.Invoke(runBot),
	).Run()
}

func newGatewayClient(cfg config.Config) (*gateway.Client, error) {
	if cfg.InternalToken == "" {
		return nil, fmt.Errorf("INTERNAL_TOKEN is not set")
	}
	return gateway.New(cfg.GatewayURL, cfg.InternalToken), nil
}

func newBot(cfg config.Config, st *state.Store, gw *gateway.Client) (*bot.Bot, error) {
	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is not set")
	}
	return bot.New(cfg.TelegramToken, st, gw)
}

func runBot(lc fx.Lifecycle, b *bot.Bot, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := b.Run(ctx); err != nil {
					slog.Error("bot stopped", "error", err)
					s.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}
