package config

import "testing"

func TestLoadUsesRabbitMQDefaultAndOverride(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RabbitMQURL != "amqp://guest:guest@localhost:5672/" {
		t.Fatalf("default URL=%q", c.RabbitMQURL)
	}
	t.Setenv("RABBITMQ_URL", "amqp://custom")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RabbitMQURL != "amqp://custom" {
		t.Fatalf("configured URL=%q", c.RabbitMQURL)
	}
}
