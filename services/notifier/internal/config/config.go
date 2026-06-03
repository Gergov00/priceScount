package config

import (
	"fmt"
	"os"
)

type Config struct {
	RabbitMQURL   string
	TelegramToken string
}

func Load() (*Config, error) {
	c := &Config{
		RabbitMQURL:   getenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
	}
	if c.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}
	if c.TelegramToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
