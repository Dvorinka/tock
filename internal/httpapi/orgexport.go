package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// ---- ExportController + ExportService port ----
//
// A whole-organization export is a ZIP with one CSV per table plus meta.json.
// Column sets, value formatting and the file names follow upstream exactly so
// the solidtime importer can read tock exports.

const organizationExportVersion = "1.0"

type organizationExportMeta struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	Organizations []string `json:"organizations"`
	ExportedAt    string   `json:"exported_at"`
}

// handleExportOrganization: POST /api/v1/organizations/{organization}/export
func (s *Server) handleExportOrganization(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "export")
	if !ok {
		return
	}
	content, err := s.buildOrganizationExport(r.Context(), scope.org)
	if err != nil {
		slog.ErrorContext(r.Context(), "organization export failed",
			"organization_id", domain.UUIDString(scope.org.ID), "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	now := time.Now().UTC()
	filename := fmt.Sprintf("export_%s_%s_%s.zip",
		domain.UUIDString(scope.org.ID), now.Format("2006-01-02_15-04-05"), newUUID())
	url, err := s.saveExportFileTTL(filename, content, 10*time.Minute)
	if err != nil {
		slog.ErrorContext(r.Context(), "organization export store failed", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "download_url": url})
}

func (s *Server) buildOrganizationExport(ctx context.Context, org db.Organization) ([]byte, error) {
	type file struct {
		name    string
		content []byte
	}
	var files []file
	add := func(name string, content []byte, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files = append(files, file{name: name, content: content})
		return nil
	}

	organizations, err := csvBytes(
		[]string{"id", "name", "billable_rate", "currency", "created_at", "updated_at"},
		[][]string{{
			domain.UUIDString(org.ID),
			org.Name,
			int32OrEmpty(org.BillableRate),
			org.Currency,
			zuluOrEmpty(org.CreatedAt),
			zuluOrEmpty(org.UpdatedAt),
		}},
	)
	if err := add("organizations.csv", organizations, err); err != nil {
		return nil, err
	}

	invitations, err := s.exportOrganizationInvitations(ctx, org.ID)
	if err := add("organization_invitations.csv", invitations, err); err != nil {
		return nil, err
	}
	timeEntries, err := s.exportTimeEntries(ctx, org.ID)
	if err := add("time_entries.csv", timeEntries, err); err != nil {
		return nil, err
	}
	clients, err := s.exportClients(ctx, org.ID)
	if err := add("clients.csv", clients, err); err != nil {
		return nil, err
	}
	projects, err := s.exportProjects(ctx, org.ID)
	if err := add("projects.csv", projects, err); err != nil {
		return nil, err
	}
	projectMembers, err := s.exportProjectMembers(ctx, org.ID)
	if err := add("project_members.csv", projectMembers, err); err != nil {
		return nil, err
	}
	members, err := s.exportMembers(ctx, org.ID)
	if err := add("members.csv", members, err); err != nil {
		return nil, err
	}
	tasks, err := s.exportTasks(ctx, org.ID)
	if err := add("tasks.csv", tasks, err); err != nil {
		return nil, err
	}
	tags, err := s.exportTags(ctx, org.ID)
	if err := add("tags.csv", tags, err); err != nil {
		return nil, err
	}

	meta, err := json.Marshal(organizationExportMeta{
		ID:            newUUID(),
		Version:       organizationExportVersion,
		Organizations: []string{domain.UUIDString(org.ID)},
		ExportedAt:    time.Now().UTC().Format(zuluLayout),
	})
	if err := add("meta.json", meta, err); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, fmt.Errorf("zip entry %s: %w", f.name, err)
		}
		if _, err := w.Write(f.content); err != nil {
			return nil, fmt.Errorf("zip write %s: %w", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("zip close: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *Server) exportOrganizationInvitations(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, organization_id, role, created_at, updated_at
		FROM organization_invitations WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, invOrgID pgtype.UUID
		var email string
		var role *string
		var created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &email, &invOrgID, &role, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), email, domain.UUIDString(invOrgID), textOrEmpty(role),
			zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes(
		[]string{"id", "email", "organization_id", "role", "created_at", "updated_at"}, out)
}

func (s *Server) exportTimeEntries(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, description, start, "end", billable_rate, billable, type, member_id, user_id,
		       organization_id, client_id, project_id, task_id, tags, is_imported,
		       still_active_email_sent_at, created_at, updated_at
		FROM time_entries WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, memberID, userID, entryOrgID, clientID, projectID, taskID pgtype.UUID
		var description, entryType string
		var start pgtype.Timestamp
		var end, stillActiveSentAt, created, updated pgtype.Timestamp
		var billableRate *int32
		var billable, isImported bool
		var tags []byte
		if err := rows.Scan(&id, &description, &start, &end, &billableRate, &billable, &entryType,
			&memberID, &userID, &entryOrgID, &clientID, &projectID, &taskID, &tags, &isImported,
			&stillActiveSentAt, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id),
			description,
			zuluOrEmpty(start),
			zuluOrEmpty(end),
			int32OrEmpty(billableRate),
			boolText(billable),
			entryType,
			domain.UUIDString(memberID),
			domain.UUIDString(userID),
			domain.UUIDString(entryOrgID),
			uuidOrEmpty(clientID),
			uuidOrEmpty(projectID),
			uuidOrEmpty(taskID),
			string(tags),
			boolText(isImported),
			zuluOrEmpty(stillActiveSentAt),
			zuluOrEmpty(created),
			zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes([]string{
		"id", "description", "start", "end", "billable_rate", "billable", "type", "member_id",
		"user_id", "organization_id", "client_id", "project_id", "task_id", "tags", "is_imported",
		"still_active_email_sent_at", "created_at", "updated_at",
	}, out)
}

func (s *Server) exportClients(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, organization_id, archived_at, created_at, updated_at
		FROM clients WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, clientOrgID pgtype.UUID
		var name string
		var archivedAt, created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &name, &clientOrgID, &archivedAt, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), name, domain.UUIDString(clientOrgID),
			zuluOrEmpty(archivedAt), zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes(
		[]string{"id", "name", "organization_id", "archived_at", "created_at", "updated_at"}, out)
}

func (s *Server) exportProjects(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, color, billable_rate, is_public, client_id, organization_id, is_billable,
		       archived_at, created_at, updated_at
		FROM projects WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, clientID, projectOrgID pgtype.UUID
		var name, color string
		var billableRate *int32
		var isPublic, isBillable bool
		var archivedAt, created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &name, &color, &billableRate, &isPublic, &clientID, &projectOrgID,
			&isBillable, &archivedAt, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), name, color, int32OrEmpty(billableRate), boolText(isPublic),
			uuidOrEmpty(clientID), domain.UUIDString(projectOrgID), boolText(isBillable),
			zuluOrEmpty(archivedAt), zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes([]string{
		"id", "name", "color", "billable_rate", "is_public", "client_id", "organization_id",
		"is_billable", "archived_at", "created_at", "updated_at",
	}, out)
}

func (s *Server) exportProjectMembers(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pm.id, pm.billable_rate, pm.project_id, pm.user_id, pm.member_id, pm.created_at, pm.updated_at
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE p.organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, projectID, userID, memberID pgtype.UUID
		var billableRate *int32
		var created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &billableRate, &projectID, &userID, &memberID, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), int32OrEmpty(billableRate), domain.UUIDString(projectID),
			domain.UUIDString(userID), domain.UUIDString(memberID),
			zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes([]string{
		"id", "billable_rate", "project_id", "user_id", "member_id", "created_at", "updated_at",
	}, out)
}

func (s *Server) exportMembers(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.user_id, u.name, u.email, m.organization_id, m.billable_rate, m.role,
		       m.created_at, m.updated_at
		FROM members m JOIN users u ON u.id = m.user_id
		WHERE m.organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, userID, memberOrgID pgtype.UUID
		var name, email string
		var billableRate *int32
		var role *string
		var created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &userID, &name, &email, &memberOrgID, &billableRate, &role,
			&created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), domain.UUIDString(userID), name, email,
			domain.UUIDString(memberOrgID), int32OrEmpty(billableRate), textOrEmpty(role),
			zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes([]string{
		"id", "user_id", "name", "email", "organization_id", "billable_rate", "role",
		"created_at", "updated_at",
	}, out)
}

func (s *Server) exportTasks(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, project_id, organization_id, done_at, created_at, updated_at
		FROM tasks WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, projectID, taskOrgID pgtype.UUID
		var name string
		var doneAt, created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &name, &projectID, &taskOrgID, &doneAt, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), name, domain.UUIDString(projectID), domain.UUIDString(taskOrgID),
			zuluOrEmpty(doneAt), zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes([]string{
		"id", "name", "project_id", "organization_id", "done_at", "created_at", "updated_at",
	}, out)
}

func (s *Server) exportTags(ctx context.Context, orgID pgtype.UUID) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, organization_id, created_at, updated_at
		FROM tags WHERE organization_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, tagOrgID pgtype.UUID
		var name string
		var created, updated pgtype.Timestamp
		if err := rows.Scan(&id, &name, &tagOrgID, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, []string{
			domain.UUIDString(id), name, domain.UUIDString(tagOrgID),
			zuluOrEmpty(created), zuluOrEmpty(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return csvBytes(
		[]string{"id", "name", "organization_id", "created_at", "updated_at"}, out)
}

// ---- CSV value formatting ----

func csvBytes(header []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func zuluOrEmpty(t pgtype.Timestamp) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(zuluLayout)
}

func textOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int32OrEmpty(n *int32) string {
	if n == nil {
		return ""
	}
	return fmt.Sprintf("%d", *n)
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func uuidOrEmpty(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return domain.UUIDString(u)
}
