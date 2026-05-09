package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Gergov00/pricescount/services/discovery/internal/agent"
	"github.com/Gergov00/pricescount/services/discovery/internal/config"
	"github.com/Gergov00/pricescount/services/discovery/internal/handler"
	"github.com/Gergov00/pricescount/services/discovery/internal/publisher"
	"github.com/Gergov00/pricescount/shared/pkg/broker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.Load()

	conn, err := broker.ConnectWithRetry(cfg.RabbitMQURL, 10)
	if err != nil {
		slog.Error("rabbitmq unavailable", "error", err)
		os.Exit(1)
	}
	
	defer conn.Close()

	if err := conn.DeclareQueue(broker.QueueDiscoveryURLs); err != nil {
		slog.Error("queue declare failed", "queue", broker.QueueDiscoveryURLs, "error", err)
		os.Exit(1)
	}

	ag := agent.New(cfg.SerperAPIKey)
	pub := publisher.New(conn)
	h := handler.New(ag, pub)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /discover", h.Discover)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("discovery service listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown error", "error", err)
	}
	slog.Info("discovery service stopped")
}
