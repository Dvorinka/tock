package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/domain"
)

// csvBytes must write RFC4180 like League CSV with enclosure '"': quote only
// when needed, double embedded quotes, LF line endings.
func TestCSVBytes(t *testing.T) {
	out, err := csvBytes(
		[]string{"id", "name"},
		[][]string{
			{"1", "plain"},
			{"2", "with,comma"},
			{"3", `with"quote`},
			{"4", "with\nnewline"},
		},
	)
	if err != nil {
		t.Fatalf("csvBytes: %v", err)
	}
	want := "id,name\n" +
		"1,plain\n" +
		"2,\"with,comma\"\n" +
		"3,\"with\"\"quote\"\n" +
		"4,\"with\nnewline\"\n"
	if string(out) != want {
		t.Errorf("csv output:\n got %q\nwant %q", out, want)
	}

	records, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(records) != 5 || records[3][1] != `with"quote` {
		t.Errorf("round trip: %#v", records)
	}
}

func TestOrganizationExportValueFormatting(t *testing.T) {
	when := time.Date(2026, 10, 25, 9, 5, 3, 0, time.UTC)
	if got := zuluOrEmpty(pgtype.Timestamp{Time: when, Valid: true}); got != "2026-10-25T09:05:03Z" {
		t.Errorf("timestamp: %q", got)
	}
	if got := zuluOrEmpty(pgtype.Timestamp{}); got != "" {
		t.Errorf("null timestamp: %q", got)
	}
	name := "tag"
	if got := textOrEmpty(&name); got != "tag" {
		t.Errorf("text: %q", got)
	}
	if got := textOrEmpty(nil); got != "" {
		t.Errorf("null text: %q", got)
	}
	rate := int32(4200)
	if got := int32OrEmpty(&rate); got != "4200" {
		t.Errorf("int: %q", got)
	}
	if got := int32OrEmpty(nil); got != "" {
		t.Errorf("null int: %q", got)
	}
	if boolText(true) != "true" || boolText(false) != "false" {
		t.Errorf("bool formatting")
	}
	id, ok := domain.UUID("0190abcd-1234-7000-8000-000000000001")
	if !ok {
		t.Fatal("bad uuid")
	}
	if got := uuidOrEmpty(id); got != "0190abcd-1234-7000-8000-000000000001" {
		t.Errorf("uuid: %q", got)
	}
	if got := uuidOrEmpty(pgtype.UUID{}); got != "" {
		t.Errorf("null uuid: %q", got)
	}
}

// meta.json drives the importer's version check; key order and shape follow
// ExportService::export.
func TestOrganizationExportMetaJSON(t *testing.T) {
	raw, err := json.Marshal(organizationExportMeta{
		ID:            "11111111-1111-1111-1111-111111111111",
		Version:       organizationExportVersion,
		Organizations: []string{"22222222-2222-2222-2222-222222222222"},
		ExportedAt:    "2026-10-25T09:05:03Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"11111111-1111-1111-1111-111111111111","version":"1.0",` +
		`"organizations":["22222222-2222-2222-2222-222222222222"],"exported_at":"2026-10-25T09:05:03Z"}`
	if string(raw) != want {
		t.Errorf("meta.json:\n got %s\nwant %s", raw, want)
	}
}
