package config

import "testing"

func TestUpstreamEnvCompat(t *testing.T) {
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_DATABASE", "solidtime")
	t.Setenv("DB_USERNAME", "root")
	t.Setenv("DB_PASSWORD", "root")
	t.Setenv("PORT", "8000")
	t.Setenv("APP_URL", "https://time.example.com")
	t.Setenv("MAIL_HOST", "mailpit")
	t.Setenv("MAIL_PORT", "1025")
	cfg := Load()
	if cfg.DatabaseDSN != "postgres://root:root@db:5432/solidtime?sslmode=disable" {
		t.Fatalf("dsn: %s", cfg.DatabaseDSN)
	}
	if cfg.Addr != ":8000" {
		t.Fatalf("addr: %s", cfg.Addr)
	}
	if cfg.AppURL != "https://time.example.com" {
		t.Fatalf("app url: %s", cfg.AppURL)
	}
	if cfg.SMTPAddr != "mailpit:1025" {
		t.Fatalf("smtp: %s", cfg.SMTPAddr)
	}
}

func TestTockEnvWinsOverUpstream(t *testing.T) {
	t.Setenv("DB_HOST", "db")
	t.Setenv("TOCK_DB_DSN", "postgres://x:y@h:1/d?sslmode=disable")
	t.Setenv("TOCK_ADDR", ":9999")
	t.Setenv("PORT", "8000")
	cfg := Load()
	if cfg.DatabaseDSN != "postgres://x:y@h:1/d?sslmode=disable" {
		t.Fatalf("dsn: %s", cfg.DatabaseDSN)
	}
	if cfg.Addr != ":9999" {
		t.Fatalf("addr: %s", cfg.Addr)
	}
}

func TestAutoDBMigrateDefaultsOnAndCanBeDisabled(t *testing.T) {
	t.Setenv("AUTO_DB_MIGRATE", "true")
	if !Load().AutoDBMigrate {
		t.Fatal("AUTO_DB_MIGRATE=true should enable startup migrations")
	}

	t.Setenv("AUTO_DB_MIGRATE", "false")
	if Load().AutoDBMigrate {
		t.Fatal("AUTO_DB_MIGRATE=false should disable startup migrations")
	}
}

func TestRegisterRateLimitDefaultsAndCanBeConfigured(t *testing.T) {
	t.Setenv("TOCK_RATE_LIMIT_REGISTER_PER_HOUR", "")
	if got := Load().RegisterRequestsPerHour; got != 100 {
		t.Fatalf("default register limit = %d, want 100", got)
	}

	t.Setenv("TOCK_RATE_LIMIT_REGISTER_PER_HOUR", "1000")
	if got := Load().RegisterRequestsPerHour; got != 1000 {
		t.Fatalf("configured register limit = %d, want 1000", got)
	}

	t.Setenv("TOCK_RATE_LIMIT_REGISTER_PER_HOUR", "0")
	if got := Load().RegisterRequestsPerHour; got != 100 {
		t.Fatalf("invalid register limit = %d, want default 100", got)
	}
}
