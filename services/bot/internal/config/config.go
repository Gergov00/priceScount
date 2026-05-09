package config

import "os"

type Config struct {
	TelegramToken string
	DiscoveryURL  string
	RedisURL      string
	PostgresDSN   string
}

func Load() Config {
	return Config{
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		DiscoveryURL:  getEnv("DISCOVERY_URL", "http://localhost:8081"),
		RedisURL:      getEnv("REDIS_URL", "redis://localhost:6379"),
		PostgresDSN:   getEnv("POSTGRES_DSN", "postgres://pricescount:pricescount@localhost:5434/pricescount?sslmode=disable"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
