-- name: ImportListUsers :many
SELECT u.id, u.name, u.email FROM users u
JOIN members m ON m.user_id = u.id
WHERE m.organization_id = $1;

-- name: ImportListMembers :many
SELECT id, organization_id, user_id, role, billable_rate FROM members
WHERE organization_id = $1;

-- name: ImportListClients :many
SELECT id, name, organization_id, archived_at FROM clients
WHERE organization_id = $1;

-- name: ImportListProjects :many
SELECT id, name, color, billable_rate, is_public, client_id, organization_id, is_billable, archived_at, estimated_time FROM projects
WHERE organization_id = $1;

-- name: ImportListProjectMembers :many
SELECT pm.id, pm.billable_rate, pm.project_id, pm.user_id, pm.member_id FROM project_members pm
JOIN projects p ON pm.project_id = p.id
WHERE p.organization_id = $1;

-- name: ImportListTags :many
SELECT id, name, organization_id FROM tags
WHERE organization_id = $1;

-- name: ImportListTasks :many
SELECT id, name, project_id, organization_id, done_at, estimated_time FROM tasks
WHERE organization_id = $1;

-- name: ImportListInvitations :many
SELECT id, organization_id, email, role, accepted_at FROM organization_invitations
WHERE organization_id = $1;

-- name: ImportInsertUser :one
INSERT INTO users (id, name, email, password, timezone, week_start, is_placeholder, created_at, updated_at)
VALUES ($1, $2, $3, NULL, $4, 'monday', true, now(), now())
RETURNING id;

-- name: ImportInsertMember :one
INSERT INTO members (id, user_id, organization_id, role, billable_rate, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, now(), now())
RETURNING id, organization_id, user_id, role, billable_rate;

-- name: ImportInsertClient :one
INSERT INTO clients (id, name, organization_id, archived_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
RETURNING id, name, organization_id, archived_at;

-- name: ImportInsertTask :one
INSERT INTO tasks (id, name, project_id, organization_id, done_at, estimated_time, spent_time, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, NULL, 0, now(), now())
RETURNING id, name, project_id, organization_id, done_at, estimated_time;

-- name: ImportInsertTimeEntry :one
INSERT INTO time_entries (id, description, start, "end", billable_rate, billable,
  user_id, organization_id, project_id, task_id, tags, member_id, client_id,
  is_imported, still_active_email_sent_at, type, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, true, $14, $15, now(), now())
RETURNING id;

-- name: TryImportLock :one
SELECT pg_try_advisory_xact_lock(hashtext($1));
