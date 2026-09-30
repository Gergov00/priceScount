package config

import "testing"

func TestLoadUsesGatewayDefaultAndReadsEnvironment(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "telegram-test-token")
	t.Setenv("GATEWAY_URL", "")
	t.Setenv("INTERNAL_TOKEN", "internal-test-token")
	got := Load()
	if got.TelegramToken != "telegram-test-token" || got.GatewayURL != "http://localhost:8080" || got.InternalToken != "internal-test-token" {
		t.Fatalf("Load() = %#v", got)
	}
}

func TestLoadUsesConfiguredGatewayURL(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("GATEWAY_URL", "http://gateway.test:9000")
	t.Setenv("INTERNAL_TOKEN", "")
	got := Load()
	if got.GatewayURL != "http://gateway.test:9000" || got.TelegramToken != "" || got.InternalToken != "" {
		t.Fatalf("Load() = %#v", got)
	}
}
