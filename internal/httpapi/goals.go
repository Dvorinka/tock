package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// ---- enums (app/Enums/Goal*.php) ----

const (
	goalTypePersonal     = "personal"
	goalTypeOrganization = "organization"

	goalComparisonAtLeast  = "at_least"
	goalComparisonLessThan = "less_than"

	goalPeriodDay   = "day"
	goalPeriodWeek  = "week"
	goalPeriodMonth = "month"

	goalStatusInProgress = "in_progress"
	goalStatusAchieved   = "achieved"
	goalStatusOnTrack    = "on_track"
	goalStatusExceeded   = "exceeded"
)

var goalTypes = map[string]bool{goalTypePersonal: true, goalTypeOrganization: true}
var goalComparisons = map[string]bool{goalComparisonAtLeast: true, goalComparisonLessThan: true}
var goalPeriods = map[string]bool{goalPeriodDay: true, goalPeriodWeek: true, goalPeriodMonth: true}

// ---- GoalFiltersDto port ----

// goalFilters mirrors upstream GoalFiltersDto. The JSONB column uses camelCase
// keys (Laravel cast); the API surfaces them as snake_case.
type goalFilters struct {
	MemberIDs     *[]string `json:"memberIds"`
	ProjectIDs    *[]string `json:"projectIds"`
	TaskIDs       *[]string `json:"taskIds"`
	TagIDs        *[]string `json:"tagIds"`
	TagMatchType  *string   `json:"tagMatchType"`
	ClientIDs     *[]string `json:"clientIds"`
	Billable      *bool     `json:"billable"`
	TimeEntryType *string   `json:"timeEntryType"`
}

// goalRecord is the shared shape of the sqlc list/get row types.
type goalRecord struct {
	ID             pgtype.UUID
	Name           string
	Type           string
	Comparison     string
	TargetSeconds  int64
	Period         string
	Filters        []byte
	Timezone       string
	WeekStart      string
	ArchivedAt     pgtype.Timestamp
	OrganizationID pgtype.UUID
	MemberID       pgtype.UUID
	CreatedAt      pgtype.Timestamp
	UpdatedAt      pgtype.Timestamp
	MemberName     *string
}

func goalRecordFromList(r db.ListGoalsPaginatedRow) goalRecord {
	return goalRecord{
		ID: r.ID, Name: r.Name, Type: r.Type, Comparison: r.Comparison,
		TargetSeconds: r.TargetSeconds, Period: r.Period, Filters: r.Filters,
		Timezone: r.Timezone, WeekStart: r.WeekStart, ArchivedAt: r.ArchivedAt,
		OrganizationID: r.OrganizationID, MemberID: r.MemberID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, MemberName: r.MemberName,
	}
}

func goalRecordFromGet(r db.GetGoalByIDRow) goalRecord {
	return goalRecord{
		ID: r.ID, Name: r.Name, Type: r.Type, Comparison: r.Comparison,
		TargetSeconds: r.TargetSeconds, Period: r.Period, Filters: r.Filters,
		Timezone: r.Timezone, WeekStart: r.WeekStart, ArchivedAt: r.ArchivedAt,
		OrganizationID: r.OrganizationID, MemberID: r.MemberID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, MemberName: r.MemberName,
	}
}

func decodeGoalFilters(raw []byte) goalFilters {
	var f goalFilters
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// ---- GoalProgressService port ----

type goalProgress struct {
	PeriodStart    time.Time
	PeriodEnd      time.Time
	TrackedSeconds int64
	Status         string
}

// weekdayIndex maps a Weekday enum to time.Weekday.
func weekdayIndex(day string) time.Weekday {
	switch day {
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	case "sunday":
		return time.Sunday
	default:
		return time.Monday
	}
}

// goalPeriodBounds ports getPeriodBounds: the period containing $now, computed
// in the goal's timezone and respecting its week start. Start inclusive, end
// exclusive, both in UTC.
func goalPeriodBounds(goal goalRecord, now time.Time) (time.Time, time.Time) {
	loc, err := time.LoadLocation(goal.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)

	var start time.Time
	switch goal.Period {
	case goalPeriodWeek:
		offset := (int(local.Weekday()) - int(weekdayIndex(goal.WeekStart)) + 7) % 7
		d := local.AddDate(0, 0, -offset)
		start = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	case goalPeriodMonth:
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	default: // day
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	}

	var end time.Time
	switch goal.Period {
	case goalPeriodWeek:
		end = start.AddDate(0, 0, 7)
	case goalPeriodMonth:
		end = start.AddDate(0, 1, 0)
	default:
		end = start.AddDate(0, 0, 1)
	}
	return start.UTC(), end.UTC()
}

// goalStatus ports getStatus.
func goalStatus(goal goalRecord, trackedSeconds int64) string {
	if goal.Comparison == goalComparisonLessThan {
		if trackedSeconds < goal.TargetSeconds {
			return goalStatusOnTrack
		}
		return goalStatusExceeded
	}
	if trackedSeconds >= goal.TargetSeconds {
		return goalStatusAchieved
	}
	return goalStatusInProgress
}

// goalProgressFor mirrors getCurrentProgress.
func (s *Server) goalProgressFor(ctx context.Context, goal goalRecord, now time.Time) (goalProgress, error) {
	start, end := goalPeriodBounds(goal, now)
	tracked, err := s.goalTrackedSeconds(ctx, goal, start, end, now)
	if err != nil {
		return goalProgress{}, err
	}
	return goalProgress{
		PeriodStart: start, PeriodEnd: end,
		TrackedSeconds: tracked, Status: goalStatus(goal, tracked),
	}, nil
}

// goalTrackedSeconds ports getProgress: the seconds in the period that match
// the goal filters. A running entry counts up to now.
func (s *Server) goalTrackedSeconds(ctx context.Context, goal goalRecord, periodStart, periodEnd, now time.Time) (int64, error) {
	f := &teFilter{}
	f.add("organization_id = " + f.arg(goal.OrganizationID))
	if goal.MemberID.Valid {
		f.add("member_id = " + f.arg(goal.MemberID))
	}
	f.add("start >= " + f.arg(periodStart))
	f.add(`start < ` + f.arg(periodEnd))

	filters := decodeGoalFilters(goal.Filters)
	f.addIDList("project_id", parseIDListStr(derefList(filters.ProjectIDs)))
	f.addIDList("task_id", parseIDListStr(derefList(filters.TaskIDs)))
	match := "contains"
	if filters.TagMatchType != nil {
		match = *filters.TagMatchType
	}
	f.addTagFilter(parseIDListStr(derefList(filters.TagIDs)), match)
	f.addIDList("client_id", parseIDListStr(derefList(filters.ClientIDs)))
	if filters.Billable != nil {
		f.add("billable = " + f.arg(*filters.Billable))
	}
	if filters.TimeEntryType != nil {
		f.add("type = " + f.arg(*filters.TimeEntryType))
	}
	if !goal.MemberID.Valid {
		f.addIDList("member_id", parseIDListStr(derefList(filters.MemberIDs)))
	}

	// now must be UTC — "end" is a naive timestamp; a local-time arg would
	// add the tz offset to the running-entry duration.
	query := `SELECT coalesce(round(sum(extract(epoch from (coalesce("end", ` + f.arg(now.UTC()) +
		`::timestamp) - "start")))), 0)::bigint FROM time_entries WHERE ` + f.whereClause()
	var tracked int64
	if err := s.pool.QueryRow(ctx, query, f.args...).Scan(&tracked); err != nil {
		return 0, fmt.Errorf("goal progress: %w", err)
	}
	if tracked < 0 {
		tracked = 0
	}
	return tracked, nil
}

// ---- resource ----

func goalResource(goal goalRecord, progress goalProgress) map[string]any {
	filters := decodeGoalFilters(goal.Filters)
	var memberID any
	if goal.MemberID.Valid {
		memberID = domain.UUIDString(goal.MemberID)
	}
	var memberName any
	if goal.MemberName != nil {
		memberName = *goal.MemberName
	}
	return map[string]any{
		"id":             domain.UUIDString(goal.ID),
		"name":           goal.Name,
		"type":           goal.Type,
		"comparison":     goal.Comparison,
		"target_seconds": goal.TargetSeconds,
		"period":         goal.Period,
		"member_id":      memberID,
		"member_name":    memberName,
		"timezone":       goal.Timezone,
		"week_start":     goal.WeekStart,
		"is_archived":    goal.ArchivedAt.Valid,
		"filters": map[string]any{
			"member_ids":      filters.MemberIDs,
			"project_ids":     filters.ProjectIDs,
			"task_ids":        filters.TaskIDs,
			"tag_ids":         filters.TagIDs,
			"tag_match_type":  filters.TagMatchType,
			"client_ids":      filters.ClientIDs,
			"billable":        filters.Billable,
			"time_entry_type": filters.TimeEntryType,
		},
		"progress": map[string]any{
			"period_start":    progress.PeriodStart.UTC().Format(zuluLayout),
			"period_end":      progress.PeriodEnd.UTC().Format(zuluLayout),
			"tracked_seconds": progress.TrackedSeconds,
			"status":          progress.Status,
		},
		"created_at": tsFormat(goal.CreatedAt),
		"updated_at": tsFormat(goal.UpdatedAt),
	}
}

// ---- handlers ----

// resolveGoal loads {goal} and enforces the organization check
// (GoalController::checkGoalBelongsToOrganization).
func (s *Server) resolveGoal(w http.ResponseWriter, r *http.Request, orgID pgtype.UUID) (goalRecord, bool) {
	var goal goalRecord
	id, ok := domain.UUID(chi.URLParam(r, "goal"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Goal", chi.URLParam(r, "goal")))
		return goal, false
	}
	row, err := db.New(s.pool).GetGoalByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Goal", chi.URLParam(r, "goal")))
		return goal, false
	}
	if row.OrganizationID != orgID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return goal, false
	}
	return goalRecordFromGet(row), true
}

func (s *Server) handleIndexGoals(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	q := r.URL.Query()
	errs := map[string][]string{}
	goalType := ""
	if raw := q.Get("type"); raw != "" {
		if !goalTypes[raw] {
			errs["type"] = []string{"The selected type is invalid."}
		} else {
			goalType = raw
		}
	}
	archivedFilter := "false"
	if raw := q.Get("archived"); raw != "" {
		if raw != "true" && raw != "false" && raw != "all" {
			errs["archived"] = []string{"The selected archived is invalid."}
		} else {
			archivedFilter = raw
		}
	}
	page := 1
	if raw := q.Get("page"); raw != "" {
		p, convErr := strconv.Atoi(raw)
		if convErr != nil || p < 1 {
			errs["page"] = []string{"The page field must be an integer."}
		} else {
			page = p
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	// An organization goal is visible to the member it is for, who does not
	// hold the :organization-type permission — either permission is enough.
	if !scope.has("goals:view:own") && !scope.has("goals:view:organization-type") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	queries := db.New(s.pool)
	countParams := db.CountGoalsParams{
		OrganizationID: scope.org.ID,
		MemberID:       scope.member.ID,
		Archived:       archivedFilter,
	}
	if goalType != "" {
		countParams.GoalType = &goalType
	}
	total, err := queries.CountGoals(r.Context(), countParams)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	listParams := db.ListGoalsPaginatedParams{
		OrganizationID: scope.org.ID,
		MemberID:       scope.member.ID,
		GoalType:       countParams.GoalType,
		Archived:       archivedFilter,
		PageLimit:      defaultPerPage,
		PageOffset:     int32((page - 1) * defaultPerPage),
	}
	rows, err := queries.ListGoalsPaginated(r.Context(), listParams)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}

	now := time.Now()
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		goal := goalRecordFromList(row)
		progress, err := s.goalProgressFor(r.Context(), goal, now)
		if err != nil {
			slog.ErrorContext(r.Context(), "goal progress", "error", err)
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		items = append(items, goalResource(goal, progress))
	}
	paginatedResponse(w, r, items, total, page, defaultPerPage)
}

func (s *Server) handleShowGoal(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	goal, ok := s.resolveGoal(w, r, scope.org.ID)
	if !ok {
		return
	}
	if !scope.has("goals:view:own") && !scope.has("goals:view:organization-type") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if !s.canViewGoal(scope, goal) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	progress, err := s.goalProgressFor(r.Context(), goal, time.Now())
	if err != nil {
		slog.ErrorContext(r.Context(), "goal progress", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": goalResource(goal, progress)})
}

// canViewGoal ports GoalsContract::canViewGoal.
func (s *Server) canViewGoal(scope *orgScope, goal goalRecord) bool {
	return goal.Type == goalTypePersonal && goal.MemberID.Valid && goal.MemberID == scope.member.ID
}

func (s *Server) handleStoreGoal(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}

	name, namePresent, nameValid := jsonStr(body, "name")
	switch {
	case !namePresent:
		errs["name"] = []string{"The name field is required."}
	case !nameValid:
		errs["name"] = []string{"The name field must be a string."}
	case len(name) > 255:
		errs["name"] = []string{"The name field must not be greater than 255 characters."}
	}

	goalType, typePresent, typeValid := jsonStr(body, "type")
	switch {
	case !typePresent:
		errs["type"] = []string{"The type field is required."}
	case !typeValid || !goalTypes[goalType]:
		errs["type"] = []string{"The selected type is invalid."}
	}

	comparison, cmpPresent, cmpValid := jsonStr(body, "comparison")
	switch {
	case !cmpPresent:
		errs["comparison"] = []string{"The comparison field is required."}
	case !cmpValid || !goalComparisons[comparison]:
		errs["comparison"] = []string{"The selected comparison is invalid."}
	}

	targetSeconds, targetPresent, targetValid := jsonInt(body, "target_seconds")
	switch {
	case !targetPresent:
		errs["target_seconds"] = []string{"The target seconds field is required."}
	case !targetValid:
		errs["target_seconds"] = []string{"The target seconds field must be an integer."}
	case targetSeconds < 1:
		errs["target_seconds"] = []string{"The target seconds field must be at least 1."}
	case targetSeconds > 2147483647:
		errs["target_seconds"] = []string{"The target seconds field must not be greater than 2147483647."}
	}

	period, periodPresent, periodValid := jsonStr(body, "period")
	switch {
	case !periodPresent:
		errs["period"] = []string{"The period field is required."}
	case !periodValid || !goalPeriods[period]:
		errs["period"] = []string{"The selected period is invalid."}
	}

	// member_id: a personal goal is always for the current member, an
	// organization goal may target a member or every member (null).
	memberID := scope.member.ID
	memberEvery := false
	rawMember, memberPresent := body["member_id"]
	if memberPresent {
		if string(rawMember) == "null" {
			if goalType == goalTypePersonal {
				errs["member_id"] = []string{"The member id field must have a value."}
			} else {
				memberID = pgtype.UUID{}
				memberEvery = true
			}
		} else {
			value, okStr := rawString(rawMember)
			if !okStr {
				errs["member_id"] = []string{"The member id field must be a string."}
			} else if goalType == goalTypePersonal {
				if value != domain.UUIDString(scope.member.ID) {
					errs["member_id"] = []string{"The selected member id is invalid."}
				}
			} else if id, valid := domain.UUID(value); !valid {
				errs["member_id"] = []string{"The selected member id is invalid."}
			} else if !s.memberExistsInOrg(r.Context(), scope.org.ID, id) {
				errs["member_id"] = []string{"The selected member id is invalid."}
			} else {
				memberID = id
			}
		}
	}

	timezone := ""
	if raw, present := body["timezone"]; present && string(raw) != "null" {
		value, okStr := rawString(raw)
		if !okStr || !isValidTimezone(value) {
			errs["timezone"] = []string{"The timezone field must be a valid timezone."}
		} else {
			timezone = value
		}
	}

	weekStart := ""
	if raw, present := body["week_start"]; present && string(raw) != "null" {
		value, okStr := rawString(raw)
		if !okStr || !weekdayValues[value] {
			errs["week_start"] = []string{"The selected week start is invalid."}
		} else {
			weekStart = value
		}
	}

	filters := s.parseGoalFilters(r, body, scope.org.ID, memberEvery, errs)

	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	// Permission depends on the type: personal and organization goals have
	// separate permissions.
	perm := "goals:create:own"
	if goalType == goalTypeOrganization {
		perm = "goals:create:organization-type"
	}
	if !scope.has(perm) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	// GoalsContract::canCreateGoal — core only creates personal goals for the
	// acting member.
	if goalType != goalTypePersonal || !memberID.Valid || memberID != scope.member.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	user := auth.UserFromContext(r.Context())
	if timezone == "" {
		timezone = user.Timezone
	}
	if weekStart == "" {
		weekStart = user.WeekStart
	}
	filtersJSON, _ := json.Marshal(filters)
	id := pgUUID()
	if err := db.New(s.pool).InsertGoal(r.Context(), db.InsertGoalParams{
		ID:             id,
		Name:           name,
		GoalType:       goalType,
		Comparison:     comparison,
		TargetSeconds:  targetSeconds,
		Period:         period,
		Filters:        filtersJSON,
		Timezone:       timezone,
		WeekStart:      weekStart,
		OrganizationID: scope.org.ID,
		MemberID:       memberID,
	}); err != nil {
		slog.ErrorContext(r.Context(), "insert goal", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}

	row, err := db.New(s.pool).GetGoalByID(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload goal", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	goal := goalRecordFromGet(row)
	progress, err := s.goalProgressFor(r.Context(), goal, time.Now())
	if err != nil {
		slog.ErrorContext(r.Context(), "goal progress", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	// Upstream sets 201 explicitly for goals.
	writeJSON(w, http.StatusCreated, map[string]any{"data": goalResource(goal, progress)})
}

func (s *Server) handleUpdateGoal(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	goal, ok := s.resolveGoal(w, r, scope.org.ID)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}

	if raw, present := body["name"]; present {
		if value, okStr := rawString(raw); !okStr {
			errs["name"] = []string{"The name field must be a string."}
		} else if len(value) > 255 {
			errs["name"] = []string{"The name field must not be greater than 255 characters."}
		} else {
			goal.Name = value
		}
	}
	if raw, present := body["comparison"]; present {
		if value, okStr := rawString(raw); !okStr || !goalComparisons[value] {
			errs["comparison"] = []string{"The selected comparison is invalid."}
		} else {
			goal.Comparison = value
		}
	}
	if _, present := body["target_seconds"]; present {
		if value, _, valid := jsonInt(body, "target_seconds"); !valid {
			errs["target_seconds"] = []string{"The target seconds field must be an integer."}
		} else if value < 1 {
			errs["target_seconds"] = []string{"The target seconds field must be at least 1."}
		} else if value > 2147483647 {
			errs["target_seconds"] = []string{"The target seconds field must not be greater than 2147483647."}
		} else {
			goal.TargetSeconds = value
		}
	}
	if raw, present := body["period"]; present {
		if value, okStr := rawString(raw); !okStr || !goalPeriods[value] {
			errs["period"] = []string{"The selected period is invalid."}
		} else {
			goal.Period = value
		}
	}
	if raw, present := body["timezone"]; present {
		if value, okStr := rawString(raw); !okStr || !isValidTimezone(value) {
			errs["timezone"] = []string{"The timezone field must be a valid timezone."}
		} else {
			goal.Timezone = value
		}
	}
	if raw, present := body["week_start"]; present {
		if value, okStr := rawString(raw); !okStr || !weekdayValues[value] {
			errs["week_start"] = []string{"The selected week start is invalid."}
		} else {
			goal.WeekStart = value
		}
	}
	archivedAt := goal.ArchivedAt
	if _, present := body["is_archived"]; present {
		value, _, valid := jsonBool(body, "is_archived")
		if !valid {
			errs["is_archived"] = []string{"The is archived field must be true or false."}
		} else if value {
			archivedAt = pgtype.Timestamp{Time: time.Now(), Valid: true}
		} else {
			archivedAt = pgtype.Timestamp{}
		}
	}
	// The type and the member of a goal can not be changed after creation.
	if raw, present := body["type"]; present {
		value, _ := rawString(raw)
		if value != goal.Type {
			errs["type"] = []string{"The type of a goal can not be changed."}
		}
	}
	if raw, present := body["member_id"]; present {
		current := ""
		if goal.MemberID.Valid {
			current = domain.UUIDString(goal.MemberID)
		}
		value := ""
		if string(raw) != "null" {
			value, _ = rawString(raw)
		}
		if value != current {
			errs["member_id"] = []string{"The member id of a goal can not be changed."}
		}
	}
	// filters are only allowed to carry member_ids when the goal counts every
	// member, like in GoalUpdateRequest.
	if _, present := body["filters"]; present {
		goal.Filters = nil
		filters := s.parseGoalFilters(r, body, scope.org.ID, !goal.MemberID.Valid, errs)
		if len(errs) == 0 || filters != nil {
			goal.Filters, _ = json.Marshal(filters)
		}
	}

	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	perm := "goals:update:own"
	if goal.Type == goalTypeOrganization {
		perm = "goals:update:organization-type"
	}
	if !scope.has(perm) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	// GoalsContract::canUpdateGoal
	if !s.canViewGoal(scope, goal) && goal.Type == goalTypePersonal {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if goal.Type == goalTypeOrganization {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	if err := db.New(s.pool).UpdateGoal(r.Context(), db.UpdateGoalParams{
		ID:            goal.ID,
		Name:          goal.Name,
		Comparison:    goal.Comparison,
		TargetSeconds: goal.TargetSeconds,
		Period:        goal.Period,
		Filters:       goal.Filters,
		Timezone:      goal.Timezone,
		WeekStart:     goal.WeekStart,
		ArchivedAt:    archivedAt,
	}); err != nil {
		slog.ErrorContext(r.Context(), "update goal", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}

	row, err := db.New(s.pool).GetGoalByID(r.Context(), goal.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload goal", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	updated := goalRecordFromGet(row)
	progress, err := s.goalProgressFor(r.Context(), updated, time.Now())
	if err != nil {
		slog.ErrorContext(r.Context(), "goal progress", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": goalResource(updated, progress)})
}

func (s *Server) handleDeleteGoal(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	goal, ok := s.resolveGoal(w, r, scope.org.ID)
	if !ok {
		return
	}
	perm := "goals:delete:own"
	if goal.Type == goalTypeOrganization {
		perm = "goals:delete:organization-type"
	}
	if !scope.has(perm) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	// GoalsContract::canDeleteGoal
	if goal.Type != goalTypePersonal || !goal.MemberID.Valid || goal.MemberID != scope.member.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if err := db.New(s.pool).DeleteGoal(r.Context(), goal.ID); err != nil {
		slog.ErrorContext(r.Context(), "delete goal", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- validation helpers ----

// parseGoalFilters ports GoalFilterRules: it validates filters.* and returns
// the DTO to persist. Empty selections become null like set*Ids does.
func (s *Server) parseGoalFilters(r *http.Request, body map[string]json.RawMessage, orgID pgtype.UUID, forEveryMember bool, errs map[string][]string) *goalFilters {
	filters := &goalFilters{}
	raw, present := body["filters"]
	if !present || string(raw) == "null" {
		return filters
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		errs["filters"] = []string{"The filters field must be an array."}
		return filters
	}

	readIDList := func(key, table string, excludePlaceholders bool) *[]string {
		value, present := fields[key]
		if !present || string(value) == "null" {
			return nil
		}
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			errs["filters."+key] = []string{"The filters " + strings.ReplaceAll(key, "_", " ") + " field must be an array."}
			return nil
		}
		ids := make([]pgtype.UUID, 0, len(list))
		for i, item := range list {
			if item == timeEntryFilterNone {
				continue
			}
			id, ok := domain.UUID(item)
			if !ok {
				errs[fmt.Sprintf("filters.%s.%d", key, i)] = []string{goalInvalidSelection(key, i)}
				continue
			}
			ids = append(ids, id)
		}
		// ExistsEloquent: every ID has to belong to the organization.
		if len(ids) > 0 {
			existing, err := s.existingIDs(r.Context(), table, orgID, ids, excludePlaceholders)
			if err != nil {
				slog.ErrorContext(r.Context(), "goal filter existence check", "table", table, "error", err)
				errs["filters."+key] = []string{"The filters " + strings.ReplaceAll(key, "_", " ") + " field must be an array."}
				return nil
			}
			for i, item := range list {
				id, ok := domain.UUID(item)
				if !ok {
					continue
				}
				if !existing[domain.UUIDString(id)] {
					errs[fmt.Sprintf("filters.%s.%d", key, i)] = []string{goalInvalidSelection(key, i)}
				}
			}
		}
		if len(list) == 0 {
			return nil
		}
		return &list
	}

	// Rule::prohibitedIf — member filters only exist for goals that count
	// every member; null and [] pass as "no restriction".
	if raw, present := fields["member_ids"]; present {
		empty := string(raw) == "null" || string(raw) == "[]"
		if !forEveryMember && !empty {
			errs["filters.member_ids"] = []string{"The filters member ids field is prohibited."}
		} else if forEveryMember {
			filters.MemberIDs = readIDList("member_ids", "members", true)
		}
	}
	filters.ProjectIDs = readIDList("project_ids", "projects", false)
	filters.TaskIDs = readIDList("task_ids", "tasks", false)
	filters.TagIDs = readIDList("tag_ids", "tags", false)
	filters.ClientIDs = readIDList("client_ids", "clients", false)

	if value, present := fields["tag_match_type"]; present && string(value) != "null" {
		tagMatch, okStr := rawString(value)
		if !okStr || (tagMatch != "contains" && tagMatch != "not_contains") {
			errs["filters.tag_match_type"] = []string{"The selected filters tag match type is invalid."}
		} else {
			filters.TagMatchType = &tagMatch
		}
	}
	if value, present := fields["billable"]; present && string(value) != "null" {
		var b bool
		if json.Unmarshal(value, &b) != nil {
			errs["filters.billable"] = []string{"The filters billable field must be true or false."}
		} else {
			filters.Billable = &b
		}
	}
	if value, present := fields["time_entry_type"]; present && string(value) != "null" {
		entryType, okStr := rawString(value)
		if !okStr || (entryType != "work" && entryType != "break") {
			errs["filters.time_entry_type"] = []string{"The selected filters time entry type is invalid."}
		} else {
			filters.TimeEntryType = &entryType
		}
	}
	return filters
}

// memberExistsInOrg implements the ExistsEloquent rule on members, which also
// excludes placeholders.
func (s *Server) memberExistsInOrg(ctx context.Context, orgID pgtype.UUID, memberID pgtype.UUID) bool {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM members WHERE id = $1 AND organization_id = $2 AND role <> 'placeholder')`,
		memberID, orgID).Scan(&exists)
	return err == nil && exists
}

// jsonInt reads an optional integer field.
func jsonInt(m map[string]json.RawMessage, key string) (int64, bool, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, true, false
	}
	return n, true, true
}

// goalInvalidSelection renders Laravel's exists message for a wildcard
// attribute, which is displayed verbatim ("filters.project_ids.0").
func goalInvalidSelection(key string, index int) string {
	return "The selected filters." + key + "." + strconv.Itoa(index) + " is invalid."
}

// existingIDs returns the subset of ids that exist in the organization.
func (s *Server) existingIDs(ctx context.Context, table string, orgID pgtype.UUID, ids []pgtype.UUID, excludePlaceholders bool) (map[string]bool, error) {
	query := `SELECT id FROM ` + table + ` WHERE organization_id = $1 AND id = ANY($2)`
	if excludePlaceholders {
		query += ` AND role <> 'placeholder'`
	}
	rows, err := s.pool.Query(ctx, query, orgID, uuidBytes(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[domain.UUIDString(id)] = true
	}
	return found, rows.Err()
}
