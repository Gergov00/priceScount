package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/marketplace"
	"github.com/Gergov00/pricescount/services/extractor/internal/config"
	"github.com/Gergov00/pricescount/services/extractor/internal/consumer"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Provide(
			config.Load,
			newBroker,
			newWBClient,
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
	for _, q := range []string{
		broker.QueueLookupTasks,
		broker.QueueScraperTasks,
		broker.QueuePriceResults,
	} {
		if err := conn.DeclareQueue(q); err != nil {
			conn.Close()
			return nil, fmt.Errorf("declare queue %s: %w", q, err)
		}
	}
	lc.Append(fx.Hook{OnStop: func(_ context.Context) error {
		conn.Close()
		return nil
	}})
	return conn, nil
}

func newWBClient(lc fx.Lifecycle) *marketplace.WBClient {
	wb := marketplace.NewWBClient()
	lc.Append(fx.Hook{OnStop: func(_ context.Context) error {
		wb.Close()
		return nil
	}})
	return wb
}

func newConsumer(mq *broker.Connection, wb *marketplace.WBClient) *consumer.Consumer {
	return consumer.New(mq, wb)
}

func runConsumer(lc fx.Lifecycle, c *consumer.Consumer, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			slog.Info("extractor service started")
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
