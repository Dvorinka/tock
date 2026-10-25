-- name: ListGoalsPaginated :many
SELECT g.id, g.name, g.type, g.comparison, g.target_seconds, g.period,
       g.filters, g.timezone, g.week_start, g.archived_at, g.organization_id,
       g.member_id, g.created_at, g.updated_at, u.name AS member_name
FROM goals g
LEFT JOIN members m ON m.id = g.member_id
LEFT JOIN users u ON u.id = m.user_id
WHERE g.organization_id = sqlc.arg('organization_id')
  -- GoalsContract::scopeVisibleGoals: core only reaches personal goals of the acting member.
  AND g.type = 'personal'
  AND g.member_id = sqlc.arg('member_id')
  AND (g.type = coalesce(sqlc.narg('goal_type')::text, g.type))
  AND (CASE sqlc.arg('archived')::text
           WHEN 'true' THEN g.archived_at IS NOT NULL
           WHEN 'false' THEN g.archived_at IS NULL
           ELSE TRUE
       END)
ORDER BY g.created_at DESC, g.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountGoals :one
SELECT count(*)
FROM goals g
WHERE g.organization_id = sqlc.arg('organization_id')
  -- GoalsContract::scopeVisibleGoals: core only reaches personal goals of the acting member.
  AND g.type = 'personal'
  AND g.member_id = sqlc.arg('member_id')
  AND (g.type = coalesce(sqlc.narg('goal_type')::text, g.type))
  AND (CASE sqlc.arg('archived')::text
           WHEN 'true' THEN g.archived_at IS NOT NULL
           WHEN 'false' THEN g.archived_at IS NULL
           ELSE TRUE
       END);

-- name: GetGoalByID :one
SELECT g.id, g.name, g.type, g.comparison, g.target_seconds, g.period,
       g.filters, g.timezone, g.week_start, g.archived_at, g.organization_id,
       g.member_id, g.created_at, g.updated_at, u.name AS member_name
FROM goals g
LEFT JOIN members m ON m.id = g.member_id
LEFT JOIN users u ON u.id = m.user_id
WHERE g.id = sqlc.arg('id');

-- name: InsertGoal :exec
INSERT INTO goals (id, name, type, comparison, target_seconds, period, filters,
                   timezone, week_start, archived_at, organization_id, member_id,
                   created_at, updated_at)
VALUES (sqlc.arg('id'), sqlc.arg('name'), sqlc.arg('goal_type'),
        sqlc.arg('comparison'), sqlc.arg('target_seconds'), sqlc.arg('period'),
        sqlc.arg('filters'), sqlc.arg('timezone'), sqlc.arg('week_start'), NULL,
        sqlc.arg('organization_id'), sqlc.arg('member_id'), now(), now());

-- name: UpdateGoal :exec
UPDATE goals
SET name = sqlc.arg('name'),
    comparison = sqlc.arg('comparison'),
    target_seconds = sqlc.arg('target_seconds'),
    period = sqlc.arg('period'),
    filters = sqlc.arg('filters'),
    timezone = sqlc.arg('timezone'),
    week_start = sqlc.arg('week_start'),
    archived_at = sqlc.arg('archived_at'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: DeleteGoal :exec
DELETE FROM goals WHERE id = sqlc.arg('id');
