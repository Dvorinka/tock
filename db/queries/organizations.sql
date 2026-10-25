-- Organizations -----------------------------------------------------------

-- name: InsertOrganization :one
INSERT INTO organizations (id, user_id, name, personal_team, currency,
    number_format, currency_format, date_format, interval_format, time_format,
    employees_can_manage_tasks, breaks_enabled, employees_can_see_billable_rates,
    prevent_overlapping_time_entries, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5,
    'point-comma', 'iso-code-after-with-space', 'hyphen-separated-yyyy-mm-dd',
    'hours-minutes', '24-hours', false, false, false, false, now(), now())
RETURNING *;

-- name: UpdateOrganization :one
UPDATE organizations SET name = $2, currency = $3, billable_rate = $4,
    employees_can_see_billable_rates = $5, employees_can_manage_tasks = $6,
    prevent_overlapping_time_entries = $7, breaks_enabled = $8,
    number_format = $9, currency_format = $10, date_format = $11,
    interval_format = $12, time_format = $13, user_id = $14, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetOrganizationOwner :exec
UPDATE organizations SET user_id = $2, updated_at = now() WHERE id = $1;

-- name: DeleteOrganizationRow :exec
DELETE FROM organizations WHERE id = $1;

-- Members -----------------------------------------------------------------

-- name: GetMemberByID :one
SELECT * FROM members WHERE id = $1;

-- name: ListMembersPaginated :many
SELECT m.id, m.organization_id, m.user_id, m.role, m.billable_rate, m.created_at,
    u.name AS user_name, u.email AS user_email, u.is_placeholder AS user_is_placeholder
FROM members m JOIN users u ON u.id = m.user_id
WHERE m.organization_id = $1
ORDER BY m.created_at DESC, m.id
LIMIT $2 OFFSET $3;

-- name: CountMembers :one
SELECT count(*) FROM members WHERE organization_id = $1;

-- name: IsEmailMemberOfOrganization :one
SELECT EXISTS(
    SELECT 1 FROM members m JOIN users u ON u.id = m.user_id
    WHERE m.organization_id = $1 AND u.email = $2 AND m.role <> 'placeholder'
);

-- name: UpdateMemberBillableRate :exec
UPDATE members SET billable_rate = $2, updated_at = now() WHERE id = $1;

-- name: UpdateMemberRole :exec
UPDATE members SET role = $2, updated_at = now() WHERE id = $1;

-- name: UpdateMemberUser :exec
UPDATE members SET user_id = $2, updated_at = now() WHERE id = $1;

-- name: DeleteMember :exec
DELETE FROM members WHERE id = $1;

-- name: DemoteOtherOwners :exec
UPDATE members SET role = 'admin', updated_at = now()
WHERE organization_id = $1 AND role = 'owner' AND id <> $2;

-- name: PlaceholderMembersByEmail :many
SELECT m.id AS member_id, u.id AS user_id FROM members m JOIN users u ON u.id = m.user_id
WHERE m.organization_id = $1 AND u.is_placeholder = true AND u.email = $2;

-- name: MemberExistsForUser :one
SELECT EXISTS(SELECT 1 FROM members WHERE organization_id = $1 AND user_id = $2);

-- name: CountUserMemberships :one
SELECT count(*) FROM members WHERE user_id = $1;

-- name: FirstUserOrganization :one
SELECT organization_id FROM members WHERE user_id = $1 ORDER BY created_at LIMIT 1;

-- name: ListMembersForOrg :many
SELECT m.id AS member_id, m.role, u.id AS user_id, u.is_placeholder
FROM members m JOIN users u ON u.id = m.user_id
WHERE m.organization_id = $1;

-- name: DeleteMembersForOrg :exec
DELETE FROM members WHERE organization_id = $1;

-- name: ClearCurrentTeamForOrg :exec
UPDATE users SET current_team_id = NULL, updated_at = now() WHERE current_team_id = $1;

-- name: ClearUserCurrentTeam :exec
UPDATE users SET current_team_id = NULL, updated_at = now() WHERE id = $1;

-- name: InsertPlaceholderUser :one
INSERT INTO users (id, name, email, is_placeholder, timezone, week_start,
    send_time_entry_still_running_email, created_at, updated_at)
SELECT $2, name, email, true, timezone, week_start,
    send_time_entry_still_running_email, now(), now()
FROM users WHERE users.id = $1
RETURNING *;

-- name: DeleteUserByID :exec
DELETE FROM users WHERE id = $1;

-- Invitations -------------------------------------------------------------

-- name: ListInvitationsPaginated :many
SELECT * FROM organization_invitations
WHERE organization_id = $1
ORDER BY created_at DESC, id
LIMIT $2 OFFSET $3;

-- name: CountInvitations :one
SELECT count(*) FROM organization_invitations WHERE organization_id = $1;

-- name: GetInvitation :one
SELECT * FROM organization_invitations WHERE id = $1;

-- name: InvitationExistsForEmail :one
SELECT EXISTS(SELECT 1 FROM organization_invitations
    WHERE organization_id = $1 AND email = $2);

-- name: CreateInvitation :one
INSERT INTO organization_invitations (id, organization_id, email, role, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
RETURNING *;

-- name: MarkInvitationAccepted :exec
UPDATE organization_invitations SET accepted_at = now()
WHERE id = $1 AND accepted_at IS NULL;

-- name: DeleteInvitation :exec
DELETE FROM organization_invitations WHERE id = $1;

-- name: AcceptedInvitationsForEmail :many
SELECT * FROM organization_invitations
WHERE lower(email) = lower($1) AND accepted_at IS NOT NULL;

-- Entity reassignment (merge / placeholder transitions) --------------------

-- name: DeleteRunningTimeEntriesForMember :exec
DELETE FROM time_entries
WHERE organization_id = $1 AND member_id = $2 AND "end" IS NULL;

-- name: ReassignTimeEntriesToMember :exec
UPDATE time_entries te SET user_id = $3, member_id = $2, updated_at = now()
WHERE te.organization_id = $1 AND te.member_id = $4;

-- name: ReassignTimeEntriesToUser :exec
UPDATE time_entries te SET user_id = $3, updated_at = now()
WHERE te.organization_id = $1 AND te.user_id = $2;

-- name: ReassignProjectMembersToMember :exec
UPDATE project_members pm SET user_id = $4, member_id = $3, updated_at = now()
WHERE pm.member_id = $2
  AND pm.project_id IN (SELECT id FROM projects WHERE organization_id = $1)
  AND pm.project_id NOT IN (SELECT pm2.project_id FROM project_members pm2 WHERE pm2.member_id = $3);

-- name: DeleteProjectMembersForMember :exec
DELETE FROM project_members pm
WHERE pm.member_id = $2
  AND pm.project_id IN (SELECT id FROM projects WHERE organization_id = $1);

-- name: ReassignProjectMembersToUser :exec
UPDATE project_members pm SET user_id = $3, updated_at = now()
WHERE pm.user_id = $2
  AND pm.project_id IN (SELECT id FROM projects WHERE organization_id = $1);

-- name: ReassignGoalsToMember :exec
UPDATE goals SET member_id = $3, updated_at = now()
WHERE organization_id = $1 AND member_id = $2;

-- name: GoalsWithMemberFilter :many
SELECT id, filters FROM goals
WHERE organization_id = $1 AND filters -> 'memberIds' @> to_jsonb($2::text);

-- name: UpdateGoalFilters :exec
UPDATE goals SET filters = $2, updated_at = now() WHERE id = $1;

-- Member removal -----------------------------------------------------------

-- name: TimeEntriesExistForMemberUser :one
SELECT EXISTS(SELECT 1 FROM time_entries WHERE organization_id = $1 AND user_id = $2);

-- name: DeleteTimeEntriesForMemberUser :exec
DELETE FROM time_entries WHERE organization_id = $1 AND user_id = $2;

-- name: ProjectMembersExistForMemberUser :one
SELECT EXISTS(SELECT 1 FROM project_members pm
    JOIN projects p ON p.id = pm.project_id
    WHERE p.organization_id = $1 AND pm.user_id = $2);

-- name: DeleteProjectMembersForMemberUser :exec
DELETE FROM project_members pm USING projects p
WHERE p.id = pm.project_id AND p.organization_id = $1 AND pm.user_id = $2;

-- name: DeleteGoalsForMember :exec
DELETE FROM goals WHERE organization_id = $1 AND member_id = $2;

-- Organization deletion cascade --------------------------------------------

-- name: DeleteInvitationsForOrg :exec
DELETE FROM organization_invitations WHERE organization_id = $1;

-- name: DeleteTimeEntriesForOrg :exec
DELETE FROM time_entries WHERE organization_id = $1;

-- name: DeleteTagsForOrg :exec
DELETE FROM tags WHERE organization_id = $1;

-- name: DeleteTasksForOrg :exec
DELETE FROM tasks WHERE organization_id = $1;

-- name: DeleteProjectMembersForOrg :exec
DELETE FROM project_members pm USING projects p
WHERE p.id = pm.project_id AND p.organization_id = $1;

-- name: DeleteProjectsForOrg :exec
DELETE FROM projects WHERE organization_id = $1;

-- name: DeleteClientsForOrg :exec
DELETE FROM clients WHERE organization_id = $1;

-- name: DeleteReportsForOrg :exec
DELETE FROM reports WHERE organization_id = $1;

-- name: DeleteGoalsForOrg :exec
DELETE FROM goals WHERE organization_id = $1;

-- Billable-rate propagation (BillableRateService) ---------------------------

-- name: PropagateBillableRateForMember :exec
UPDATE time_entries te SET billable_rate = $3, updated_at = now()
WHERE te.billable = true AND te.organization_id = $1 AND te.member_id = $2
  AND NOT EXISTS (
    SELECT 1 FROM projects p
    WHERE p.id = te.project_id
      AND (p.billable_rate IS NOT NULL
        OR EXISTS (SELECT 1 FROM project_members pm
                   WHERE pm.project_id = p.id
                     AND pm.billable_rate IS NOT NULL
                     AND pm.member_id = te.member_id)));

-- name: PropagateBillableRateForOrganization :exec
UPDATE time_entries te SET billable_rate = $2, updated_at = now()
WHERE te.billable = true AND te.organization_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM members m
    WHERE m.id = te.member_id AND m.billable_rate IS NOT NULL)
  AND NOT EXISTS (
    SELECT 1 FROM projects p
    WHERE p.id = te.project_id
      AND (p.billable_rate IS NOT NULL
        OR EXISTS (SELECT 1 FROM project_members pm
                   WHERE pm.project_id = p.id
                     AND pm.billable_rate IS NOT NULL
                     AND pm.member_id = te.member_id)));

-- User deletion (DeletionService::deleteUser) -------------------------------

-- name: ListUserMemberships :many
SELECT id AS member_id, organization_id, role FROM members WHERE user_id = $1;

-- name: CountNonPlaceholderMembers :one
SELECT count(*) FROM members WHERE organization_id = $1 AND role <> 'placeholder';

-- name: DeleteRefreshTokensForUser :exec
DELETE FROM oauth_refresh_tokens
WHERE access_token_id IN (SELECT id FROM oauth_access_tokens WHERE user_id = $1);

-- name: DeleteAccessTokensForUser :exec
DELETE FROM oauth_access_tokens WHERE user_id = $1;

-- name: DeleteAuthCodesForUser :exec
DELETE FROM oauth_auth_codes WHERE user_id = $1;
