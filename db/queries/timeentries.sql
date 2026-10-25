-- name: ListTagsPaginated :many
SELECT * FROM tags
WHERE organization_id = $1
ORDER BY created_at DESC, id
LIMIT $2 OFFSET $3;

-- name: CountTags :one
SELECT count(*) FROM tags WHERE organization_id = $1;

-- name: GetTagByID :one
SELECT * FROM tags WHERE id = $1;

-- name: InsertTag :one
INSERT INTO tags (id, name, organization_id, created_at, updated_at)
VALUES ($1, $2, $3, now(), now())
RETURNING *;

-- name: UpdateTagName :one
UPDATE tags SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteTag :exec
DELETE FROM tags WHERE id = $1;

-- name: TimeEntryExistsWithTag :one
SELECT EXISTS(
  SELECT 1 FROM time_entries
  WHERE organization_id = $1 AND tags @> to_jsonb($2::text)
);

-- name: ListClientsPaginated :many
SELECT c.* FROM clients c
WHERE c.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND c.archived_at IS NULL) OR ($2 = 2 AND c.archived_at IS NOT NULL))
  AND ($3::bool = false OR EXISTS (
      SELECT 1 FROM projects p
      WHERE p.client_id = c.id AND p.organization_id = $1 AND (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $4
        )
      )
  ))
ORDER BY c.created_at DESC, c.id
LIMIT $5 OFFSET $6;

-- name: CountClients :one
SELECT count(*) FROM clients c
WHERE c.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND c.archived_at IS NULL) OR ($2 = 2 AND c.archived_at IS NOT NULL))
  AND ($3::bool = false OR EXISTS (
      SELECT 1 FROM projects p
      WHERE p.client_id = c.id AND p.organization_id = $1 AND (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $4
        )
      )
  ));

-- name: GetClientByID :one
SELECT * FROM clients WHERE id = $1;

-- name: InsertClient :one
INSERT INTO clients (id, name, organization_id, created_at, updated_at)
VALUES ($1, $2, $3, now(), now())
RETURNING *;

-- name: UpdateClient :one
UPDATE clients SET name = $2, archived_at = $3, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteClient :exec
DELETE FROM clients WHERE id = $1;

-- name: ClientHasProjects :one
SELECT EXISTS(SELECT 1 FROM projects WHERE client_id = $1);

-- name: ListProjectsPaginated :many
SELECT p.* FROM projects p
WHERE p.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND p.archived_at IS NULL) OR ($2 = 2 AND p.archived_at IS NOT NULL))
  AND ($3::bool = false OR (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $4
        )
  ))
ORDER BY p.created_at DESC, p.id
LIMIT $5 OFFSET $6;

-- name: CountProjects :one
SELECT count(*) FROM projects p
WHERE p.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND p.archived_at IS NULL) OR ($2 = 2 AND p.archived_at IS NOT NULL))
  AND ($3::bool = false OR (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $4
        )
  ));

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: GetProjectByIDInOrg :one
SELECT * FROM projects WHERE id = $1 AND organization_id = $2;

-- name: InsertProject :one
INSERT INTO projects (id, name, color, billable_rate, is_public, client_id, organization_id,
  is_billable, archived_at, estimated_time, spent_time, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 0, now(), now())
RETURNING *;

-- name: UpdateProject :one
UPDATE projects SET name = $2, color = $3, billable_rate = $4, is_public = $5,
  client_id = $6, is_billable = $7, archived_at = $8, estimated_time = $9, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1;

-- name: DeleteProjectMembersForProject :exec
DELETE FROM project_members WHERE project_id = $1;

-- name: ProjectHasTasks :one
SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id = $1);

-- name: ProjectHasTimeEntries :one
SELECT EXISTS(SELECT 1 FROM time_entries WHERE project_id = $1);

-- name: UpdateTimeEntriesClientForProject :exec
UPDATE time_entries SET client_id = $3, updated_at = now()
WHERE organization_id = $1 AND project_id = $2;

-- name: ProjectVisibleToUser :one
SELECT EXISTS(
  SELECT 1 FROM projects p
  WHERE p.id = $1 AND p.organization_id = $2 AND (
    p.is_public OR EXISTS (
      SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $3
    )
  )
);

-- name: ListProjectMembersPaginated :many
SELECT * FROM project_members WHERE project_id = $1
ORDER BY created_at DESC, id
LIMIT $2 OFFSET $3;

-- name: CountProjectMembers :one
SELECT count(*) FROM project_members WHERE project_id = $1;

-- name: GetProjectMemberByID :one
SELECT pm.*, p.organization_id AS project_org_id
FROM project_members pm JOIN projects p ON p.id = pm.project_id
WHERE pm.id = $1;

-- name: GetProjectMember :one
SELECT * FROM project_members WHERE project_id = $1 AND member_id = $2;

-- name: ProjectMemberExists :one
SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND member_id = $2);

-- name: InsertProjectMember :one
INSERT INTO project_members (id, billable_rate, project_id, user_id, member_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, now(), now())
RETURNING *;

-- name: UpdateProjectMemberRate :one
UPDATE project_members SET billable_rate = $2, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteProjectMember :exec
DELETE FROM project_members WHERE id = $1;

-- name: ListTasksPaginated :many
SELECT t.* FROM tasks t
WHERE t.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND t.done_at IS NULL) OR ($2 = 2 AND t.done_at IS NOT NULL))
  AND ($3::uuid IS NULL OR t.project_id = $3)
  AND ($4::bool = false OR EXISTS (
      SELECT 1 FROM projects p
      WHERE p.id = t.project_id AND (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $5
        )
      )
  ))
ORDER BY t.created_at DESC, t.id
LIMIT $6 OFFSET $7;

-- name: CountTasks :one
SELECT count(*) FROM tasks t
WHERE t.organization_id = $1
  AND ($2::int = 0 OR ($2 = 1 AND t.done_at IS NULL) OR ($2 = 2 AND t.done_at IS NOT NULL))
  AND ($3::uuid IS NULL OR t.project_id = $3)
  AND ($4::bool = false OR EXISTS (
      SELECT 1 FROM projects p
      WHERE p.id = t.project_id AND (
        p.is_public OR EXISTS (
          SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $5
        )
      )
  ));

-- name: GetTaskByID :one
SELECT * FROM tasks WHERE id = $1;

-- name: GetTaskByIDInProject :one
SELECT * FROM tasks WHERE id = $1 AND project_id = $2 AND organization_id = $3;

-- name: TaskExistsInProject :one
SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1 AND project_id = $2 AND organization_id = $3);

-- name: InsertTask :one
INSERT INTO tasks (id, name, project_id, organization_id, estimated_time, spent_time, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 0, now(), now())
RETURNING *;

-- name: UpdateTask :one
UPDATE tasks SET name = $2, done_at = $3, estimated_time = $4, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteTask :exec
DELETE FROM tasks WHERE id = $1;

-- name: TaskHasTimeEntries :one
SELECT EXISTS(SELECT 1 FROM time_entries WHERE task_id = $1);

-- name: GetTimeEntryByID :one
SELECT * FROM time_entries WHERE id = $1;

-- name: InsertTimeEntry :one
INSERT INTO time_entries (id, description, start, "end", billable_rate, billable,
  user_id, organization_id, project_id, task_id, tags, member_id, client_id,
  is_imported, type, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, false, $14, now(), now())
RETURNING *;

-- name: UpdateTimeEntry :one
UPDATE time_entries SET description = $2, start = $3, "end" = $4, billable_rate = $5,
  billable = $6, user_id = $7, project_id = $8, task_id = $9, tags = $10,
  member_id = $11, client_id = $12, type = $13, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteTimeEntry :exec
DELETE FROM time_entries WHERE id = $1;

-- name: RunningTimeEntryForUser :one
SELECT * FROM time_entries WHERE user_id = $1 AND "end" IS NULL
ORDER BY start DESC;

-- name: CountRunningTimeEntriesForUser :one
SELECT count(*) FROM time_entries WHERE user_id = $1 AND "end" IS NULL;

-- name: OverlappingTimeEntryExists :one
SELECT EXISTS(
  SELECT 1 FROM time_entries
  WHERE organization_id = $1 AND member_id = $2
    AND ($3::uuid IS NULL OR id <> $3)
    AND (
      ("end" > $4 AND start < $4)
      OR ($5::timestamp IS NOT NULL AND (
        (start < $5 AND "end" > $5)
        OR (start >= $4 AND "end" <= $5)
      ))
    )
);

-- name: MemberBillableRateForTimeEntry :one
-- BillableRateService::getBillableRateForTimeEntry precedence:
-- project_member > project > member > organization
SELECT coalesce(
  (SELECT pm.billable_rate FROM project_members pm
   WHERE pm.user_id = $2 AND pm.project_id = $3 AND pm.billable_rate IS NOT NULL),
  (SELECT p.billable_rate FROM projects p
   WHERE p.id = $3 AND p.billable_rate IS NOT NULL),
  (SELECT m.billable_rate FROM members m
   WHERE m.user_id = $2 AND m.organization_id = $1 AND m.billable_rate IS NOT NULL),
  (SELECT o.billable_rate FROM organizations o
   WHERE o.id = $1 AND o.billable_rate IS NOT NULL)
);

-- name: PropagateBillableRateForProjectMember :exec
-- BillableRateService::updateTimeEntriesBillableRateForProjectMember
UPDATE time_entries SET billable_rate = $3, updated_at = now()
WHERE billable = true AND member_id = $1 AND project_id = $2;

-- name: PropagateBillableRateForProject :exec
-- BillableRateService::updateTimeEntriesBillableRateForProject
UPDATE time_entries SET billable_rate = $3, updated_at = now()
WHERE billable = true AND organization_id = $1 AND time_entries.project_id = $2
  AND member_id NOT IN (
    SELECT pm.member_id FROM project_members pm
    WHERE pm.project_id = $2 AND pm.billable_rate IS NOT NULL
  );

-- name: RecalculateSpentTimeForProject :exec
UPDATE projects SET spent_time = coalesce((
  SELECT sum(extract(epoch from ("end" - start)))::bigint
  FROM time_entries WHERE project_id = $1 AND "end" IS NOT NULL
), 0)
WHERE id = $1;

-- name: RecalculateSpentTimeForTask :exec
UPDATE tasks SET spent_time = coalesce((
  SELECT sum(extract(epoch from ("end" - start)))::bigint
  FROM time_entries WHERE task_id = $1 AND "end" IS NOT NULL
), 0)
WHERE id = $1;

-- name: DescriptorNamesForIDs :many
-- loadDescriptorsMap per type is resolved in Go (per-table lookups); these
-- four cover client/user/project/task/tag.
SELECT id, name, NULL::text AS color FROM clients WHERE id = ANY($1::uuid[])
UNION ALL SELECT id, name, NULL FROM users WHERE id = ANY($1::uuid[])
UNION ALL SELECT id, name, color FROM projects WHERE id = ANY($1::uuid[])
UNION ALL SELECT id, name, NULL FROM tasks WHERE id = ANY($1::uuid[])
UNION ALL SELECT id, name, NULL FROM tags WHERE id = ANY($1::uuid[]);

-- name: TimeEntriesForChartAggregate :many
-- getDailyTrackedHours / getWeeklyHistory: per-date sums in user tz
SELECT DATE(start + make_interval(secs => $2::int)) AS date,
       round(sum(extract(epoch from (coalesce("end", now()) - start))))::bigint AS aggregate
FROM time_entries
WHERE user_id = $1 AND organization_id = $3 AND type = 'work'
  AND start BETWEEN $4 AND $5
GROUP BY DATE(start + make_interval(secs => $2::int))
ORDER BY date;

-- name: TimeEntriesSumBetween :one
SELECT coalesce(round(sum(extract(epoch from (coalesce("end", now()) - start)))), 0)::bigint
FROM time_entries
WHERE user_id = $1 AND organization_id = $2 AND type = 'work'
  AND start BETWEEN $3 AND $4;

-- name: TimeEntriesBillableSumBetween :one
SELECT coalesce(round(sum(extract(epoch from (coalesce("end", now()) - start)))), 0)::bigint
FROM time_entries
WHERE user_id = $1 AND organization_id = $2 AND billable = true
  AND start BETWEEN $3 AND $4;

-- name: TimeEntriesBillableAmountBetween :one
SELECT coalesce(round(sum(extract(epoch from (coalesce("end", now()) - start)) * (billable_rate::float/60/60))), 0)::bigint
FROM time_entries
WHERE user_id = $1 AND organization_id = $2 AND billable = true AND billable_rate IS NOT NULL
  AND start BETWEEN $3 AND $4;

-- name: WeeklyProjectOverview :many
SELECT project_id,
       round(sum(extract(epoch from (coalesce("end", now()) - start))))::bigint AS aggregate
FROM time_entries
WHERE user_id = $1 AND organization_id = $2 AND type = 'work'
  AND start BETWEEN $3 AND $4
GROUP BY project_id;

-- name: ProjectNamesForChart :many
SELECT id, name, color FROM projects
WHERE organization_id = $1 AND id = ANY($2::uuid[]);

-- name: LatestTeamActivity :many
SELECT DISTINCT ON (te.member_id) te.member_id, te.description, te.id, te.task_id, te."end", te.start, u.name AS user_name
FROM time_entries te
JOIN members m ON m.id = te.member_id
JOIN users u ON u.id = m.user_id
WHERE te.organization_id = $1
ORDER BY te.member_id, te.start DESC;

-- name: LatestTasks :many
SELECT t.id, t.name, p.name AS project_name, t.project_id AS t_project_id
FROM tasks t JOIN projects p ON p.id = t.project_id
WHERE t.organization_id = $1
  AND EXISTS (
    SELECT 1 FROM time_entries te
    WHERE te.task_id = t.id AND te.user_id = $2 AND te.organization_id = $1
  )
ORDER BY (SELECT max(te.start) FROM time_entries te
          WHERE te.task_id = t.id AND te.user_id = $2 AND te.organization_id = $1) DESC
LIMIT 4;

-- name: LastSevenDaysWindows :many
-- DashboardService::lastSevenDays — 3h windows over the last 7 days
SELECT time_ranges.start,
       EXTRACT(epoch FROM sum(
         LEAST(time_ranges."end", coalesce(time_entries."end", $5::timestamp)) -
         GREATEST(time_ranges.start, time_entries.start)
       ))::bigint AS aggregate
FROM (
  SELECT time_range_starts.start AS start, time_range_starts.start + interval '3 hours' AS "end"
  FROM generate_series($1::timestamp, $2::timestamp + interval '3 hours', interval '3 hours') AS time_range_starts (start)
) time_ranges
JOIN time_entries ON time_entries.start < time_ranges."end"
                 AND coalesce(time_entries."end", $5::timestamp) > time_ranges.start
WHERE time_entries.user_id = $3 AND time_entries.organization_id = $4
  AND time_entries.type = 'work'
GROUP BY time_ranges.start
ORDER BY time_ranges.start;
