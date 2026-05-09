package config

import (
	"os"
	"time"
)

type Config struct {
	RabbitMQURL string
	RedisURL    string
	LLMAPIKey   string
	LLMModel    string
	ScrapedTTL  time.Duration // dedup window — skip re-scraping within this period
}

func Load() Config {
	return Config{
		RabbitMQURL: getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
		LLMAPIKey:   getEnv("LLM_API_KEY", ""),
		LLMModel:    getEnv("LLM_MODEL", "llama-3.3-70b-versatile"),
		ScrapedTTL:  time.Hour,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
