package config

import "os"

type Config struct {
	RabbitMQURL   string
	PostgresDSN   string
	TelegramToken string
}

func Load() Config {
	return Config{
		RabbitMQURL:   getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		PostgresDSN:   getEnv("POSTGRES_DSN", "postgres://pricescount:pricescount@localhost:5434/pricescount?sslmode=disable"),
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
