package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

var solidtimeSupportedVersions = map[string]bool{"1.0": true}

func csvFromZip(files map[string][]byte, name string) ([]map[string]string, error) {
	raw, ok := files[name]
	if !ok {
		return nil, fail("File %q missing in ZIP", name)
	}
	_, records, err := readCSV(raw)
	if err != nil {
		return nil, fail("Invalid CSV data")
	}
	return records, nil
}

func parseCSVTime(v string) (pgtype.Timestamp, error) {
	if v == "" {
		return pgtype.Timestamp{}, nil
	}
	t, err := parseISO(v)
	if err != nil {
		return pgtype.Timestamp{}, err
	}
	return ts(t), nil
}

// importSolidtime ports SolidtimeImporter (ZIP of CSVs + meta.json).
func (b *base) importSolidtime(ctx context.Context, data []byte) error {
	files, err := extractZip(data)
	if err != nil {
		return err
	}
	metaRaw, ok := files["meta.json"]
	if !ok {
		return fail("File \"meta.json\" missing in ZIP")
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil || !solidtimeSupportedVersions[meta.Version] {
		return fail("Invalid version")
	}
	clients, err := csvFromZip(files, "clients.csv")
	if err != nil {
		return err
	}
	members, err := csvFromZip(files, "members.csv")
	if err != nil {
		return err
	}
	// Upstream validates the file exists but never imports rows from it.
	if _, ok := files["organization_invitations.csv"]; !ok {
		return fail("File \"organization_invitations.csv\" missing in ZIP")
	}
	projectMembers, err := csvFromZip(files, "project_members.csv")
	if err != nil {
		return err
	}
	projects, err := csvFromZip(files, "projects.csv")
	if err != nil {
		return err
	}
	tags, err := csvFromZip(files, "tags.csv")
	if err != nil {
		return err
	}
	tasks, err := csvFromZip(files, "tasks.csv")
	if err != nil {
		return err
	}
	timeEntries, err := csvFromZip(files, "time_entries.csv")
	if err != nil {
		return err
	}

	for _, c := range clients {
		archived, err := parseCSVTime(c["archived_at"])
		if err != nil {
			return fmt.Errorf("archived_at: %w", err)
		}
		if _, err := b.clientKey(ctx, c["name"], archived, c["id"]); err != nil {
			return err
		}
	}
	for _, t := range tags {
		if _, err := b.tagKey(ctx, t["name"], t["id"]); err != nil {
			return err
		}
	}
	for _, m := range members {
		userID, err := b.userKey(ctx, m["email"], m["name"], "UTC", m["user_id"])
		if err != nil {
			return err
		}
		var rate *int32
		if m["billable_rate"] != "" {
			if n, err := strconv.Atoi(m["billable_rate"]); err == nil {
				r := int32(n)
				rate = &r
			}
		}
		if _, err := b.memberKey(ctx, userID, rate, m["id"]); err != nil {
			return err
		}
	}
	for _, p := range projects {
		var clientID pgtype.UUID
		if p["client_id"] != "" {
			var ok bool
			clientID, ok = b.clients.keyByExternal(p["client_id"])
			if !ok {
				return errMissing("Client does not exist")
			}
		}
		if !validColor.MatchString(p["color"]) {
			return fail("Invalid color")
		}
		var rate *int32
		if p["billable_rate"] != "" {
			if n, err := strconv.Atoi(p["billable_rate"]); err == nil {
				r := int32(n)
				rate = &r
			}
		}
		archived, err := parseCSVTime(p["archived_at"])
		if err != nil {
			return fmt.Errorf("archived_at: %w", err)
		}
		if _, err := b.projectKey(ctx, p["name"], clientID, projectCreate{
			Color:        p["color"],
			BillableRate: rate,
			IsPublic:     p["is_public"] == "true",
			IsBillable:   p["is_billable"] == "true",
			ArchivedAt:   archived,
		}, p["id"]); err != nil {
			return err
		}
	}
	for _, pm := range projectMembers {
		userID, _ := b.users.keyByExternal(pm["user_id"])
		memberID, _ := b.members.keyByExternal(pm["member_id"])
		projectID, _ := b.projects.keyByExternal(pm["project_id"])
		var rate *int32
		if pm["billable_rate"] != "" {
			if n, err := strconv.Atoi(pm["billable_rate"]); err == nil {
				r := int32(n)
				rate = &r
			}
		}
		if _, err := b.projectMemberKey(ctx, projectID, memberID, userID, rate, pm["id"]); err != nil {
			return err
		}
	}
	for _, t := range tasks {
		projectID, ok := b.projects.keyByExternal(t["project_id"])
		if !ok {
			return errMissing("Project does not exist")
		}
		if err := checkTaskNameLength(t["name"]); err != nil {
			return err
		}
		doneAt, err := parseCSVTime(t["done_at"])
		if err != nil {
			return fmt.Errorf("done_at: %w", err)
		}
		if _, err := b.taskKey(ctx, t["name"], projectID, doneAt, t["id"]); err != nil {
			return err
		}
	}

	for _, row := range timeEntries {
		// Running time entries are skipped: the member could already have one.
		if row["end"] == "" {
			continue
		}
		userID, ok := b.users.keyByExternal(row["user_id"])
		if !ok {
			return fail("Time entry references a user that does not exist in the import")
		}
		memberID, ok := b.members.keyByExternal(row["member_id"])
		if !ok {
			return fail("Time entry references a member that does not exist in the import")
		}
		member, err := b.memberModel(memberID)
		if err != nil {
			return err
		}
		var clientID pgtype.UUID
		if row["client_id"] != "" {
			clientID, ok = b.clients.keyByExternal(row["client_id"])
			if !ok {
				return fail("Time entry references a client that does not exist in the import")
			}
		}
		var projectID pgtype.UUID
		var project *projectRow
		var projectMember *projectMemberRow
		if row["project_id"] != "" {
			projectID, ok = b.projects.keyByExternal(row["project_id"])
			if !ok {
				return fail("Time entry references a project that does not exist in the import")
			}
			project, err = b.projectModel(projectID)
			if err != nil {
				return err
			}
			projectMember, err = b.projectMemberFor(ctx, projectID, memberID)
			if err != nil {
				return err
			}
		}
		var taskID pgtype.UUID
		if row["task_id"] != "" {
			taskID, ok = b.tasks.keyByExternal(row["task_id"])
			if !ok {
				return fail("Time entry references a task that does not exist in the import")
			}
			b.tasks.modelByID(taskID)
		}
		if len(row["description"]) > 5000 {
			return fail("Time entry description is too long")
		}
		if row["billable"] != "true" && row["billable"] != "false" {
			return fail("Invalid billable value")
		}
		billable := row["billable"] == "true"
		entryType := "work"
		if v := row["type"]; v != "" {
			if v != "work" && v != "break" {
				return fail("Invalid type value")
			}
			entryType = v
		}
		tagIDs, err := b.solidtimeTagIDs(row["tags"])
		if err != nil {
			return err
		}
		start, err := parseISO(row["start"])
		if err != nil {
			return fail("Start date (%q) is invalid", row["start"])
		}
		end, err := parseISO(row["end"])
		if err != nil {
			return fail("End date (%q) is invalid", row["end"])
		}
		if end.Before(start) {
			return fail("End date (%q) is before start date (%q)", row["end"], row["start"])
		}
		var stillActive pgtype.Timestamp
		if v := row["still_active_email_sent_at"]; v != "" {
			t, err := parseISO(v)
			if err != nil {
				return fail("Still active email timestamp (%q) is invalid", v)
			}
			stillActive = ts(t)
		}
		if err := b.insertTimeEntry(ctx, timeEntryCreate{
			UserID: userID, MemberID: memberID, TaskID: taskID,
			ProjectID: projectID, ClientID: clientID,
			Description:            row["description"],
			Billable:               billable,
			BillableRate:           b.billableRate(billable, projectMember, project, member),
			Tags:                   tagIDs,
			Type:                   entryType,
			Start:                  start,
			End:                    end,
			StillActiveEmailSentAt: stillActive,
		}); err != nil {
			return err
		}
	}
	return b.recalcSpent(ctx)
}

// solidtimeTagIDs resolves the exported JSON array of external tag ids to
// internal ids; non-uuid entries are skipped like upstream.
func (b *base) solidtimeTagIDs(raw string) ([]pgtype.UUID, error) {
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, nil
	}
	var ids []pgtype.UUID
	for _, ext := range parsed {
		if parseUUID(ext).Valid {
			id, _ := b.tags.keyByExternal(ext)
			ids = append(ids, id)
		}
	}
	return ids, nil
}
