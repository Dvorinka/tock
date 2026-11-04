// Package seed inserts demo data matching upstream's DatabaseSeeder so the
// SPA is usable in development.
package seed

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// demoPasswordHash is bcrypt("password") — the same literal the upstream
// UserFactory uses.
const demoPasswordHash = "$2y$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi"

// Run seeds the database: OAuth clients (desktop + personal access) and one
// demo admin user with a personal organization.
func Run(ctx context.Context, pool *pgxpool.Pool, personalClientID, personalClientSecret string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := ensureOAuthClients(ctx, tx, personalClientID, personalClientSecret); err != nil {
		return err
	}
	if err := seedAdminUser(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit seed: %w", err)
	}
	return nil
}

// desktopClientID is the public authorization-code client created when none
// exists yet. A restored upstream dump already has its own row and is left alone.
const desktopClientID = "7f3c1a2e-6b54-4d8e-9a10-2c5b8e4f6d31"

// EnsureOAuthClients inserts the desktop and personal-access clients when the
// database has none. Existing clients, including ones from an upstream dump,
// are not modified. Safe to call on every serve.
func EnsureOAuthClients(ctx context.Context, pool *pgxpool.Pool, personalClientID, personalClientSecret string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureOAuthClients(ctx, tx, personalClientID, personalClientSecret); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit oauth clients: %w", err)
	}
	return nil
}

func ensureOAuthClients(ctx context.Context, tx pgx.Tx, personalClientID, personalClientSecret string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO oauth_clients (id, owner_id, name, secret, provider, revoked, grant_types, redirect_uris, owner_type, created_at, updated_at)
		SELECT $1, NULL, 'Desktop App', NULL, 'users', false, '["authorization_code","refresh_token"]', '["solidtime://oauth/callback"]', NULL, now(), now()
		WHERE NOT EXISTS (
			SELECT 1 FROM oauth_clients
			WHERE revoked = false AND redirect_uris LIKE '%solidtime://oauth/callback%'
		)
		ON CONFLICT (id) DO NOTHING`,
		desktopClientID); err != nil {
		return fmt.Errorf("insert desktop client: %w", err)
	}

	var missing bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM oauth_clients
			WHERE revoked = false AND grant_types LIKE '%personal_access%'
		)`).Scan(&missing); err != nil {
		return fmt.Errorf("check personal access client: %w", err)
	}
	if !missing {
		return nil
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(personalClientSecret), 10)
	if err != nil {
		return fmt.Errorf("hash client secret: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO oauth_clients (id, owner_id, name, secret, provider, revoked, grant_types, redirect_uris, owner_type, created_at, updated_at)
		SELECT $1, NULL, 'API', $2, 'users', false, '["personal_access"]', '["http://localhost"]', NULL, now(), now()
		WHERE NOT EXISTS (
			SELECT 1 FROM oauth_clients
			WHERE revoked = false AND grant_types LIKE '%personal_access%'
		)
		ON CONFLICT (id) DO NOTHING`,
		personalClientID, string(hashed)); err != nil {
		return fmt.Errorf("insert personal access client: %w", err)
	}
	return nil
}

func seedAdminUser(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = 'admin@example.com')`).Scan(&exists); err != nil {
		return fmt.Errorf("check admin user: %w", err)
	}
	if exists {
		return nil
	}

	userID := uuid.NewString()
	orgID := uuid.NewString()
	memberID := uuid.NewString()

	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, name, email, email_verified_at, password, is_placeholder, current_team_id, timezone, week_start, created_at, updated_at)
		VALUES ($1, 'Admin', 'admin@example.com', now(), $2, false, NULL, 'Europe/Vienna', 'monday', now(), now())`,
		userID, demoPasswordHash); err != nil {
		return fmt.Errorf("insert admin user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO organizations (id, user_id, name, personal_team, billable_rate, currency, employees_can_see_billable_rates, number_format, currency_format, date_format, interval_format, time_format, prevent_overlapping_time_entries, employees_can_manage_tasks, breaks_enabled, created_at, updated_at)
		VALUES ($1, $2, 'Admin''s Organization', true, NULL, 'EUR', false, 'point-comma', 'iso-code-after-with-space', 'hyphen-separated-yyyy-mm-dd', 'hours-minutes', '24-hours', false, false, false, now(), now())`,
		orgID, userID); err != nil {
		return fmt.Errorf("insert personal organization: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO members (id, organization_id, user_id, role, billable_rate, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', NULL, now(), now())`,
		memberID, orgID, userID); err != nil {
		return fmt.Errorf("insert owner member: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET current_team_id = $1 WHERE id = $2`, orgID, userID); err != nil {
		return fmt.Errorf("set current team: %w", err)
	}
	return nil
}

var ErrSeeded = errors.New("already seeded")
