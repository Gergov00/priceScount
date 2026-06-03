package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	RabbitMQURL   string
	PostgresDSN   string
	CheckInterval time.Duration
}

func Load() (*Config, error) {
	interval := 60 * time.Minute
	if v := os.Getenv("CHECK_INTERVAL_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			interval = time.Duration(mins) * time.Minute
		}
	}

	c := &Config{
		RabbitMQURL:   getenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		PostgresDSN:   os.Getenv("POSTGRES_DSN"),
		CheckInterval: interval,
	}
	if c.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
