package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

func testOrgUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	id, ok := domain.UUID("0190abcd-1234-7000-8000-0000000000aa")
	if !ok {
		t.Fatal("bad test uuid")
	}
	return id
}

func goalFixture() goalRecord {
	return goalRecord{
		Name: "Daily focus", Type: goalTypePersonal, Comparison: goalComparisonAtLeast,
		TargetSeconds: 4 * 3600, Period: goalPeriodDay,
		Timezone: "Europe/Vienna", WeekStart: "monday",
		Filters: []byte(`{"memberIds":null,"projectIds":null,"taskIds":null,"tagIds":null,"tagMatchType":null,"clientIds":null,"billable":null,"timeEntryType":"work"}`),
	}
}

func zulu(t time.Time) string { return t.UTC().Format(zuluLayout) }

// GoalProgressService::getPeriodBounds — periods are computed in the goal
// timezone and returned in UTC.
func TestGoalPeriodBoundsDay(t *testing.T) {
	goal := goalFixture()
	start, end := goalPeriodBounds(goal, time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC))
	if got := zulu(start); got != "2026-03-07T23:00:00Z" {
		t.Errorf("day start: got %s", got)
	}
	if got := zulu(end); got != "2026-03-08T23:00:00Z" {
		t.Errorf("day end: got %s", got)
	}
}

func TestGoalPeriodBoundsWeekHonoursWeekStart(t *testing.T) {
	goal := goalFixture()
	goal.Period = goalPeriodWeek
	// Wednesday 2026-03-11 in Vienna.
	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)

	start, end := goalPeriodBounds(goal, now)
	if got := zulu(start); got != "2026-03-08T23:00:00Z" { // Monday 00:00 CET
		t.Errorf("monday week start: got %s", got)
	}
	if got := zulu(end); got != "2026-03-15T23:00:00Z" {
		t.Errorf("monday week end: got %s", got)
	}

	goal.WeekStart = "sunday"
	start, end = goalPeriodBounds(goal, now)
	if got := zulu(start); got != "2026-03-07T23:00:00Z" { // Sunday 00:00 CET
		t.Errorf("sunday week start: got %s", got)
	}
	if got := zulu(end); got != "2026-03-14T23:00:00Z" {
		t.Errorf("sunday week end: got %s", got)
	}
}

func TestGoalPeriodBoundsMonthCrossesDST(t *testing.T) {
	goal := goalFixture()
	goal.Period = goalPeriodMonth
	start, end := goalPeriodBounds(goal, time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC))
	if got := zulu(start); got != "2026-02-28T23:00:00Z" { // March 1st 00:00 CET
		t.Errorf("month start: got %s", got)
	}
	if got := zulu(end); got != "2026-03-31T22:00:00Z" { // April 1st 00:00 CEST
		t.Errorf("month end: got %s", got)
	}
}

// A day period on the DST switch is 23 hours long, like Carbon computes it.
func TestGoalPeriodBoundsDayAcrossDST(t *testing.T) {
	goal := goalFixture()
	start, end := goalPeriodBounds(goal, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC))
	if got := zulu(start); got != "2026-03-28T23:00:00Z" {
		t.Errorf("dst day start: got %s", got)
	}
	if got := zulu(end); got != "2026-03-29T22:00:00Z" {
		t.Errorf("dst day end: got %s", got)
	}
	if d := end.Sub(start); d != 23*time.Hour {
		t.Errorf("dst day length: got %s want 23h", d)
	}
}

func TestGoalStatus(t *testing.T) {
	goal := goalFixture()
	goal.TargetSeconds = 3600
	if got := goalStatus(goal, 3599); got != goalStatusInProgress {
		t.Errorf("at_least below target: %s", got)
	}
	if got := goalStatus(goal, 3600); got != goalStatusAchieved {
		t.Errorf("at_least at target: %s", got)
	}
	goal.Comparison = goalComparisonLessThan
	if got := goalStatus(goal, 0); got != goalStatusOnTrack {
		t.Errorf("less_than below target: %s", got)
	}
	if got := goalStatus(goal, 3600); got != goalStatusExceeded {
		t.Errorf("less_than at target: %s", got)
	}
}

// An absent filters object still serializes every key, matching GoalFiltersDto::set.
func TestParseGoalFiltersEmptySerializesAllKeys(t *testing.T) {
	s := &Server{}
	errs := map[string][]string{}
	req := httptest.NewRequest("POST", "/", nil)
	filters := s.parseGoalFilters(req, map[string]json.RawMessage{}, testOrgUUID(t), true, errs)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	raw, err := json.Marshal(filters)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"memberIds":null,"projectIds":null,"taskIds":null,"tagIds":null,"tagMatchType":null,"clientIds":null,"billable":null,"timeEntryType":null}`
	if string(raw) != want {
		t.Errorf("filters JSON:\n got %s\nwant %s", raw, want)
	}
}

func TestParseGoalFiltersEmptyArraysBecomeNull(t *testing.T) {
	s := &Server{}
	errs := map[string][]string{}
	req := httptest.NewRequest("POST", "/", nil)
	body := map[string]json.RawMessage{
		"filters": json.RawMessage(`{"project_ids":[],"tag_ids":[],"member_ids":[]}`),
	}
	filters := s.parseGoalFilters(req, body, testOrgUUID(t), true, errs)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if filters.ProjectIDs != nil || filters.TagIDs != nil || filters.MemberIDs != nil {
		t.Errorf("empty selections must be null: %+v", filters)
	}
}

func TestParseGoalFiltersProhibitsMemberIDsForSingleMemberGoals(t *testing.T) {
	s := &Server{}
	errs := map[string][]string{}
	req := httptest.NewRequest("POST", "/", nil)
	body := map[string]json.RawMessage{
		"filters": json.RawMessage(`{"member_ids":["0190abcd-1234-7000-8000-000000000001"]}`),
	}
	s.parseGoalFilters(req, body, testOrgUUID(t), false, errs)
	if got := errs["filters.member_ids"]; len(got) == 0 || got[0] != "The filters member ids field is prohibited." {
		t.Errorf("prohibited rule: %v", errs)
	}
}

func TestParseGoalFiltersEnumAndTypeErrors(t *testing.T) {
	s := &Server{}
	errs := map[string][]string{}
	req := httptest.NewRequest("POST", "/", nil)
	body := map[string]json.RawMessage{
		"filters": json.RawMessage(`{
			"tag_match_type":"maybe",
			"billable":"yes",
			"time_entry_type":"vacation",
			"project_ids":["nope"]
		}`),
	}
	s.parseGoalFilters(req, body, testOrgUUID(t), true, errs)
	for _, key := range []string{"filters.tag_match_type", "filters.billable", "filters.time_entry_type", "filters.project_ids.0"} {
		if len(errs[key]) == 0 {
			t.Errorf("missing error for %s (got %v)", key, errs)
		}
	}
	if got := errs["filters.project_ids.0"][0]; got != "The selected filters.project_ids.0 is invalid." {
		t.Errorf("implicit attribute message: %q", got)
	}
}

func TestParseGoalFiltersNotAnArray(t *testing.T) {
	s := &Server{}
	errs := map[string][]string{}
	req := httptest.NewRequest("POST", "/", nil)
	body := map[string]json.RawMessage{"filters": json.RawMessage(`"all"`)}
	s.parseGoalFilters(req, body, testOrgUUID(t), true, errs)
	if got := errs["filters"]; len(got) == 0 || got[0] != "The filters field must be an array." {
		t.Errorf("array rule: %v", errs)
	}
}

// GoalResource exposes the upstream field set and no internal columns.
func TestGoalResourceShape(t *testing.T) {
	goal := goalFixture()
	goal.MemberName = nil
	progress := goalProgress{
		PeriodStart:    time.Date(2026, 3, 7, 23, 0, 0, 0, time.UTC),
		PeriodEnd:      time.Date(2026, 3, 8, 23, 0, 0, 0, time.UTC),
		TrackedSeconds: 3600,
		Status:         goalStatusInProgress,
	}
	m := goalResource(goal, progress)
	for _, key := range []string{
		"id", "name", "type", "comparison", "target_seconds", "period", "member_id",
		"member_name", "timezone", "week_start", "is_archived", "filters", "progress",
		"created_at", "updated_at",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %s", key)
		}
	}
	if m["is_archived"] != false {
		t.Errorf("is_archived: %v", m["is_archived"])
	}
	if m["member_id"] != nil {
		t.Errorf("member_id must be null for every-member goals: %v", m["member_id"])
	}
	filters, ok := m["filters"].(map[string]any)
	if !ok {
		t.Fatalf("filters: %T", m["filters"])
	}
	for _, key := range []string{
		"member_ids", "project_ids", "task_ids", "tag_ids", "tag_match_type",
		"client_ids", "billable", "time_entry_type",
	} {
		if _, ok := filters[key]; !ok {
			t.Errorf("missing filter key %s", key)
		}
	}
	progressMap, ok := m["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress: %T", m["progress"])
	}
	if progressMap["status"] != goalStatusInProgress || progressMap["tracked_seconds"] != int64(3600) {
		t.Errorf("progress: %+v", progressMap)
	}
	if progressMap["period_start"] != "2026-03-07T23:00:00Z" {
		t.Errorf("period_start: %v", progressMap["period_start"])
	}
}

// The list and get rows must map onto the same record fields.
func TestGoalRecordFromRows(t *testing.T) {
	list := goalRecordFromList(db.ListGoalsPaginatedRow{Name: "a", Type: goalTypePersonal})
	get := goalRecordFromGet(db.GetGoalByIDRow{Name: "a", Type: goalTypePersonal})
	if list.Name != get.Name || list.Type != get.Type {
		t.Errorf("row converters diverged: %+v %+v", list, get)
	}
}
