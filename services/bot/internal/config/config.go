package config

import "os"

type Config struct {
	TelegramToken string
	GatewayURL    string
	InternalToken string
}

func Load() Config {
	return Config{
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		GatewayURL:    getEnv("GATEWAY_URL", "http://localhost:8080"),
		InternalToken: os.Getenv("INTERNAL_TOKEN"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
