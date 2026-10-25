package importer

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// importHarvestTimeEntries ports HarvestTimeEntriesImporter.
func (b *base) importHarvestTimeEntries(ctx context.Context, data []byte, timezone string) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header,
		"Date", "Hours", "Client", "Project", "Task", "Billable?",
		"First Name", "Last Name", "Notes"); err != nil {
		return err
	}
	for _, rec := range records {
		first, last := rec["First Name"], rec["Last Name"]
		email := slug(first) + "." + slug(last) + "@solidtime-import.test"
		userID, err := b.userKey(ctx, email, first+" "+last, "UTC", "")
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
				Color:      randomColor(),
				IsBillable: true,
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
		if len(rec["Notes"]) > 5000 {
			return fail("Time entry note is too long")
		}
		if rec["Billable?"] != "Yes" && rec["Billable?"] != "No" {
			return fail("Invalid billable value")
		}
		billable := rec["Billable?"] == "Yes"
		date, err := parseLocal("2006-01-02", rec["Date"], timezone)
		if err != nil {
			return fail("Date (%q) is invalid", rec["Date"])
		}
		hoursField := strings.ReplaceAll(rec["Hours"], ",", ".")
		hours, err := strconv.ParseFloat(hoursField, 64)
		if err != nil {
			return fail("Hours (%q) is invalid", rec["Hours"])
		}
		if hours < 0 {
			return fail("Hours (%q) is negative", rec["Hours"])
		}
		start := date
		end := date.Add(time.Duration(hours * float64(time.Hour)))
		if err := b.insertTimeEntry(ctx, timeEntryCreate{
			UserID: userID, MemberID: memberID, TaskID: taskID,
			ProjectID: projectID, ClientID: clientID,
			Description: rec["Notes"],
			Billable:    billable,
			BillableRate: b.billableRate(billable, projectMember, project, member),
			Tags:        nil,
			Start:       start,
			End:         end,
		}); err != nil {
			return err
		}
	}
	return b.recalcSpent(ctx)
}

// importHarvestProjects ports HarvestProjectsImporter.
func (b *base) importHarvestProjects(ctx context.Context, data []byte) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header, "Client", "Project", "Budget", "Billable Hours"); err != nil {
		return err
	}
	for _, rec := range records {
		var clientID pgtype.UUID
		if rec["Client"] != "" {
			clientID, err = b.clientKey(ctx, rec["Client"], pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		if rec["Project"] == "" {
			continue
		}
		var estimated *int32
		budgetField := strings.ReplaceAll(rec["Budget"], ",", ".")
		if budgetField != "" {
			if f, err := strconv.ParseFloat(budgetField, 64); err == nil {
				if e := int32(f * 3600); e != 0 {
					estimated = &e
				}
			}
		}
		var billableHours *int
		bhField := strings.ReplaceAll(rec["Billable Hours"], ",", ".")
		if bhField != "" {
			if f, err := strconv.ParseFloat(bhField, 64); err == nil {
				bh := int(f)
				billableHours = &bh
			}
		}
		isBillable := billableHours != nil && *billableHours > 0
		if _, err := b.projectKey(ctx, rec["Project"], clientID, projectCreate{
			Color:         randomColor(),
			EstimatedTime: estimated,
			IsBillable:    isBillable,
		}, ""); err != nil {
			return err
		}
	}
	return nil
}

// importHarvestClients ports HarvestClientsImporter.
func (b *base) importHarvestClients(ctx context.Context, data []byte) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header, "Client Name"); err != nil {
		return err
	}
	for _, rec := range records {
		if _, err := b.clientKey(ctx, rec["Client Name"], pgtype.Timestamp{}, ""); err != nil {
			return err
		}
	}
	return nil
}
