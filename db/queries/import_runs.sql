-- name: CreateImportRun :one
INSERT INTO import_runs (id, organization_id, user_id, importer_type, timezone, data)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: GetImportRun :one
SELECT id, status, report, error, created_at, completed_at
FROM import_runs
WHERE id = $1 AND organization_id = $2;

-- name: GetImportRunForProcessing :one
SELECT id, organization_id, importer_type, timezone, data, status
FROM import_runs
WHERE id = $1
FOR UPDATE;

-- name: MarkImportRunProcessing :exec
UPDATE import_runs SET status = 'processing', updated_at = now()
WHERE id = $1 AND status = 'queued';

-- name: CompleteImportRun :exec
UPDATE import_runs SET status = 'completed', data = NULL, report = $2,
    error = NULL, completed_at = now(), updated_at = now()
WHERE id = $1 AND status = 'processing';

-- name: FailImportRun :exec
UPDATE import_runs SET status = 'failed', data = NULL, report = NULL,
    error = $2, completed_at = now(), updated_at = now()
WHERE id = $1 AND status = 'processing';
