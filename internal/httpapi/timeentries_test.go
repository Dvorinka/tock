package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"

	"github.com/Dvorinka/tock/internal/domain"
)

func TestParseIDList(t *testing.T) {
	id := "0190abcd-1234-7000-8000-000000000001"
	l := parseIDList(id + ",none," + id)
	if !l.given || l.invalid || !l.none || len(l.ids) != 2 {
		t.Fatalf("got %+v", l)
	}
	if l := parseIDList(""); l.given {
		t.Fatal("empty input must not be given")
	}
	if l := parseIDList("bogus"); !l.invalid {
		t.Fatal("bogus input must be invalid")
	}
	if l := parseIDList("none"); !l.none || len(l.ids) != 0 || l.empty {
		t.Fatal("none-only selection")
	}
}

func TestIDListWhere(t *testing.T) {
	orgID, _ := domain.UUID("0190abcd-1234-7000-8000-0000000000aa")
	tagID, _ := domain.UUID("0190abcd-1234-7000-8000-0000000000bb")
	f := &teFilter{}
	f.add("organization_id = " + f.arg(orgID))
	f.addIDList("project_id", parseIDList(domain.UUIDString(tagID)+",none"))
	where := f.whereClause()
	if !strings.Contains(where, "project_id = ANY(") || !strings.Contains(where, "project_id IS NULL") {
		t.Fatalf("unexpected clause: %s", where)
	}
	// Tag contains + not_contains produce the negated forms.
	f2 := &teFilter{}
	f2.addTagFilter(parseIDList(domain.UUIDString(tagID)), "not_contains")
	if !strings.Contains(f2.whereClause(), "NOT (") || !strings.Contains(f2.whereClause(), "tags IS NULL") {
		t.Fatalf("not_contains clause: %s", f2.whereClause())
	}
	f3 := &teFilter{}
	f3.addTagFilter(parseIDList("none"), "contains")
	if !strings.Contains(f3.whereClause(), "jsonb_array_length") {
		t.Fatalf("untagged clause: %s", f3.whereClause())
	}
}

func TestRoundingSelects(t *testing.T) {
	s, e := roundingSelects("", 0, "2024-01-01 00:00:00")
	if s != "start" || !strings.Contains(e, `coalesce("end"`) {
		t.Fatalf("no-rounding selects: %q %q", s, e)
	}
	s, e = roundingSelects("up", 15, "2024-01-01 00:00:00")
	if !strings.Contains(s, "date_bin('1 minutes'") || !strings.Contains(e, "CASE WHEN") {
		t.Fatalf("up rounding: %q %q", s, e)
	}
	_, e = roundingSelects("down", 15, "2024-01-01 00:00:00")
	if !strings.Contains(e, "date_bin('15 minutes'") {
		t.Fatalf("down rounding: %q", e)
	}
	_, e = roundingSelects("nearest", 15, "2024-01-01 00:00:00")
	if !strings.Contains(e, "interval '7.5 minutes'") {
		t.Fatalf("nearest rounding: %q", e)
	}
}

func TestTimeSlotsBetween(t *testing.T) {
	loc := time.UTC
	start := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 4, 10, 0, 0, 0, time.UTC)
	slots := timeSlotsBetween(start, end, loc, "monday", "day", "2006-01-02")
	want := []string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04"}
	if len(slots) != len(want) {
		t.Fatalf("slots %v", slots)
	}
	for i := range want {
		if slots[i] != want[i] {
			t.Fatalf("slot %d: %s want %s", i, slots[i], want[i])
		}
	}
	// Week slots anchor on the week_start day.
	wslots := timeSlotsBetween(start, end.AddDate(0, 0, 10), loc, "monday", "week", "2006-01-02")
	if len(wslots) != 2 || wslots[0] != "2024-01-01" {
		t.Fatalf("week slots %v", wslots)
	}
}

func TestFillGapsInTimeGroups(t *testing.T) {
	loc := time.UTC
	data := []map[string]any{
		{"key": "2024-01-02", "seconds": int64(60), "cost": int64(5), "grouped_type": nil, "grouped_data": nil},
	}
	got := fillGapsInTimeGroups(data, "day", "", loc, "monday",
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC))
	if len(got) != 3 {
		t.Fatalf("want 3 slots (end exclusive), got %v", got)
	}
	if got[0]["seconds"] != 0 || got[1]["seconds"] != int64(60) || got[2]["key"] != "2024-01-03" {
		t.Fatalf("unexpected filled data %v", got)
	}
}

func TestFormatInterval(t *testing.T) {
	if got := formatInterval(3661); got != "1:01:01" {
		t.Fatalf("got %s", got)
	}
	if got := formatInterval(360000 + 5*60 + 9); got != "100:05:09" {
		t.Fatalf("got %s", got)
	}
}

func TestTimeEntryResourceShape(t *testing.T) {
	start := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	e := mkEntry(start, start.Add(time.Hour))
	r := timeEntryResource(e)
	if r["start"] != "2024-01-01T10:00:00Z" || r["end"] != "2024-01-01T11:00:00Z" {
		t.Fatalf("zulu format: %v", r)
	}
	if r["duration"] != int64(3600) {
		t.Fatalf("duration: %v", r["duration"])
	}
	if tags, ok := r["tags"].([]string); !ok || len(tags) != 0 {
		t.Fatalf("tags must be empty array, got %v", r["tags"])
	}
	// Running entry → null end + null duration.
	e.End = pgtype.Timestamp{}
	r = timeEntryResource(e)
	if r["end"] != nil || r["duration"] != nil {
		t.Fatalf("running entry must have null end/duration: %v", r)
	}
}

func mkEntry(start, end time.Time) db.TimeEntry {
	id, _ := domain.UUID("0190abcd-1234-7000-8000-0000000000cc")
	return db.TimeEntry{
		ID:          id,
		Description: "d",
		Start:       pgtype.Timestamp{Time: start, Valid: true},
		End:         pgtype.Timestamp{Time: end, Valid: true},
		Type:        "work",
	}
}
