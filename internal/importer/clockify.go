package importer

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

var clockifyDateRe = regexp.MustCompile(`^([0-9]{1,2})/([0-9]{1,2})/([0-9]{4}) ([0-9]{1,2}):([0-9]{1,2})(:[0-9]{1,2})? (AM|PM)$`)

// parseClockify ports the two Carbon formats used upstream:
// no seconds  -> 'm/d/Y h:i A'   (12-hour clock, AM/PM applied)
// with seconds -> 'm/d/Y H:i:s A' (24-hour clock; Carbon ignores the AM/PM
// marker when 'H' is used, so "13:04:05 PM" stays 13:04:05).
func parseClockify(s, timezone string) (time.Time, bool) {
	m := clockifyDateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	month, _ := strconv.Atoi(m[1])
	day, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[4])
	min, _ := strconv.Atoi(m[5])
	sec := 0
	if m[6] != "" {
		sec, _ = strconv.Atoi(m[6][1:])
	} else {
		// 12-hour path: apply the AM/PM marker.
		if m[7] == "PM" && hour != 12 {
			hour += 12
		}
		if m[7] == "AM" && hour == 12 {
			hour = 0
		}
	}
	if month > 12 || hour > 23 || min > 59 || sec > 59 || day > 31 {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, loc)
	// Reject normalization of impossible dates (e.g. Feb 31 rolls into March).
	if t.Month() != time.Month(month) || t.Day() != day {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// importClockifyTimeEntries ports ClockifyTimeEntriesImporter, including the
// break handling: break rows carry no project/client/task/tags and are stored
// as type=break, non-billable.
func (b *base) importClockifyTimeEntries(ctx context.Context, data []byte, timezone string) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header,
		"Project", "Description", "User", "Email", "Tags",
		"Start Date", "Start Time", "End Date", "End Time"); err != nil {
		return err
	}
	taskKey := ""
	for _, k := range []string{"Task", "Activity"} {
		for _, h := range header {
			if h == k {
				taskKey = k
			}
		}
		if taskKey != "" {
			break
		}
	}
	if taskKey == "" {
		return fail("Invalid CSV header, missing field: Task")
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
		isBreak := strings.EqualFold(rec["Type"], "break") && rec["Type"] != ""
		var clientID pgtype.UUID
		if !isBreak && rec["Client"] != "" {
			clientID, err = b.clientKey(ctx, rec["Client"], pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		var projectID pgtype.UUID
		var project *projectRow
		var projectMember *projectMemberRow
		if !isBreak && rec["Project"] != "" {
			projectID, err = b.projectKey(ctx, rec["Project"], clientID, projectCreate{
				Color:      randomColor(),
				IsBillable: false,
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
		if !isBreak && rec[taskKey] != "" {
			if err := checkTaskNameLength(rec[taskKey]); err != nil {
				return err
			}
			taskID, err = b.taskKey(ctx, rec[taskKey], projectID, pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
			b.tasks.modelByID(taskID)
		}
		if len(rec["Description"]) > 5000 {
			return fail("Time entry description is too long")
		}
		billable := false
		if v, present := rec["Billable"]; present {
			if v != "Yes" && v != "No" {
				return fail("Invalid billable value")
			}
			billable = v == "Yes"
		}
		var tagIDs []pgtype.UUID
		entryType := "work"
		if isBreak {
			entryType = "break"
			billable = false
		} else {
			tagIDs, err = b.tagIDsFromList(ctx, rec["Tags"], ", ", false)
			if err != nil {
				return err
			}
		}

		startStr := rec["Start Date"] + " " + rec["Start Time"]
		if m := clockifyDateRe.FindStringSubmatch(startStr); m != nil {
			if month, _ := strconv.Atoi(m[1]); month > 12 {
				return fail("Start date (%q) is invalid, please select the correct date format before exporting from Clockify", rec["Start Date"])
			}
		}
		start, ok := parseClockify(startStr, timezone)
		if !ok {
			return fail("Start date (%q) or time (%q) are invalid", rec["Start Date"], rec["Start Time"])
		}
		endStr := rec["End Date"] + " " + rec["End Time"]
		if m := clockifyDateRe.FindStringSubmatch(endStr); m != nil {
			if month, _ := strconv.Atoi(m[1]); month > 12 {
				return fail("Start date (%q) is invalid, please select the correct date format before exporting from Clockify", rec["End Date"])
			}
		}
		end, ok := parseClockify(endStr, timezone)
		if !ok {
			return fail("End date (%q) or time (%q) are invalid", rec["End Date"], rec["End Time"])
		}
		if end.Before(start) {
			return fail("End (%q) is before start (%q)", endStr, startStr)
		}
		if err := b.insertTimeEntry(ctx, timeEntryCreate{
			UserID: userID, MemberID: memberID, TaskID: taskID,
			ProjectID: projectID, ClientID: clientID,
			Description: rec["Description"],
			Billable:    billable,
			BillableRate: b.billableRate(billable, projectMember, project, member),
			Tags:        tagIDs,
			Type:        entryType,
			Start:       start,
			End:         end,
		}); err != nil {
			return err
		}
	}
	return b.recalcSpent(ctx)
}

// importClockifyProjects ports ClockifyProjectsImporter.
func (b *base) importClockifyProjects(ctx context.Context, data []byte) error {
	header, records, err := readCSV(data)
	if err != nil {
		return fail("Invalid CSV data")
	}
	if err := validateHeader(header, "Project", "Status", "Visibility"); err != nil {
		return err
	}
	tasksKey := ""
	for _, k := range []string{"Tasks", "Task", "Activities"} {
		for _, h := range header {
			if h == k {
				tasksKey = k
			}
		}
		if tasksKey != "" {
			break
		}
	}
	if tasksKey == "" {
		return fail("Invalid CSV header, missing field: Tasks")
	}
	rateKey := ""
	for _, h := range header {
		if strings.HasPrefix(h, "Billable Rate (") {
			rateKey = h
			break
		}
	}
	for _, rec := range records {
		var clientID pgtype.UUID
		if rec["Client"] != "" {
			clientID, err = b.clientKey(ctx, rec["Client"], pgtype.Timestamp{}, "")
			if err != nil {
				return err
			}
		}
		var projectID pgtype.UUID
		if rec["Project"] != "" {
			var rate *int32
			if rateKey != "" && rec[rateKey] != "" {
				if f, err := strconv.ParseFloat(rec[rateKey], 64); err == nil {
					r := int32(f * 100)
					rate = &r
				}
			}
			var estimated *int32
			if v := rec["Estimated (h)"]; v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					e := int32(f * 3600)
					estimated = &e
				}
			}
			var archived pgtype.Timestamp
			if rec["Status"] == "Archived" {
				archived = ts(time.Now())
			}
			projectID, err = b.projectKey(ctx, rec["Project"], clientID, projectCreate{
				Color:         randomColor(),
				IsBillable:    rec["Billability"] == "Yes",
				BillableRate:  rate,
				EstimatedTime: estimated,
				ArchivedAt:    archived,
			}, "")
			if err != nil {
				return err
			}
		}
		if rec[tasksKey] != "" {
			for _, task := range strings.Split(rec[tasksKey], ", ") {
				if err := checkTaskNameLength(task); err != nil {
					return err
				}
				if _, err := b.taskKey(ctx, task, projectID, pgtype.Timestamp{}, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

