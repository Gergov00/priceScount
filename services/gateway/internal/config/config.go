package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Addr        string
	PostgresDSN string
	RabbitMQURL string
}

func Load() (*Config, error) {
	c := &Config{
		Addr:        getenv("GATEWAY_ADDR", ":8080"),
		PostgresDSN: os.Getenv("POSTGRES_DSN"),
		RabbitMQURL: os.Getenv("RABBITMQ_URL"),
	}
	if c.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	if c.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

var _ = getenvInt // suppress unused warning; available for future use
