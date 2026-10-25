-- Audit log queries (phase 8). The audits table itself is upstream baseline.

-- name: InsertAudit :one
INSERT INTO audits (actor_type, actor_id, event, auditable_type, auditable_id,
    old_values, new_values, url, ip_address, user_agent, tags, owner_user_id,
    owner_organization_id, created_at, updated_at)
VALUES (sqlc.narg('actor_type'), sqlc.narg('actor_id'), sqlc.arg('event'),
    sqlc.arg('auditable_type'), sqlc.arg('auditable_id'), sqlc.arg('old_values'),
    sqlc.arg('new_values'), sqlc.narg('url'), sqlc.narg('ip_address'),
    sqlc.narg('user_agent'), sqlc.narg('tags'), sqlc.narg('owner_user_id'),
    sqlc.narg('owner_organization_id'), now(), now())
RETURNING id;

-- name: ListAuditsPaginated :many
SELECT a.id, a.actor_type, a.actor_id, u.name AS actor_name, a.event,
       a.auditable_type, a.auditable_id, a.old_values, a.new_values, a.url,
       coalesce(host(a.ip_address), '')::text AS ip_address, a.user_agent, a.tags, a.owner_user_id,
       a.owner_organization_id, a.created_at, a.updated_at
FROM audits a
LEFT JOIN users u ON u.id = a.actor_id
WHERE a.owner_organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('actor_id')::uuid IS NULL OR a.actor_id = sqlc.narg('actor_id')::uuid)
  AND (sqlc.narg('event')::text IS NULL OR a.event = sqlc.narg('event')::text)
  AND (sqlc.narg('auditable_type')::text IS NULL OR a.auditable_type = sqlc.narg('auditable_type')::text)
  AND (sqlc.narg('auditable_id')::uuid IS NULL OR a.auditable_id = sqlc.narg('auditable_id')::uuid)
  AND (sqlc.narg('from_at')::timestamp IS NULL OR a.created_at >= sqlc.narg('from_at')::timestamp)
  AND (sqlc.narg('to_at')::timestamp IS NULL OR a.created_at <= sqlc.narg('to_at')::timestamp)
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: GetAudit :one
SELECT a.id, a.actor_type, a.actor_id, u.name AS actor_name, a.event,
       a.auditable_type, a.auditable_id, a.old_values, a.new_values, a.url,
       coalesce(host(a.ip_address), '')::text AS ip_address, a.user_agent, a.tags, a.owner_user_id,
       a.owner_organization_id, a.created_at, a.updated_at
FROM audits a
LEFT JOIN users u ON u.id = a.actor_id
WHERE a.id = sqlc.arg('id');

-- name: CountAudits :one
SELECT count(*)
FROM audits a
WHERE a.owner_organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('actor_id')::uuid IS NULL OR a.actor_id = sqlc.narg('actor_id')::uuid)
  AND (sqlc.narg('event')::text IS NULL OR a.event = sqlc.narg('event')::text)
  AND (sqlc.narg('auditable_type')::text IS NULL OR a.auditable_type = sqlc.narg('auditable_type')::text)
  AND (sqlc.narg('auditable_id')::uuid IS NULL OR a.auditable_id = sqlc.narg('auditable_id')::uuid)
  AND (sqlc.narg('from_at')::timestamp IS NULL OR a.created_at >= sqlc.narg('from_at')::timestamp)
  AND (sqlc.narg('to_at')::timestamp IS NULL OR a.created_at <= sqlc.narg('to_at')::timestamp);
