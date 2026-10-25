-- name: GetUserByID :one
SELECT id, name, email, email_verified_at, timezone, week_start, current_team_id, is_placeholder
FROM users
WHERE id = $1
LIMIT 1;

-- name: GetUserByEmail :one
SELECT id, name, email, email_verified_at, timezone, week_start, current_team_id, is_placeholder, password
FROM users
WHERE email = $1
LIMIT 1;

-- name: UserWithEmailExists :one
SELECT EXISTS(
    SELECT 1 FROM users
    WHERE email = $1 AND is_placeholder = false AND id != $2
) AS exists;

-- name: ApplyPendingEmail :exec
UPDATE users SET email = $2, pending_email = NULL, email_verified_at = now(),
       updated_at = now()
WHERE id = $1;
