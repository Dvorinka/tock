// Package config loads process configuration from the environment.
package config

import (
	"log/slog"
	"os"
)

// Config holds runtime configuration. Defaults match docker-compose.dev.yml.
type Config struct {
	Addr         string // TOCK_ADDR, default ":8080"
	DatabaseDSN  string // TOCK_DB_DSN
	AppURL       string // TOCK_APP_URL, default http://localhost:8080
	AppEnv       string // TOCK_ENV, default "local"
	GotenbergURL string // TOCK_GOTENBERG_URL, default http://localhost:3100
	// Dev-only defaults matching upstream .env.example.
	PersonalAccessClientID     string // TOCK_PERSONAL_ACCESS_CLIENT_ID
	PersonalAccessClientSecret string // TOCK_PERSONAL_ACCESS_CLIENT_SECRET
	AppKey             string // TOCK_APP_KEY — session/CSRF HMAC key
	SMTPAddr           string // TOCK_SMTP_ADDR — mailpit in dev
	OAuthKeyDir        string // TOCK_OAUTH_KEY_DIR — RSA keypair storage
	StorageDir         string // TOCK_STORAGE_DIR — private disk root (exports)
	SecureCookies      bool   // TOCK_SECURE_COOKIES
	Registration       bool   // TOCK_ENABLE_REGISTRATION
	Logger                     *slog.Logger
}

// Load reads configuration from environment variables.
func Load() Config {
	// Upstream solidtime env names are accepted when TOCK_* are unset, so
	// switching a deployment is a one-line image change. DB_* becomes a DSN;
	// APP_URL/PORT mirror upstream; MAIL_HOST/MAIL_PORT map to SMTPAddr.
	dsn := env("TOCK_DB_DSN", "")
	if dsn == "" {
		if host := os.Getenv("DB_HOST"); host != "" {
			dsn = "postgres://" + env("DB_USERNAME", "postgres") + ":" + os.Getenv("DB_PASSWORD") +
				"@" + host + ":" + env("DB_PORT", "5432") + "/" + env("DB_DATABASE", "solidtime") + "?sslmode=disable"
		} else {
			dsn = "postgres://tock:tock@127.0.0.1:54329/tock?sslmode=disable"
		}
	}
	addr := env("TOCK_ADDR", "")
	if addr == "" {
		addr = ":" + env("PORT", "8080")
	}
	smtp := env("TOCK_SMTP_ADDR", "")
	if smtp == "" {
		if host := os.Getenv("MAIL_HOST"); host != "" {
			smtp = host + ":" + env("MAIL_PORT", "1025")
		} else {
			smtp = "127.0.0.1:1025"
		}
	}
	cfg := Config{
		Addr:                       addr,
		DatabaseDSN:                dsn,
		AppURL:                     env("TOCK_APP_URL", env("APP_URL", "http://localhost:8080")),
		AppEnv:                     env("TOCK_ENV", "local"),
		GotenbergURL:               env("TOCK_GOTENBERG_URL", "http://127.0.0.1:3100"),
		PersonalAccessClientID:     env("TOCK_PERSONAL_ACCESS_CLIENT_ID", "9e27f54d-5dfb-4dde-99d7-834518236c92"),
		PersonalAccessClientSecret: env("TOCK_PERSONAL_ACCESS_CLIENT_SECRET", "EL5mXp3aF8ITjcwoOXRpbSK7zGrWhW4zTDpQXTkf"),
		AppKey:             env("TOCK_APP_KEY", ""),
		SMTPAddr:           smtp,
		OAuthKeyDir:        env("TOCK_OAUTH_KEY_DIR", "storage"),
		StorageDir:         env("TOCK_STORAGE_DIR", "storage"),
		SecureCookies:      env("TOCK_SECURE_COOKIES", "false") == "true",
		Registration:       env("TOCK_ENABLE_REGISTRATION", env("APP_ENABLE_REGISTRATION", "true")) == "true" || os.Getenv("APP_ENABLE_REGISTRATION") == "on",
	}
	cfg.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return cfg
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
