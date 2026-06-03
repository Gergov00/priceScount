package config

import (
	"fmt"
	"os"
)

type Config struct {
	RabbitMQURL string
}

func Load() (*Config, error) {
	c := &Config{
		RabbitMQURL: getenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
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
