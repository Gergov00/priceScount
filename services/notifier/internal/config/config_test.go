package config

import "testing"

func TestLoadRequiresTelegramTokenAndDefaultsBroker(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("RABBITMQ_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without Telegram token")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RabbitMQURL != "amqp://guest:guest@localhost:5672/" || c.TelegramToken != "bot-token" {
		t.Fatalf("config=%+v", c)
	}
}
