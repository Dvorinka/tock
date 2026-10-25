-- name: ListReportsPaginated :many
SELECT id, name, description, is_public, share_secret, properties,
       public_until, organization_id, created_at, updated_at
FROM reports
WHERE organization_id = $1
ORDER BY created_at DESC, id
LIMIT $2 OFFSET $3;

-- name: CountReports :one
SELECT count(*) FROM reports WHERE organization_id = $1;

-- name: GetReportByID :one
SELECT id, name, description, is_public, share_secret, properties,
       public_until, organization_id, created_at, updated_at
FROM reports WHERE id = $1;

-- name: GetReportByShareSecret :one
SELECT r.id, r.name, r.description, r.is_public, r.share_secret, r.properties,
       r.public_until, r.organization_id, r.created_at, r.updated_at,
       o.currency, o.currency_format, o.number_format, o.date_format,
       o.interval_format, o.time_format
FROM reports r
JOIN organizations o ON o.id = r.organization_id
WHERE r.share_secret = $1 AND r.is_public = true
  AND (r.public_until IS NULL OR r.public_until > now());

-- name: InsertReport :one
INSERT INTO reports (id, name, description, is_public, share_secret,
                     properties, public_until, organization_id,
                     created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
RETURNING id;

-- name: UpdateReport :exec
UPDATE reports SET name = $2, description = $3, is_public = $4,
                   share_secret = $5, public_until = $6, updated_at = now()
WHERE id = $1;

-- name: DeleteReport :exec
DELETE FROM reports WHERE id = $1;
