// Package config loads process configuration from the environment.
package config

import (
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration. Defaults match docker-compose.dev.yml.
type Config struct {
	Addr         string // TOCK_ADDR, default ":8080"
	DatabaseDSN  string // TOCK_DB_DSN
	AppURL       string // TOCK_APP_URL, default http://localhost:8080
	AppEnv       string // TOCK_ENV, default "local"
	GotenbergURL string // TOCK_GOTENBERG_URL, default http://localhost:3100
	// Dev-only defaults matching upstream .env.example.
	PersonalAccessClientID     string   // TOCK_PERSONAL_ACCESS_CLIENT_ID
	PersonalAccessClientSecret string   // TOCK_PERSONAL_ACCESS_CLIENT_SECRET
	AppKey                     string   // TOCK_APP_KEY or APP_KEY — session/CSRF HMAC key
	SMTPAddr                   string   // TOCK_SMTP_ADDR — mailpit in dev
	SMTPUser                   string   // MAIL_USERNAME — empty skips SMTP AUTH
	SMTPPassword               string   // MAIL_PASSWORD
	MailFrom                   string   // TOCK_MAIL_FROM or MAIL_FROM_ADDRESS
	OAuthKeyDir                string   // TOCK_OAUTH_KEY_DIR — RSA keypair storage
	StorageDir                 string   // TOCK_STORAGE_DIR — private disk root (exports)
	SecureCookies              bool     // TOCK_SECURE_COOKIES
	SuperAdmins                []string // SUPER_ADMINS — emails allowed into the admin panel
	Registration               bool     // TOCK_ENABLE_REGISTRATION
	RegisterRequestsPerHour    int      // TOCK_RATE_LIMIT_REGISTER_PER_HOUR, default 100
	LoginRequestsPer15Min      int      // TOCK_RATE_LIMIT_LOGIN, default 10 per 15 minutes
	AutoDBMigrate              bool     // AUTO_DB_MIGRATE — apply migrations on serve startup
	Logger                     *slog.Logger
}

// Load reads configuration from environment variables.
func Load() Config {
	// Upstream solidtime env names are accepted when TOCK_* are unset, so
	// an existing laravel.env keeps working. DB_* becomes a DSN;
	// APP_URL/APP_KEY/PORT/GOTENBERG_URL/MAIL_* mirror upstream.
	dsn := env("TOCK_DB_DSN", "")
	if dsn == "" {
		if host := os.Getenv("DB_HOST"); host != "" {
			dsn = postgresDSN(
				env("DB_USERNAME", "postgres"), os.Getenv("DB_PASSWORD"),
				host, env("DB_PORT", "5432"), env("DB_DATABASE", "solidtime"), sslMode(),
			)
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
	autoMigrate := env("AUTO_DB_MIGRATE", "true")
	cfg := Config{
		SuperAdmins:                strings.Split(env("SUPER_ADMINS", ""), ","),
		Addr:                       addr,
		DatabaseDSN:                dsn,
		AppURL:                     env("TOCK_APP_URL", env("APP_URL", "http://localhost:8080")),
		AppEnv:                     env("TOCK_ENV", "local"),
		GotenbergURL:               env("TOCK_GOTENBERG_URL", env("GOTENBERG_URL", "http://127.0.0.1:3100")),
		PersonalAccessClientID:     env("TOCK_PERSONAL_ACCESS_CLIENT_ID", "9e27f54d-5dfb-4dde-99d7-834518236c92"),
		PersonalAccessClientSecret: env("TOCK_PERSONAL_ACCESS_CLIENT_SECRET", "EL5mXp3aF8ITjcwoOXRpbSK7zGrWhW4zTDpQXTkf"),
		AppKey:                     env("TOCK_APP_KEY", env("APP_KEY", "")),
		SMTPAddr:                   smtp,
		SMTPUser:                   os.Getenv("MAIL_USERNAME"),
		SMTPPassword:               os.Getenv("MAIL_PASSWORD"),
		MailFrom:                   env("TOCK_MAIL_FROM", env("MAIL_FROM_ADDRESS", "tock@localhost")),
		OAuthKeyDir:                env("TOCK_OAUTH_KEY_DIR", "storage"),
		StorageDir:                 env("TOCK_STORAGE_DIR", "storage"),
		SecureCookies:              env("TOCK_SECURE_COOKIES", "false") == "true",
		Registration:               env("TOCK_ENABLE_REGISTRATION", env("APP_ENABLE_REGISTRATION", "true")) == "true" || os.Getenv("APP_ENABLE_REGISTRATION") == "on",
		RegisterRequestsPerHour:    envInt("TOCK_RATE_LIMIT_REGISTER_PER_HOUR", 100),
		LoginRequestsPer15Min:      envInt("TOCK_RATE_LIMIT_LOGIN", 10),
		AutoDBMigrate:              autoMigrate == "true" || autoMigrate == "on",
	}
	cfg.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return cfg
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v < 1 {
		return fallback
	}
	return v
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// sslMode maps solidtime's DB_SSL_MODE onto libpq. Unknown values stay
// disabled so a typo cannot turn a local socket into a TLS requirement.
func sslMode() string {
	switch os.Getenv("DB_SSL_MODE") {
	case "allow", "prefer", "require", "verify-ca", "verify-full":
		return os.Getenv("DB_SSL_MODE")
	default:
		return "disable"
	}
}

func postgresDSN(user, password, host, port, database, mode string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + database,
		RawQuery: "sslmode=" + mode,
	}
	return u.String()
}
