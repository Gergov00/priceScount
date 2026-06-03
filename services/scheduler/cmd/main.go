package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/services/scheduler/internal/config"
	"github.com/Gergov00/pricescount/services/scheduler/internal/consumer"
	"github.com/Gergov00/pricescount/services/scheduler/internal/scheduler"
	"github.com/Gergov00/pricescount/services/scheduler/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	fx.New(
		fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
		fx.Provide(
			config.Load,
			newDB,
			newBroker,
			store.New,
			newConsumer,
			newScheduler,
		),
		fx.Invoke(
			runConsumer,
			runScheduler,
		),
	).Run()
}

func newDB(lc fx.Lifecycle, cfg *config.Config) (*pgxpool.Pool, error) {
	db, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	lc.Append(fx.Hook{OnStop: func(_ context.Context) error {
		db.Close()
		return nil
	}})
	return db, nil
}

func newBroker(lc fx.Lifecycle, cfg *config.Config) (*broker.Connection, error) {
	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: %w", err)
	}
	for _, q := range []string{broker.QueueTrackRequests, broker.QueueScraperTasks} {
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

func newConsumer(mq *broker.Connection, st *store.Store) *consumer.Consumer {
	return consumer.New(mq, st)
}

func newScheduler(mq *broker.Connection, st *store.Store, cfg *config.Config) *scheduler.Scheduler {
	return scheduler.New(mq, st, cfg.CheckInterval)
}

func runConsumer(lc fx.Lifecycle, c *consumer.Consumer, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
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

func runScheduler(lc fx.Lifecycle, sc *scheduler.Scheduler, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := sc.Run(ctx); err != nil {
					slog.Error("scheduler stopped", "error", err)
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
