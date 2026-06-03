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

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/services/gateway/internal/cleaner"
	"github.com/Gergov00/pricescount/services/gateway/internal/config"
	"github.com/Gergov00/pricescount/services/gateway/internal/consumer"
	"github.com/Gergov00/pricescount/services/gateway/internal/handler"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
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
			newRouter,
		),
		fx.Invoke(
			runHTTP,
			runConsumer,
			runCleaner,
			resyncScheduler,
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

func newHandler(st *store.Store, mq *broker.Connection) *handler.Handler {
	return handler.New(st, mq)
}

func newConsumer(mq *broker.Connection, st *store.Store) *consumer.Consumer {
	return consumer.New(mq, st)
}

func newRouter(h *handler.Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), handler.RequestLogger())
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

func runCleaner(lc fx.Lifecycle, st *store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go cleaner.Run(ctx, st, 5*time.Minute)
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}

// resyncScheduler publishes track.requests{add} for every active product on startup
// so the scheduler's scheduled_urls table is always consistent with gateway's subscriptions.
func resyncScheduler(lc fx.Lifecycle, st *store.Store, mq *broker.Connection) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			products, err := st.AllActiveProducts(ctx)
			if err != nil {
				slog.Error("resync scheduler: fetch products", "error", err)
				return nil // non-fatal: scheduler will pick things up eventually
			}
			for _, p := range products {
				msg := contracts.TrackRequest{
					Action:        "add",
					ProductID:     p.ProductID,
					URL:           p.URL,
					Platform:      p.Platform,
					IntervalHours: 1,
				}
				if err := mq.Publish(ctx, broker.QueueTrackRequests, msg); err != nil {
					slog.Error("resync scheduler: publish", "url", p.URL, "error", err)
				}
			}
			slog.Info("scheduler resync complete", "products", len(products))
			return nil
		},
	})
}
