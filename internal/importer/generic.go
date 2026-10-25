package importer

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

// importGenericTimeEntries ports GenericTimeEntriesImporter.
func (b *base) importGenericTimeEntries(ctx context.Context, data []byte) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header,
		"description", "billable", "client", "project", "tags",
		"start", "end", "task", "user_name", "user_email"); err != nil {
		return err
	}
	for _, rec := range records {
		userID, err := b.userKey(ctx, rec["user_email"], rec["user_name"], "UTC", "")
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
		if rec["client"] != "" {
			clientID, err = b.clientKey(ctx, rec["client"], pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		var projectID pgtype.UUID
		var project *projectRow
		var projectMember *projectMemberRow
		if rec["project"] != "" {
			projectID, err = b.projectKey(ctx, rec["project"], clientID, projectCreate{
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
		if rec["task"] != "" {
			if err := checkTaskNameLength(rec["task"]); err != nil {
				return err
			}
			taskID, err = b.taskKey(ctx, rec["task"], projectID, pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
			b.tasks.modelByID(taskID)
		}
		if rec["billable"] != "true" && rec["billable"] != "false" {
			return fail("Invalid billable value")
		}
		billable := rec["billable"] == "true"
		tagIDs, err := b.tagIDsFromList(ctx, rec["tags"], ",", true)
		if err != nil {
			return err
		}
		start, err := parseISO(rec["start"])
		if err != nil {
			return fail("Value of start (%q) is invalid", rec["start"])
		}
		end, err := parseISO(rec["end"])
		if err != nil {
			return fail("Value of end (%q) is invalid", rec["end"])
		}
		if end.Before(start) {
			return fail("Value of end (%q) is before start (%q)", rec["end"], rec["start"])
		}
		if err := b.insertTimeEntry(ctx, timeEntryCreate{
			UserID: userID, MemberID: memberID, TaskID: taskID,
			ProjectID: projectID, ClientID: clientID,
			Description: rec["description"],
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

// importGenericProjects ports GenericProjectsImporter.
func (b *base) importGenericProjects(ctx context.Context, data []byte) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header, "name"); err != nil {
		return err
	}
	for _, rec := range records {
		var clientID pgtype.UUID
		if v, ok := rec["client"]; ok && v != "" {
			clientID, err = b.clientKey(ctx, v, pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		if rec["name"] == "" {
			continue
		}
		var archivedAt pgtype.Timestamp
		if v := rec["archived_at"]; v != "" {
			t, err := parseISO(v)
			if err != nil {
				return fail("Value of archived_at (%q) is invalid", v)
			}
			archivedAt = ts(t)
		}
		color := rec["color"]
		if color == "" {
			color = randomColor()
		}
		var rate *int32
		if v := rec["billable_rate"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				r := int32(n)
				rate = &r
			}
		}
		var estimated *int32
		if v := rec["estimated_time"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n != 0 {
				e := int32(n)
				estimated = &e
			}
		}
		if _, err := b.projectKey(ctx, rec["name"], clientID, projectCreate{
			Color:         color,
			BillableRate:  rate,
			IsPublic:      rec["is_public"] == "true",
			IsBillable:    rec["billable_default"] == "true",
			ArchivedAt:    archivedAt,
			EstimatedTime: estimated,
		}, ""); err != nil {
			return err
		}
	}
	return nil
}
