package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndInterval(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://db")
	t.Setenv("RABBITMQ_URL", "")
	t.Setenv("CHECK_INTERVAL_MINUTES", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RabbitMQURL != "amqp://guest:guest@localhost:5672/" || c.CheckInterval != 60*time.Minute {
		t.Fatalf("defaults=%+v", c)
	}
	t.Setenv("CHECK_INTERVAL_MINUTES", "15")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CheckInterval != 15*time.Minute {
		t.Fatalf("interval=%s", c.CheckInterval)
	}
}
func TestLoadRequiresPostgresDSNAndIgnoresInvalidInterval(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without POSTGRES_DSN")
	}
	t.Setenv("POSTGRES_DSN", "postgres://db")
	t.Setenv("CHECK_INTERVAL_MINUTES", "0")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CheckInterval != 60*time.Minute {
		t.Fatalf("invalid interval accepted: %s", c.CheckInterval)
	}
}
