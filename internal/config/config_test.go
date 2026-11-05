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

func TestUpstreamLaravelEnv(t *testing.T) {
	t.Setenv("DB_HOST", "database")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_DATABASE", "solidtime")
	t.Setenv("DB_USERNAME", "solidtime")
	t.Setenv("DB_PASSWORD", "p@ss/word")
	t.Setenv("DB_SSL_MODE", "prefer")
	t.Setenv("APP_KEY", "base64:abc")
	t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
	t.Setenv("MAIL_FROM_ADDRESS", "no-reply@example.com")
	t.Setenv("MAIL_USERNAME", "apikey")
	t.Setenv("MAIL_PASSWORD", "secret")
	cfg := Load()
	const want = "postgres://solidtime:p%40ss%2Fword@database:5432/solidtime?sslmode=prefer"
	if cfg.DatabaseDSN != want {
		t.Fatalf("dsn: %s", cfg.DatabaseDSN)
	}
	if cfg.AppKey != "base64:abc" {
		t.Fatalf("app key: %s", cfg.AppKey)
	}
	if cfg.GotenbergURL != "http://gotenberg:3000" {
		t.Fatalf("gotenberg: %s", cfg.GotenbergURL)
	}
	if cfg.MailFrom != "no-reply@example.com" || cfg.SMTPUser != "apikey" || cfg.SMTPPassword != "secret" {
		t.Fatalf("mail: from=%s user=%s", cfg.MailFrom, cfg.SMTPUser)
	}
}

func TestUnknownSSLModeStaysDisabled(t *testing.T) {
	t.Setenv("DB_HOST", "database")
	t.Setenv("DB_SSL_MODE", "sometimes")
	cfg := Load()
	if cfg.DatabaseDSN != "postgres://postgres:@database:5432/solidtime?sslmode=disable" {
		t.Fatalf("dsn: %s", cfg.DatabaseDSN)
	}
}

func TestTockEnvWinsOverUpstream(t *testing.T) {
	t.Setenv("DB_HOST", "db")
	t.Setenv("TOCK_DB_DSN", "postgres://x:y@h:1/d?sslmode=disable")
	t.Setenv("TOCK_ADDR", ":9999")
	t.Setenv("PORT", "8000")
	t.Setenv("APP_KEY", "upstream")
	t.Setenv("TOCK_APP_KEY", "tock")
	t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
	t.Setenv("TOCK_GOTENBERG_URL", "http://127.0.0.1:3100")
	t.Setenv("MAIL_FROM_ADDRESS", "a@example.com")
	t.Setenv("TOCK_MAIL_FROM", "tock@example.com")
	cfg := Load()
	if cfg.DatabaseDSN != "postgres://x:y@h:1/d?sslmode=disable" {
		t.Fatalf("dsn: %s", cfg.DatabaseDSN)
	}
	if cfg.Addr != ":9999" {
		t.Fatalf("addr: %s", cfg.Addr)
	}
	if cfg.AppKey != "tock" || cfg.GotenbergURL != "http://127.0.0.1:3100" || cfg.MailFrom != "tock@example.com" {
		t.Fatalf("tock env did not win: key=%s gotenberg=%s from=%s", cfg.AppKey, cfg.GotenbergURL, cfg.MailFrom)
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

func TestLoginRateLimitDefaultsAndCanBeConfigured(t *testing.T) {
	t.Setenv("TOCK_RATE_LIMIT_LOGIN", "")
	if got := Load().LoginRequestsPer15Min; got != 10 {
		t.Fatalf("default login limit = %d, want 10", got)
	}

	t.Setenv("TOCK_RATE_LIMIT_LOGIN", "10000")
	if got := Load().LoginRequestsPer15Min; got != 10000 {
		t.Fatalf("configured login limit = %d, want 10000", got)
	}

	t.Setenv("TOCK_RATE_LIMIT_LOGIN", "0")
	if got := Load().LoginRequestsPer15Min; got != 10 {
		t.Fatalf("invalid login limit = %d, want default 10", got)
	}
}
