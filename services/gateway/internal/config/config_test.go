package config

import "testing"

func TestLoadRequiresConnectionSettingsAndDefaultsAddress(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://db")
	t.Setenv("RABBITMQ_URL", "amqp://mq")
	t.Setenv("INTERNAL_TOKEN", "token")
	t.Setenv("GATEWAY_ADDR", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.PostgresDSN != "postgres://db" || c.RabbitMQURL != "amqp://mq" || c.InternalToken != "token" {
		t.Fatalf("config=%+v", c)
	}
	for _, key := range []string{"POSTGRES_DSN", "RABBITMQ_URL", "INTERNAL_TOKEN"} {
		t.Run(key+" required", func(t *testing.T) {
			t.Setenv("POSTGRES_DSN", "postgres://db")
			t.Setenv("RABBITMQ_URL", "amqp://mq")
			t.Setenv("INTERNAL_TOKEN", "token")
			t.Setenv(key, "")
			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded without %s", key)
			}
		})
	}
}
