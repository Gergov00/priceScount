package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/notifier/internal/alert"
	"github.com/Gergov00/pricescount/services/notifier/internal/config"
	"github.com/Gergov00/pricescount/services/notifier/internal/consumer"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Provide(
			config.Load,
			newBroker,
			newTelegramSender,
			newConsumer,
		),
		fx.Invoke(runConsumer),
	).Run()
}

func newBroker(lc fx.Lifecycle, cfg *config.Config) (*broker.Connection, error) {
	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: %w", err)
	}
	if err := conn.DeclareQueue(broker.QueueNotifyTasks); err != nil {
		conn.Close()
		return nil, fmt.Errorf("declare queue: %w", err)
	}
	lc.Append(fx.Hook{OnStop: func(_ context.Context) error {
		conn.Close()
		return nil
	}})
	return conn, nil
}

func newTelegramSender(cfg *config.Config) *alert.TelegramSender {
	return alert.NewTelegramSender(cfg.TelegramToken)
}

func newConsumer(mq *broker.Connection, sender *alert.TelegramSender) *consumer.Consumer {
	return consumer.New(mq, sender)
}

func runConsumer(lc fx.Lifecycle, c *consumer.Consumer, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			slog.Info("notifier service started")
			go func() {
				if err := c.Run(ctx); err != nil {
					slog.Error("consumer stopped", "error", err)
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
