package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	RabbitMQURL   string
	RedisURL      string
	CheckInterval time.Duration
	URLSetKey     string
	URLMetaKey    string
}

func Load() Config {
	interval := 60 * time.Minute
	if v := os.Getenv("CHECK_INTERVAL_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			interval = time.Duration(mins) * time.Minute
		}
	}
	return Config{
		RabbitMQURL:   getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		RedisURL:      getEnv("REDIS_URL", "redis://localhost:6379"),
		CheckInterval: interval,
		URLSetKey:     "pricescount:urls",
		URLMetaKey:    "pricescount:url_meta",
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
