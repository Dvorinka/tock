-- name: CreateUser :one
INSERT INTO users (id, name, email, password, timezone, week_start, is_placeholder, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'monday', false, now(), now())
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password = $2, updated_at = now() WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users SET name = $2, timezone = $3, week_start = $4,
    send_time_entry_still_running_email = $5,
    pending_email = $6,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetUserPendingEmail :exec
UPDATE users SET pending_email = $2, updated_at = now() WHERE id = $1;

-- name: ConfirmPendingEmail :exec
UPDATE users SET email = pending_email, pending_email = NULL,
    email_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: SetUserEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: SetUserCurrentOrganization :exec
UPDATE users SET current_team_id = $2, updated_at = now() WHERE id = $1;

-- name: SetUserRememberToken :exec
UPDATE users SET remember_token = $2, updated_at = now() WHERE id = $1;

-- name: SetUserTwoFactor :exec
UPDATE users SET two_factor_secret = $2, two_factor_recovery_codes = $3,
    two_factor_confirmed_at = $4, updated_at = now() WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- name: UpsertSession :exec
INSERT INTO sessions (id, user_id, ip_address, user_agent, payload, last_activity)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET user_id = $2, payload = $5, last_activity = $6;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE id = $1;

-- name: GetPersonalAccessClient :one
SELECT id FROM oauth_clients WHERE grant_types LIKE '%personal_access%' AND revoked = false LIMIT 1;

-- name: InsertAuthCode :exec
INSERT INTO oauth_auth_codes (id, user_id, client_id, scopes, revoked, expires_at)
VALUES ($1, $2, $3, $4, false, $5);

-- name: ConsumeAuthCode :one
DELETE FROM oauth_auth_codes WHERE id = $1 AND revoked = false AND expires_at > now()
RETURNING *;

-- name: InsertAccessToken :exec
INSERT INTO oauth_access_tokens (id, user_id, client_id, name, scopes, revoked, created_at, updated_at, expires_at)
VALUES ($1, $2, $3, $4, $5, false, now(), now(), $6);

-- name: GetAccessToken :one
SELECT * FROM oauth_access_tokens WHERE id = $1;

-- name: ListAccessTokensByUser :many
SELECT * FROM oauth_access_tokens
WHERE user_id = $1 AND revoked = false AND expires_at > now()
ORDER BY created_at DESC;

-- name: RevokeAccessToken :exec
UPDATE oauth_access_tokens SET revoked = true, updated_at = now() WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO oauth_refresh_tokens (id, access_token_id, revoked, expires_at)
VALUES ($1, $2, false, $3);

-- name: GetRefreshToken :one
SELECT * FROM oauth_refresh_tokens WHERE id = $1 AND revoked = false AND expires_at > now();

-- name: RevokeRefreshToken :exec
UPDATE oauth_refresh_tokens SET revoked = true WHERE id = $1;

-- name: RevokeRefreshByAccessToken :exec
UPDATE oauth_refresh_tokens SET revoked = true WHERE access_token_id = $1;

-- name: GetMembersByUser :many
SELECT m.*, o.name AS organization_name, o.personal_team AS organization_personal
FROM members m JOIN organizations o ON o.id = m.organization_id
WHERE m.user_id = $1;

-- name: GetMember :one
SELECT * FROM members WHERE user_id = $1 AND organization_id = $2;

-- name: CreateMember :exec
INSERT INTO members (id, user_id, organization_id, role, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now());

-- name: CreateOrganization :one
INSERT INTO organizations (id, user_id, name, personal_team, currency,
    number_format, currency_format, date_format, interval_format, time_format,
    employees_can_manage_tasks, breaks_enabled, employees_can_see_billable_rates,
    prevent_overlapping_time_entries, created_at, updated_at)
VALUES ($1, $2, $3, false, 'EUR',
    'point-comma', 'iso-code-after-with-space', 'hyphen-separated-yyyy-mm-dd',
    'hours-minutes', '24-hours',
    false, false, false, false, now(), now())
RETURNING *;

-- name: GetOrganization :one
SELECT * FROM organizations WHERE id = $1;

-- name: UpsertPasswordReset :exec
INSERT INTO password_reset_tokens (email, token, created_at)
VALUES ($1, $2, now())
ON CONFLICT (email) DO UPDATE SET token = $2, created_at = now();

-- name: GetPasswordReset :one
SELECT * FROM password_reset_tokens WHERE email = $1;

-- name: DeletePasswordReset :exec
DELETE FROM password_reset_tokens WHERE email = $1;
