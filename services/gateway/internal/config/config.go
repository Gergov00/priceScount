package config

import (
	"fmt"
	"os"
)

type Config struct {
	Addr          string
	PostgresDSN   string
	RabbitMQURL   string
	InternalToken string
}

func Load() (*Config, error) {
	c := &Config{
		Addr:          getenv("GATEWAY_ADDR", ":8080"),
		PostgresDSN:   os.Getenv("POSTGRES_DSN"),
		RabbitMQURL:   os.Getenv("RABBITMQ_URL"),
		InternalToken: os.Getenv("INTERNAL_TOKEN"),
	}
	if c.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	if c.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}
	if c.InternalToken == "" {
		return nil, fmt.Errorf("INTERNAL_TOKEN is required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
