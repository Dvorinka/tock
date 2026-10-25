package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
)

func dbReportFixture(name string, public bool, secret string) db.Report {
	rep := db.Report{Name: name, IsPublic: public}
	if secret != "" {
		rep.ShareSecret = &secret
	}
	return rep
}

func propsBody(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

func testUser() *auth.User {
	return &auth.User{ID: "u1", Timezone: "Europe/Berlin", WeekStart: "monday"}
}

const validProps = `{
	"group": "project",
	"sub_group": "task",
	"history_group": "week",
	"start": "2026-09-01T00:00:00Z",
	"end": "2026-10-25T23:59:59Z"
}`

// A minimal valid payload fills user defaults and — unlike my first pass —
// leaves time_entry_type unset, because upstream stores null when absent.
func TestParseReportPropertiesDefaults(t *testing.T) {
	props, errs := parseReportProperties(propsBody(t, validProps), testUser())
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if props.Group != "project" || props.SubGroup != "task" || props.HistoryGroup != "week" {
		t.Errorf("groups not parsed: %+v", props)
	}
	if props.WeekStart != "monday" || props.Timezone != "Europe/Berlin" {
		t.Errorf("user defaults not applied: %q %q", props.WeekStart, props.Timezone)
	}
	if props.TimeEntryType != nil {
		t.Errorf("time_entry_type must stay nil, got %q", *props.TimeEntryType)
	}
	if props.Active != nil || props.Billable != nil {
		t.Error("optional booleans must stay nil")
	}
}

func TestParseReportPropertiesRequiredFields(t *testing.T) {
	_, errs := parseReportProperties(propsBody(t, `{}`), testUser())
	for _, key := range []string{
		"properties.group", "properties.sub_group", "properties.history_group",
		"properties.start", "properties.end",
	} {
		if len(errs[key]) == 0 {
			t.Errorf("missing error for %s", key)
		}
	}
}

func TestParseReportPropertiesInvalidEnums(t *testing.T) {
	body := propsBody(t, `{
		"group": "planet",
		"sub_group": "task",
		"history_group": "fortnight",
		"start": "2026-09-01T00:00:00Z",
		"end": "2026-10-25T23:59:59Z",
		"week_start": "someday",
		"timezone": "Mars/Olympus",
		"tag_match_type": "maybe",
		"rounding_type": "sideways",
		"time_entry_type": "vacation"
	}`)
	props, errs := parseReportProperties(body, testUser())
	if len(errs["properties.group"]) == 0 || len(errs["properties.history_group"]) == 0 {
		t.Errorf("expected enum errors, got %v", errs)
	}
	// Invalid optional values fall back to the user's settings, not errors.
	if props.WeekStart != "monday" {
		t.Errorf("week_start fallback: %q", props.WeekStart)
	}
	if props.Timezone != "Europe/Berlin" {
		t.Errorf("timezone fallback: %q", props.Timezone)
	}
	if props.TagMatchType != nil || props.RoundingType != nil || props.TimeEntryType != nil {
		t.Error("invalid optional enums must be dropped")
	}
}

func TestParseReportPropertiesBadStartFormat(t *testing.T) {
	body := propsBody(t, `{
		"group": "project", "sub_group": "task", "history_group": "week",
		"start": "2026-09-01", "end": "2026-10-25T23:59:59Z"
	}`)
	_, errs := parseReportProperties(body, testUser())
	got := strings.Join(errs["properties.start"], " ")
	if !strings.Contains(got, "does not match the format") {
		t.Errorf("start format error: %q", got)
	}
}

// Filter IDs must be UUIDs; "none" is allowed only for the relation filters
// that support it (clients/projects/tags/tasks, not members).
func TestParseReportPropertiesFilterIDValidation(t *testing.T) {
	uuid := "0190abcd-1234-7000-8000-000000000001"
	body := propsBody(t, `{
		"group": "project", "sub_group": "task", "history_group": "week",
		"start": "2026-09-01T00:00:00Z", "end": "2026-10-25T23:59:59Z",
		"client_ids": ["`+uuid+`", "none"],
		"project_ids": ["`+uuid+`"],
		"tag_ids": ["none"],
		"task_ids": ["`+uuid+`"],
		"member_ids": ["`+uuid+`"]
	}`)
	props, errs := parseReportProperties(body, testUser())
	if len(errs) > 0 {
		t.Fatalf("valid payload rejected: %v", errs)
	}
	if props.ClientIDs == nil || len(*props.ClientIDs) != 2 {
		t.Errorf("client ids: %+v", props.ClientIDs)
	}

	bad := propsBody(t, `{
		"group": "project", "sub_group": "task", "history_group": "week",
		"start": "2026-09-01T00:00:00Z", "end": "2026-10-25T23:59:59Z",
		"client_ids": ["nope"],
		"member_ids": ["none"]
	}`)
	_, errs = parseReportProperties(bad, testUser())
	if got := strings.Join(errs["properties.client_ids.0"], " "); !strings.Contains(got, "must be a valid UUID") {
		t.Errorf("client id error: %q", got)
	}
	if got := strings.Join(errs["properties.member_ids.0"], " "); !strings.Contains(got, "must be a valid UUID") {
		t.Errorf("member id must not accept none: %q", got)
	}
}

func TestParseReportPropertiesArrayType(t *testing.T) {
	body := propsBody(t, `{
		"group": "project", "sub_group": "task", "history_group": "week",
		"start": "2026-09-01T00:00:00Z", "end": "2026-10-25T23:59:59Z",
		"client_ids": "all"
	}`)
	_, errs := parseReportProperties(body, testUser())
	if got := strings.Join(errs["properties.client_ids"], " "); !strings.Contains(got, "must be an array") {
		t.Errorf("array error: %q", got)
	}
}

// public_until keeps Laravel's two distinct messages: format vs after:now.
func TestParseNullableZuluMessages(t *testing.T) {
	errs := map[string][]string{}
	body := map[string]json.RawMessage{"public_until": json.RawMessage(`"20.02.2000"`)}
	parseNullableZulu(body, "public_until", errs)
	if got := strings.Join(errs["public_until"], " "); !strings.Contains(got, "does not match the format") {
		t.Errorf("format message: %q", got)
	}

	errs = map[string][]string{}
	body = map[string]json.RawMessage{"public_until": json.RawMessage(`"2000-02-22T14:58:59Z"`)}
	parseNullableZulu(body, "public_until", errs)
	if got := strings.Join(errs["public_until"], " "); !strings.Contains(got, "must be a date after now") {
		t.Errorf("after message: %q", got)
	}

	errs = map[string][]string{}
	future := time.Now().UTC().Add(48 * time.Hour).Format("2006-01-02T15:04:05Z")
	body = map[string]json.RawMessage{"public_until": json.RawMessage(`"` + future + `"`)}
	if ts := parseNullableZulu(body, "public_until", errs); !ts.Valid {
		t.Errorf("future date rejected: %v", errs)
	}
}

// The resource exposes shareable_link only while the report is public, and
// never leaks share_secret (upstream ReportResource has no such field).
func TestReportResourceShape(t *testing.T) {
	var rep = dbReportFixture("Smoke", true, "s3cret")
	srv := &Server{appURL: "http://localhost:8080"}
	m := srv.reportResource(rep, false)
	if _, ok := m["share_secret"]; ok {
		t.Error("share_secret must not be serialized")
	}
	if got := m["shareable_link"]; got != "http://localhost:8080/shared-report#s3cret" {
		t.Errorf("shareable_link: %v", got)
	}
	if _, ok := m["properties"]; ok {
		t.Error("list resource must not include properties")
	}

	private := dbReportFixture("Hidden", false, "")
	srv = &Server{appURL: "http://localhost:8080/"}
	m = srv.reportResource(private, false)
	if m["shareable_link"] != nil {
		t.Errorf("private report must not expose a link: %v", m["shareable_link"])
	}
}

func TestGenerateSecretShape(t *testing.T) {
	s := generateSecret(40)
	if len(s) != 40 {
		t.Fatalf("length %d", len(s))
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			t.Fatalf("unexpected character %q", r)
		}
	}
	if generateSecret(40) == s {
		t.Fatal("secrets must differ")
	}
}
