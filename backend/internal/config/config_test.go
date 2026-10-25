package config

import (
	"strings"
	"testing"
	"time"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"DATABASE_HOST":     "localhost",
		"DATABASE_NAME":     "bible_platform",
		"DATABASE_USER":     "bible_app",
		"DATABASE_PASSWORD": "test-password",
		"REDIS_ADDR":        "localhost:6379",
		"REDIS_PASSWORD":    "test-redis-password",
	}
}

func TestLoadUsesSafeDefaults(t *testing.T) {
	cfg, err := load(lookupFrom(validEnvironment()))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.HTTPReadHeaderTimeout != 5*time.Second {
		t.Errorf("HTTPReadHeaderTimeout = %s, want 5s", cfg.HTTPReadHeaderTimeout)
	}
	if cfg.Database.Port != 5432 || cfg.Database.MaxConns != 10 || cfg.Database.MinConns != 1 {
		t.Errorf("unexpected database defaults: %+v", cfg.Database)
	}
	if cfg.TelegramBotToken != "" {
		t.Errorf("TelegramBotToken = %q, want empty when not configured", cfg.TelegramBotToken)
	}
	if cfg.Redis.Addr != "localhost:6379" || cfg.Redis.Password != "test-redis-password" || cfg.Redis.DB != 0 || cfg.Redis.ConnectTimeout != 3*time.Second {
		t.Errorf("unexpected Redis defaults: %+v", cfg.Redis)
	}
}

func TestLoadTrimsOptionalTelegramToken(t *testing.T) {
	values := validEnvironment()
	values["TELEGRAM_BOT_TOKEN"] = "  test-token-value  "

	cfg, err := load(lookupFrom(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.TelegramBotToken != "test-token-value" {
		t.Fatalf("TelegramBotToken = %q, want trimmed token", cfg.TelegramBotToken)
	}
}

func TestLoadRejectsMissingRequiredDatabaseValues(t *testing.T) {
	tests := []string{"DATABASE_HOST", "DATABASE_NAME", "DATABASE_USER", "DATABASE_PASSWORD", "REDIS_PASSWORD"}
	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			values := validEnvironment()
			delete(values, key)
			_, err := load(lookupFrom(values))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected an error naming %s, got %v", key, err)
			}
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "listen address", key: "HTTP_ADDR", value: "not-an-address"},
		{name: "negative timeout", key: "HTTP_READ_TIMEOUT", value: "-1s"},
		{name: "malformed timeout", key: "HTTP_IDLE_TIMEOUT", value: "soon"},
		{name: "database port", key: "DATABASE_PORT", value: "70000"},
		{name: "max connections", key: "DATABASE_MAX_CONNS", value: "0"},
		{name: "min connections", key: "DATABASE_MIN_CONNS", value: "11"},
		{name: "ssl mode", key: "DATABASE_SSLMODE", value: "unsafe"},
		{name: "Redis address", key: "REDIS_ADDR", value: "not-an-address"},
		{name: "Redis database", key: "REDIS_DB", value: "16"},
		{name: "Redis timeout", key: "REDIS_CONNECT_TIMEOUT", value: "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validEnvironment()
			values[tt.key] = tt.value
			if tt.key == "DATABASE_MIN_CONNS" {
				values["DATABASE_MAX_CONNS"] = "10"
			}
			_, err := load(lookupFrom(values))
			if err == nil {
				t.Fatalf("expected invalid %s to be rejected", tt.key)
			}
		})
	}
}

func TestLoadAcceptsOverrides(t *testing.T) {
	values := validEnvironment()
	values["HTTP_ADDR"] = "127.0.0.1:9090"
	values["HTTP_READ_TIMEOUT"] = "20s"
	values["DATABASE_PORT"] = "5544"
	values["DATABASE_MIN_CONNS"] = "0"
	cfg, err := load(lookupFrom(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.HTTPReadTimeout != 20*time.Second || cfg.Database.Port != 5544 || cfg.Database.MinConns != 0 {
		t.Fatalf("unexpected overridden config: %+v", cfg)
	}
}
