package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Gergov00/pricescount/services/gateway/internal/cleaner"
	"github.com/Gergov00/pricescount/services/gateway/internal/config"
	"github.com/Gergov00/pricescount/services/gateway/internal/consumer"
	"github.com/Gergov00/pricescount/services/gateway/internal/handler"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/outbox"
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
			newHandler,
			newConsumer,
			newOutboxWorker,
			newRouter,
		),
		fx.Invoke(
			runHTTP,
			runConsumer,
			runCleaner,
			runOutbox,
			runReconciliation,
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
	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 15)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: %w", err)
	}
	for _, q := range []string{
		broker.QueueLookupTasks,
		broker.QueuePriceResults,
		broker.QueueTrackRequests,
		broker.QueueNotifyTasks,
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

func newHandler(st *store.Store) *handler.Handler {
	return handler.New(st)
}

func newOutboxWorker(st *store.Store, mq *broker.Connection) *outbox.Worker {
	return outbox.New(st, mq)
}

func newConsumer(mq *broker.Connection, st *store.Store) *consumer.Consumer {
	return consumer.New(mq, st)
}

func newRouter(h *handler.Handler, cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		gin.Recovery(),
		handler.RequestLogger(),
		handler.BodyLimit(1<<20), // 1 MiB is plenty for any of our JSON bodies
		handler.Auth(cfg.InternalToken),
	)
	h.Register(r)
	return r
}

func runHTTP(lc fx.Lifecycle, r *gin.Engine, cfg *config.Config, s fx.Shutdowner) {
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				slog.Info("gateway started", "addr", cfg.Addr)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("http server", "error", err)
					s.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	})
}

func runConsumer(lc fx.Lifecycle, c *consumer.Consumer, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				defer close(done)
				if err := c.Run(ctx); err != nil {
					if ctx.Err() == nil {
						slog.Error("consumer stopped", "error", err)
						s.Shutdown(fx.ExitCode(1))
					}
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

func runCleaner(lc fx.Lifecycle, st *store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() { defer close(done); cleaner.Run(ctx, st, 5*time.Minute, 90*24*time.Hour) }()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

func runOutbox(lc fx.Lifecycle, worker *outbox.Worker, s fx.Shutdowner) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		go func() {
			defer close(done)
			if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("outbox worker stopped", "error", err)
				s.Shutdown(fx.ExitCode(1))
			}
		}()
		return nil
	}, OnStop: func(stopCtx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-stopCtx.Done():
			return stopCtx.Err()
		}
	}})
}

func runReconciliation(lc fx.Lifecycle, st *store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				retryDelay := time.Second
				for {
					delay := 5 * time.Minute
					if err := st.ReconcileMonitoring(ctx); err != nil && ctx.Err() == nil {
						slog.Error("monitor reconciliation failed", "error", err)
						delay = retryDelay
						retryDelay *= 2
						if retryDelay > time.Minute {
							retryDelay = time.Minute
						}
					} else {
						retryDelay = time.Second
					}
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}
