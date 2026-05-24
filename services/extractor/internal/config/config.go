package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	RabbitMQURL string
	RedisURL    string
	ScrapedTTL  time.Duration
}

func Load() Config {
	return Config{
		RabbitMQURL: getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
		ScrapedTTL:  checkInterval(),
	}
}

// checkInterval reads CHECK_INTERVAL_MINUTES (shared with the scheduler service)
// so the dedup TTL stays in sync with how often the scheduler fires.
func checkInterval() time.Duration {
	if v := os.Getenv("CHECK_INTERVAL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return time.Hour
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
