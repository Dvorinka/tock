package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// importTogglTimeEntries ports TogglTimeEntriesImporter.
func (b *base) importTogglTimeEntries(ctx context.Context, data []byte, timezone string) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header,
		"User", "Email", "Client", "Project", "Task", "Description",
		"Billable", "Start date", "Start time", "End date", "End time", "Tags"); err != nil {
		return err
	}
	for _, rec := range records {
		userID, err := b.userKey(ctx, rec["Email"], rec["User"], "UTC", "")
		if err != nil {
			return err
		}
		memberID, err := b.memberKey(ctx, userID, nil, "")
		if err != nil {
			return err
		}
		member, err := b.memberModel(memberID)
		if err != nil {
			return err
		}
		var clientID pgtype.UUID
		if rec["Client"] != "" {
			clientID, err = b.clientKey(ctx, rec["Client"], pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		var projectID pgtype.UUID
		var project *projectRow
		var projectMember *projectMemberRow
		if rec["Project"] != "" {
			projectID, err = b.projectKey(ctx, rec["Project"], clientID, projectCreate{
				IsBillable: false,
				Color:      randomColor(),
			}, "")
			if err != nil {
				return err
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
		if rec["Task"] != "" {
			if err := checkTaskNameLength(rec["Task"]); err != nil {
				return err
			}
			taskID, err = b.taskKey(ctx, rec["Task"], projectID, pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
			b.tasks.modelByID(taskID)
		}
		if rec["Billable"] != "Yes" && rec["Billable"] != "No" {
			return fail("Invalid billable value")
		}
		billable := rec["Billable"] == "Yes"
		tagIDs, err := b.tagIDsFromList(ctx, rec["Tags"], ", ", false)
		if err != nil {
			return err
		}
		start, err := parseLocal("2006-01-02 15:04:05", rec["Start date"]+" "+rec["Start time"], timezone)
		if err != nil {
			return fail("Start date (%q) or time (%q) are invalid", rec["Start date"], rec["Start time"])
		}
		end, err := parseLocal("2006-01-02 15:04:05", rec["End date"]+" "+rec["End time"], timezone)
		if err != nil {
			return fail("End date (%q) or time (%q) are invalid", rec["End date"], rec["End time"])
		}
		if end.Before(start) {
			return fail("End (%q) is before start (%q)",
				rec["End date"]+" "+rec["End time"], rec["Start date"]+" "+rec["Start time"])
		}
		if err := b.insertTimeEntry(ctx, timeEntryCreate{
			UserID: userID, MemberID: memberID, TaskID: taskID,
			ProjectID: projectID, ClientID: clientID,
			Description: rec["Description"],
			Billable:    billable,
			BillableRate: b.billableRate(billable, projectMember, project, member),
			Tags:        tagIDs,
			Start:       start,
			End:         end,
		}); err != nil {
			return err
		}
	}
	return b.recalcSpent(ctx)
}

// ---- Toggl Data Importer (ZIP of JSON files) ----

var extIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// guardExternalIdentifier ports the CWE-22 fix: external ids become path
// segments, so they are restricted to a conservative allow-list.
func guardExternalIdentifier(id any) (string, error) {
	var s string
	switch v := id.(type) {
	case string:
		s = v
	case float64:
		s = fmt.Sprintf("%v", v)
		if strings.HasSuffix(s, ".0") && !strings.ContainsAny(s, "eE") {
			s = s[:len(s)-2]
		}
	default:
		return "", fail("Invalid identifier in import data")
	}
	if !extIDPattern.MatchString(s) {
		return "", fail("Invalid identifier in import data")
	}
	return s, nil
}

type togglClient struct {
	ID       any    `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

type togglTag struct {
	ID   any    `json:"id"`
	Name string `json:"name"`
}

type togglWorkspaceUser struct {
	UID      any    `json:"uid"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

type togglProject struct {
	ID        any      `json:"id"`
	Name      string   `json:"name"`
	ClientID  any      `json:"client_id"`
	Color     string   `json:"color"`
	Billable  bool     `json:"billable"`
	IsPrivate bool     `json:"is_private"`
	Rate      *float64 `json:"rate"`
}

type togglProjectMember struct {
	UserID any      `json:"user_id"`
	Rate   *float64 `json:"rate"`
}

type togglTask struct {
	ID     any    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// importTogglData ports TogglDataImporter.
func (b *base) importTogglData(ctx context.Context, data []byte, _ string) error {
	files, err := extractZip(data)
	if err != nil {
		return err
	}

	clientsRaw, ok := files["clients.json"]
	if !ok {
		return fail("File \"clients.json\" missing in ZIP")
	}
	var clients []togglClient
	if err := json.Unmarshal(clientsRaw, &clients); err != nil {
		return fail("File \"clients.json\" is empty")
	}
	projectsRaw, ok := files["projects.json"]
	if !ok {
		return fail("File \"projects.json\" missing in ZIP")
	}
	var projects []togglProject
	if err := json.Unmarshal(projectsRaw, &projects); err != nil {
		return fail("File \"projects.json\" is empty")
	}
	tagsRaw, ok := files["tags.json"]
	if !ok {
		return fail("File \"tags.json\" missing in ZIP")
	}
	var tags []togglTag
	if err := json.Unmarshal(tagsRaw, &tags); err != nil {
		return fail("File \"tags.json\" is empty")
	}
	usersRaw, ok := files["workspace_users.json"]
	if !ok {
		return fail("File \"workspace_users.json\" missing in ZIP")
	}
	var workspaceUsers []togglWorkspaceUser
	if err := json.Unmarshal(usersRaw, &workspaceUsers); err != nil {
		return fail("File \"workspace_users.json\" is empty")
	}

	for _, c := range clients {
		ext, err := guardExternalIdentifier(c.ID)
		if err != nil {
			return err
		}
		var archived pgtype.Timestamp
		if c.Archived {
			archived = ts(time.Now())
		}
		if _, err := b.clientKey(ctx, c.Name, archived, ext); err != nil {
			return err
		}
	}
	for _, t := range tags {
		ext, err := guardExternalIdentifier(t.ID)
		if err != nil {
			return err
		}
		if _, err := b.tagKey(ctx, t.Name, ext); err != nil {
			return err
		}
	}
	for _, wu := range workspaceUsers {
		tz := strings.TrimSpace(wu.Timezone)
		if tz == "" {
			tz = "UTC"
		}
		if _, err := time.LoadLocation(tz); err != nil {
			tz = "UTC"
		}
		ext, err := guardExternalIdentifier(wu.UID)
		if err != nil {
			return err
		}
		userID, err := b.userKey(ctx, wu.Email, wu.Name, tz, ext)
		if err != nil {
			return err
		}
		if _, err := b.memberKey(ctx, userID, nil, uuidStr(userID)); err != nil {
			return err
		}
	}

	for _, p := range projects {
		projectExtID, err := guardExternalIdentifier(p.ID)
		if err != nil {
			return err
		}
		var clientID pgtype.UUID
		if p.ClientID != nil {
			ext, err := guardExternalIdentifier(p.ClientID)
			if err != nil {
				return err
			}
			var ok bool
			clientID, ok = b.clients.keyByExternal(ext)
			if !ok {
				return errMissing("Client does not exist")
			}
		}
		if !validColor.MatchString(p.Color) {
			return fail("Invalid color")
		}
		var rate *int32
		if p.Rate != nil {
			r := int32(*p.Rate * 100)
			rate = &r
		}
		projectID, err := b.projectKey(ctx, p.Name, clientID, projectCreate{
			Color:        p.Color,
			IsBillable:   p.Billable,
			IsPublic:     !p.IsPrivate,
			BillableRate: rate,
		}, projectExtID)
		if err != nil {
			return err
		}

		pmPath := "projects_users/" + projectExtID + ".json"
		pmRaw, ok := files[pmPath]
		if !ok {
			return fail("File %q missing in ZIP", pmPath)
		}
		var projectMembers []togglProjectMember
		if err := json.Unmarshal(pmRaw, &projectMembers); err != nil {
			return fail("File %q is empty", pmPath)
		}
		for _, pm := range projectMembers {
			ext, err := guardExternalIdentifier(pm.UserID)
			if err != nil {
				return err
			}
			userID, ok := b.users.keyByExternal(ext)
			if !ok {
				return errMissing("User does not exist")
			}
			memberID, ok := b.members.keyByExternal(uuidStr(userID))
			if !ok {
				return errMissing("Member does not exist")
			}
			var pmRate *int32
			if pm.Rate != nil {
				r := int32(*pm.Rate * 100)
				pmRate = &r
			}
			if _, err := b.projectMemberKey(ctx, projectID, memberID, userID, pmRate, ""); err != nil {
				return err
			}
		}
	}

	for _, projectExtID := range b.projects.externalIDs() {
		guarded, err := guardExternalIdentifier(projectExtID)
		if err != nil {
			return err
		}
		taskPath := "tasks/" + guarded + ".json"
		taskRaw, ok := files[taskPath]
		if !ok {
			continue
		}
		var tasks []togglTask
		if err := json.Unmarshal(taskRaw, &tasks); err != nil {
			return fail("File %q is empty", taskPath)
		}
		for _, t := range tasks {
			projectID, ok := b.projects.keyByExternal(guarded)
			if !ok {
				return errMissing("Project does not exist")
			}
			if err := checkTaskNameLength(t.Name); err != nil {
				return err
			}
			var doneAt pgtype.Timestamp
			if !t.Active {
				doneAt = ts(time.Now())
			}
			ext, err := guardExternalIdentifier(t.ID)
			if err != nil {
				return err
			}
			if _, err := b.taskKey(ctx, t.Name, projectID, doneAt, ext); err != nil {
				return err
			}
		}
	}
	return nil
}
