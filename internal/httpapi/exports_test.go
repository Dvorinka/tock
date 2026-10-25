package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func unzipPart(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	t.Fatalf("%s missing from package", name)
	return ""
}

func testRow() detailedRow {
	// Sunday 2026-03-08 09:00 UTC for 1h30m, plus a running entry.
	return detailedRow{
		description: "design review", project: "Site", client: "Acme", user: "Ada",
		start:    time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC),
		end:      time.Date(2026, 3, 8, 10, 30, 0, 0, time.UTC),
		hasEnd:   true,
		duration: 5400, billable: true, tags: "a, b",
	}
}

// Detailed CSV always uses IntervalService::format (H:MM:SS) even when the
// organization prefers a verbose interval format, while XLSX/ODS use the org
// format — the two writers are fed different interval functions on purpose.
func TestDetailedCsvIntervalIsAlwaysColonFormat(t *testing.T) {
	l := locWith("comma-point", "", "hyphen-separated-yyyy-mm-dd", "hours-minutes", "24-hours", "EUR")
	cells := testRow().csvCells(l, formatInterval)
	if len(cells) != len(detailedHeader) {
		t.Fatalf("want %d cells, got %d", len(detailedHeader), len(cells))
	}
	if cells[7] != "1:30:00" {
		t.Errorf("csv duration: got %q want 1:30:00", cells[7])
	}
	if cells[8] != "1.5" {
		t.Errorf("csv decimal: got %q want 1.5", cells[8])
	}
	if cells[5] != "2026-03-08 09:00:00" {
		t.Errorf("csv start: got %q", cells[5])
	}
	if cells[9] != "Yes" || cells[10] != "No" {
		t.Errorf("billable/break: got %q/%q", cells[9], cells[10])
	}
}

func TestDetailedCsvRunningEntryLeavesEndBlank(t *testing.T) {
	row := testRow()
	row.hasEnd = false
	cells := row.csvCells(locWith("comma-point", "", "", "", "", "EUR"), formatInterval)
	for _, i := range []int{6, 7, 8} {
		if cells[i] != "" {
			t.Errorf("column %d must be empty for a running entry, got %q", i, cells[i])
		}
	}
}

// writeDetailedXLSX must store Excel serial dates with the upstream number
// format and keep the duration as text.
func TestDetailedXLSXCellFormats(t *testing.T) {
	l := locWith("comma-point", "", "", "hours-minutes", "", "EUR")
	data, err := writeDetailedXLSX([]detailedRow{testRow()}, l)
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	styles := unzipPart(t, data, "xl/styles.xml")
	if !strings.Contains(styles, `formatCode="yyyy-mm-dd hh:mm:ss"`) {
		t.Error("missing yyyy-mm-dd hh:mm:ss number format")
	}
	sheet := unzipPart(t, data, "xl/worksheets/sheet1.xml")
	if !strings.Contains(sheet, `t="n"`) && !strings.Contains(sheet, "<v>") {
		t.Error("expected numeric serial date cells")
	}
	// 2026-03-08 09:00 UTC → Excel serial 46089.375
	if !strings.Contains(sheet, "46089.375") {
		t.Errorf("expected Excel serial 46089.375 in sheet: %s", sheet)
	}
	shared := unzipPart(t, data, "xl/sharedStrings.xml")
	if !strings.Contains(shared, "1h 30m") {
		t.Errorf("duration must use the org interval format, got %s", shared)
	}
}

// Aggregate XLSX mirrors the spreadsheet blade: [hh]:mm:ss durations as Excel
// day fractions, SUM formulas in the total row, and a border on every cell.
func TestAggregateXLSXFormulasAndBorders(t *testing.T) {
	l := locWith("comma-point", "", "", "", "", "EUR")
	rows := []aggExportRow{
		{g1Label: "Site", g2Label: "Design", seconds: 5400, cost: 9000},
		{g1Label: "Site", g2Label: "-", seconds: 1800, cost: 0},
	}
	data, err := writeAggXLSX(rows, "project", "task", "EUR", true, l)
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	sheet := unzipPart(t, data, "xl/worksheets/sheet1.xml")
	for _, want := range []string{"SUM(C2:C3)", "SUM(D2:D3)", "SUM(E2:E3)"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("missing formula %s", want)
		}
	}
	// 5400s → 0.0625 Excel days; 1.5 hours → 1.5
	if !strings.Contains(sheet, "0.0625") || !strings.Contains(sheet, "1.5") {
		t.Errorf("expected day-fraction durations in %s", sheet)
	}
	styles := unzipPart(t, data, "xl/styles.xml")
	if !strings.Contains(styles, `formatCode="[hh]:mm:ss"`) {
		t.Error("missing [hh]:mm:ss number format")
	}
	if !strings.Contains(styles, "<border>") {
		t.Error("expected bordered cell styles")
	}
}

// Without billable rates upstream still emits the amount header and a SUM cell
// in the total row (a blade quirk) while body rows stop at four cells.
func TestAggregateXLSXAlwaysEmitsAmountTotal(t *testing.T) {
	data, err := writeAggXLSX([]aggExportRow{{g1Label: "Site", g2Label: "Design", seconds: 60}}, "project", "task", "EUR", false, locWith("comma-point", "", "", "", "", "EUR"))
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	sheet := unzipPart(t, data, "xl/worksheets/sheet1.xml")
	if !strings.Contains(sheet, "SUM(E2:E2)") {
		t.Errorf("total row must keep the amount formula: %s", sheet)
	}
	shared := unzipPart(t, data, "xl/sharedStrings.xml")
	if !strings.Contains(shared, "Amount (EUR)") {
		t.Error("amount header must always be present")
	}
}

// ODS output must be a valid package with the upstream string/float split.
func TestAggregateODSIsValidPackage(t *testing.T) {
	l := locWith("comma-point", "", "", "", "", "EUR")
	data, err := writeAggODS([]aggExportRow{{g1Label: "Site", g2Label: "Design", seconds: 5400, cost: 9000}}, "project", "task", "EUR", true, l)
	if err != nil {
		t.Fatalf("ods: %v", err)
	}
	content := unzipPart(t, data, "content.xml")
	if !strings.Contains(content, "Site") || !strings.Contains(content, "1:30:00") {
		t.Errorf("expected labels and colon interval in %s", content)
	}
	if !strings.Contains(content, `office:value-type="string"`) {
		t.Error("aggregate ODS cells are strings upstream")
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	unzipPart(t, data, "META-INF/manifest.xml")
}

func TestDetailedODSDecimalIsNumeric(t *testing.T) {
	l := locWith("comma-point", "", "", "hours-minutes", "", "EUR")
	data, err := writeDetailedODS([]detailedRow{testRow()}, l)
	if err != nil {
		t.Fatalf("ods: %v", err)
	}
	content := unzipPart(t, data, "content.xml")
	if !strings.Contains(content, `office:value-type="float" office:value="1.5"`) {
		t.Errorf("decimal column must be numeric, got %s", content)
	}
	if !strings.Contains(content, "1h 30m") {
		t.Error("duration column must use the org interval format")
	}
}

// Labels follow the blade: billable → Yes/No, description first, then the
// localized key, then '-'.
func TestAggregateLabels(t *testing.T) {
	l := locWith("comma-point", "", "hyphen-separated-yyyy-mm-dd", "", "", "EUR")
	desc := "Site rebuild"
	billable := "1"
	nonBillable := "0"

	if got := aggCellLabel(map[string]any{"key": "abc", "description": desc}, "project", l); got != "Site rebuild" {
		t.Errorf("description: %q", got)
	}
	if got := aggCellLabel(map[string]any{"key": nil, "description": nil}, "project", l); got != "-" {
		t.Errorf("null key: %q", got)
	}
	if got := aggCellLabel(map[string]any{"key": billable}, "billable", l); got != "Yes" {
		t.Errorf("billable: %q", got)
	}
	if got := aggCellLabel(map[string]any{"key": nonBillable}, "billable", l); got != "No" {
		t.Errorf("non-billable: %q", got)
	}
	if got := aggCellLabel(map[string]any{"key": "2026-03-08"}, "day", l); got != "2026-03-08" {
		t.Errorf("day key: %q", got)
	}
	// PDF variant spells out billable and null keys.
	if got := aggCellLabelPDF(map[string]any{"key": billable}, "billable", l); got != "Billable" {
		t.Errorf("pdf billable: %q", got)
	}
	if got := aggCellLabelPDF(map[string]any{"key": nil}, "project", l); got != "No project" {
		t.Errorf("pdf null project: %q", got)
	}
}

func TestAggregateHeaderUppercasesCurrency(t *testing.T) {
	got := aggExportHeader("project", "task", "eur")
	want := []string{"Project", "Task", "Duration", "Duration (decimal)", "Amount (EUR)"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d: got %q want %q", i, got[i], want[i])
		}
	}
}
