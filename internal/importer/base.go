package importer

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type (
	projectRow       = db.ImportListProjectsRow
	memberRow        = db.ImportListMembersRow
	projectMemberRow = db.ImportListProjectMembersRow
)

// base is the port of DefaultImporter: shared entity helpers plus counters.
// Billable rates are always usable in tock (no plan gates).
type base struct {
	q    *db.Queries
	org  db.Organization

	users          *helper[db.ImportListUsersRow]
	members        *helper[db.ImportListMembersRow]
	clients        *helper[db.ImportListClientsRow]
	projects       *helper[db.ImportListProjectsRow]
	projectMembers *helper[db.ImportListProjectMembersRow]
	tags           *helper[db.ImportListTagsRow]
	tasks          *helper[db.ImportListTasksRow]

	timeEntriesCreated int
}

func newBase(ctx context.Context, q *db.Queries, org db.Organization) *base {
	b := &base{q: q, org: org}
	orgID := org.ID

	b.users = newHelper(
		func(ctx context.Context) ([]db.ImportListUsersRow, error) { return q.ImportListUsers(ctx, orgID) },
		func(r db.ImportListUsersRow) []any { return []any{r.Email} },
		func(r db.ImportListUsersRow) pgtype.UUID { return r.ID },
	)
	b.members = newHelper(
		func(ctx context.Context) ([]db.ImportListMembersRow, error) { return q.ImportListMembers(ctx, orgID) },
		func(r db.ImportListMembersRow) []any { return []any{uuidVal(r.UserID), uuidVal(r.OrganizationID)} },
		func(r db.ImportListMembersRow) pgtype.UUID { return r.ID },
	)
	b.clients = newHelper(
		func(ctx context.Context) ([]db.ImportListClientsRow, error) { return q.ImportListClients(ctx, orgID) },
		func(r db.ImportListClientsRow) []any { return []any{r.Name, uuidVal(r.OrganizationID)} },
		func(r db.ImportListClientsRow) pgtype.UUID { return r.ID },
	)
	b.projects = newHelper(
		func(ctx context.Context) ([]db.ImportListProjectsRow, error) { return q.ImportListProjects(ctx, orgID) },
		func(r db.ImportListProjectsRow) []any {
			return []any{r.Name, uuidVal(r.ClientID), uuidVal(r.OrganizationID)}
		},
		func(r db.ImportListProjectsRow) pgtype.UUID { return r.ID },
	)
	b.projectMembers = newHelper(
		func(ctx context.Context) ([]db.ImportListProjectMembersRow, error) {
			return q.ImportListProjectMembers(ctx, orgID)
		},
		func(r db.ImportListProjectMembersRow) []any {
			return []any{uuidVal(r.ProjectID), uuidVal(r.MemberID)}
		},
		func(r db.ImportListProjectMembersRow) pgtype.UUID { return r.ID },
	)
	b.tags = newHelper(
		func(ctx context.Context) ([]db.ImportListTagsRow, error) { return q.ImportListTags(ctx, orgID) },
		func(r db.ImportListTagsRow) []any { return []any{r.Name, uuidVal(r.OrganizationID)} },
		func(r db.ImportListTagsRow) pgtype.UUID { return r.ID },
	)
	b.tasks = newHelper(
		func(ctx context.Context) ([]db.ImportListTasksRow, error) { return q.ImportListTasks(ctx, orgID) },
		func(r db.ImportListTasksRow) []any {
			return []any{r.Name, uuidVal(r.ProjectID), uuidVal(r.OrganizationID)}
		},
		func(r db.ImportListTasksRow) pgtype.UUID { return r.ID },
	)
	return b
}

// userKey: users identified by email; created as placeholders.
func (b *base) userKey(ctx context.Context, email, name, timezone, ext string) (pgtype.UUID, error) {
	return b.users.getKey(ctx, []any{email}, func(ctx context.Context) (db.ImportListUsersRow, error) {
		if err := requireNameMax(name, 255); err != nil {
			return db.ImportListUsersRow{}, err
		}
		if err := requireTimezone(timezone); err != nil {
			return db.ImportListUsersRow{}, err
		}
		id, err := b.q.ImportInsertUser(ctx, db.ImportInsertUserParams{
			ID:       newID(),
			Name:     name,
			Email:    email,
			Timezone: timezone,
		})
		return db.ImportListUsersRow{ID: id, Name: name, Email: email}, err
	}, ext)
}

// memberKey: members identified by (user_id, organization_id); created with
// role=placeholder and an optional billable rate.
func (b *base) memberKey(ctx context.Context, userID pgtype.UUID, billableRate *int32, ext string) (pgtype.UUID, error) {
	orgID := b.org.ID
	return b.members.getKey(ctx, []any{uuidVal(userID), uuidVal(orgID)}, func(ctx context.Context) (db.ImportListMembersRow, error) {
		row, err := b.q.ImportInsertMember(ctx, db.ImportInsertMemberParams{
			ID:             newID(),
			UserID:         userID,
			OrganizationID: orgID,
			Role:           strPtr("placeholder"),
			BillableRate:   billableRate,
		})
		return db.ImportListMembersRow(row), err
	}, ext)
}

// clientKey: clients identified by (name, organization_id).
func (b *base) clientKey(ctx context.Context, name string, archivedAt pgtype.Timestamp, ext string) (pgtype.UUID, error) {
	orgID := b.org.ID
	return b.clients.getKey(ctx, []any{name, uuidVal(orgID)}, func(ctx context.Context) (db.ImportListClientsRow, error) {
		if err := requireNameMax(name, 255); err != nil {
			return db.ImportListClientsRow{}, err
		}
		row, err := b.q.ImportInsertClient(ctx, db.ImportInsertClientParams{
			ID:             newID(),
			Name:           name,
			OrganizationID: orgID,
			ArchivedAt:     archivedAt,
		})
		return db.ImportListClientsRow(row), err
	}, ext)
}

type projectCreate struct {
	Color         string
	BillableRate  *int32
	IsPublic      bool
	IsBillable    bool
	ArchivedAt    pgtype.Timestamp
	EstimatedTime *int32
}

// projectKey: projects identified by (name, client_id, organization_id).
func (b *base) projectKey(ctx context.Context, name string, clientID pgtype.UUID, cv projectCreate, ext string) (pgtype.UUID, error) {
	orgID := b.org.ID
	return b.projects.getKey(ctx, []any{name, uuidVal(clientID), uuidVal(orgID)}, func(ctx context.Context) (db.ImportListProjectsRow, error) {
		if err := requireNameMax(name, 255); err != nil {
			return db.ImportListProjectsRow{}, err
		}
		// beforeSave: billable_rate === 0 becomes null.
		if cv.BillableRate != nil && *cv.BillableRate == 0 {
			cv.BillableRate = nil
		}
		row, err := b.q.InsertProject(ctx, db.InsertProjectParams{
			ID:             newID(),
			Name:           name,
			Color:          cv.Color,
			BillableRate:   cv.BillableRate,
			IsPublic:       cv.IsPublic,
			ClientID:       clientID,
			OrganizationID: orgID,
			IsBillable:     cv.IsBillable,
			ArchivedAt:     cv.ArchivedAt,
			EstimatedTime:  cv.EstimatedTime,
		})
		return db.ImportListProjectsRow{
			ID: row.ID, Name: row.Name, Color: row.Color, BillableRate: row.BillableRate,
			IsPublic: row.IsPublic, ClientID: row.ClientID, OrganizationID: row.OrganizationID,
			IsBillable: row.IsBillable, ArchivedAt: row.ArchivedAt, EstimatedTime: row.EstimatedTime,
		}, err
	}, ext)
}

// projectMemberKey: project members identified by (project_id, member_id).
func (b *base) projectMemberKey(ctx context.Context, projectID, memberID, userID pgtype.UUID, billableRate *int32, ext string) (pgtype.UUID, error) {
	return b.projectMembers.getKey(ctx, []any{uuidVal(projectID), uuidVal(memberID)}, func(ctx context.Context) (db.ImportListProjectMembersRow, error) {
		// beforeSave: billable_rate === 0 becomes null.
		if billableRate != nil && *billableRate == 0 {
			billableRate = nil
		}
		row, err := b.q.InsertProjectMember(ctx, db.InsertProjectMemberParams{
			ID:           newID(),
			BillableRate: billableRate,
			ProjectID:    projectID,
			UserID:       userID,
			MemberID:     memberID,
		})
		return db.ImportListProjectMembersRow{
			ID: row.ID, BillableRate: row.BillableRate, ProjectID: row.ProjectID,
			UserID: row.UserID, MemberID: row.MemberID,
		}, err
	}, ext)
}

// tagKey: tags identified by (name, organization_id).
func (b *base) tagKey(ctx context.Context, name, ext string) (pgtype.UUID, error) {
	orgID := b.org.ID
	return b.tags.getKey(ctx, []any{name, uuidVal(orgID)}, func(ctx context.Context) (db.ImportListTagsRow, error) {
		if err := requireNameMax(name, 255); err != nil {
			return db.ImportListTagsRow{}, err
		}
		row, err := b.q.InsertTag(ctx, db.InsertTagParams{ID: newID(), Name: name, OrganizationID: orgID})
		return db.ImportListTagsRow{ID: row.ID, Name: row.Name, OrganizationID: row.OrganizationID}, err
	}, ext)
}

// taskKey: tasks identified by (name, project_id, organization_id).
func (b *base) taskKey(ctx context.Context, name string, projectID pgtype.UUID, doneAt pgtype.Timestamp, ext string) (pgtype.UUID, error) {
	orgID := b.org.ID
	return b.tasks.getKey(ctx, []any{name, uuidVal(projectID), uuidVal(orgID)}, func(ctx context.Context) (db.ImportListTasksRow, error) {
		if err := requireNameMax(name, 500); err != nil {
			return db.ImportListTasksRow{}, err
		}
		row, err := b.q.ImportInsertTask(ctx, db.ImportInsertTaskParams{
			ID:             newID(),
			Name:           name,
			ProjectID:      projectID,
			OrganizationID: orgID,
			DoneAt:         doneAt,
		})
		return db.ImportListTasksRow(row), err
	}, ext)
}

// tagIDsFromList resolves a delimiter-separated tag name list to tag ids.
// trim mirrors Str::trim on each part (generic importer trims, Toggl and
// Clockify split on ", " and do not).
func (b *base) tagIDsFromList(ctx context.Context, raw, sep string, trim bool) ([]pgtype.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []pgtype.UUID
	for _, part := range strings.Split(raw, sep) {
		if trim {
			part = strings.TrimSpace(part)
		}
		id, err := b.tagKey(ctx, part, "")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func marshalUUIDs(ids []pgtype.UUID) ([]byte, error) {
	strs := make([]string, 0, len(ids))
	for _, id := range ids {
		strs = append(strs, uuidStr(id))
	}
	return json.Marshal(strs)
}

// timeEntryCreate bundles one imported time entry insert.
type timeEntryCreate struct {
	UserID, MemberID, TaskID, ProjectID, ClientID pgtype.UUID
	Description                                    string
	Billable                                       bool
	BillableRate                                   *int32
	Tags                                           []pgtype.UUID
	Start, End                                     time.Time
	StillActiveEmailSentAt                         pgtype.Timestamp
	Type                                           string
}

func (b *base) insertTimeEntry(ctx context.Context, e timeEntryCreate) error {
	tagJSON := []byte("[]")
	if len(e.Tags) > 0 {
		var err error
		tagJSON, err = marshalUUIDs(e.Tags)
		if err != nil {
			return err
		}
	}
	typ := e.Type
	if typ == "" {
		typ = "work"
	}
	_, err := b.q.ImportInsertTimeEntry(ctx, db.ImportInsertTimeEntryParams{
		ID:                     newID(),
		Description:            e.Description,
		Start:                  ts(e.Start),
		End:                    ts(e.End),
		BillableRate:           e.BillableRate,
		Billable:               e.Billable,
		UserID:                 e.UserID,
		OrganizationID:         b.org.ID,
		ProjectID:              e.ProjectID,
		TaskID:                 e.TaskID,
		Tags:                   tagJSON,
		MemberID:               e.MemberID,
		ClientID:               e.ClientID,
		StillActiveEmailSentAt: e.StillActiveEmailSentAt,
		Type:                   typ,
	})
	if err != nil {
		return err
	}
	b.timeEntriesCreated++
	return nil
}

// billableRate ports BillableRateService::getBillableRateForTimeEntryWithGivenRelations.
func (b *base) billableRate(billable bool, projectMember *projectMemberRow, project *projectRow, member *memberRow) *int32 {
	if !billable {
		return nil
	}
	if projectMember != nil && projectMember.BillableRate != nil {
		return projectMember.BillableRate
	}
	if project != nil && project.BillableRate != nil {
		return project.BillableRate
	}
	if member != nil && member.BillableRate != nil {
		return member.BillableRate
	}
	return b.org.BillableRate
}

// projectMemberFor mirrors projectMemberImportHelper->getModel().
func (b *base) projectMemberFor(ctx context.Context, projectID, memberID pgtype.UUID) (*projectMemberRow, error) {
	row, ok, err := b.projectMembers.model(ctx, []any{uuidVal(projectID), uuidVal(memberID)})
	if err != nil || !ok {
		return nil, err
	}
	return &row, nil
}

// recalcSpent dispatches the spent_time recompute for every project/task that
// was resolved via modelByID during the import (upstream's queued jobs run
// synchronously here, inside the import transaction).
func (b *base) recalcSpent(ctx context.Context) error {
	for _, p := range b.projects.usedModels() {
		if err := b.q.RecalculateSpentTimeForProject(ctx, p.ID); err != nil {
			return err
		}
	}
	for _, t := range b.tasks.usedModels() {
		if err := b.q.RecalculateSpentTimeForTask(ctx, t.ID); err != nil {
			return err
		}
	}
	return nil
}

func strPtr(s string) *string { return &s }

// memberModel mirrors memberImportHelper->getModelById().
func (b *base) memberModel(id pgtype.UUID) (*memberRow, error) {
	row, ok := b.members.modelByID(id)
	if !ok {
		return nil, fail("Member does not exist")
	}
	return &row, nil
}

// projectModel mirrors projectImportHelper->getModelById().
func (b *base) projectModel(id pgtype.UUID) (*projectRow, error) {
	row, ok := b.projects.modelByID(id)
	if !ok {
		return nil, fail("Project does not exist")
	}
	return &row, nil
}
