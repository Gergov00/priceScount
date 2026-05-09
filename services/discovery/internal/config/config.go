package config

import "os"

type Config struct {
	Port         string
	RabbitMQURL  string
	SerperAPIKey string
}

func Load() Config {
	return Config{
		Port:         getEnv("PORT", "8081"),
		RabbitMQURL:  getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		SerperAPIKey: getEnv("SERPER_API_KEY", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
