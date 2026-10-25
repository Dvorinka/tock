package httpapi

import (
	"testing"
	"time"
)

func locWith(number, currencyFmt, dateFmt, intervalFmt, timeFmt, code string) localization {
	return localization{
		numberFormat: number, currencyFormat: currencyFmt, dateFormat: dateFmt,
		intervalFormat: intervalFmt, timeFormat: timeFmt, currency: code,
	}
}

// Upstream LocalizationService::formatNumber — always 2 decimals, negatives
// keep a leading minus.
func TestFormatNumberMatchesUpstream(t *testing.T) {
	cases := []struct {
		numberFormat string
		in           float64
		want         string
	}{
		{"point-comma", 1234.5, "1.234,50"},
		{"comma-point", 1234.5, "1,234.50"},
		{"space-point", 1234.5, "1 234.50"},
		{"space-comma", 1234.5, "1 234,50"},
		{"apostrophe-point", 1234.5, "1'234.50"},
		{"point-comma", -1234.5, "-1.234,50"},
		{"comma-point", 0, "0.00"},
		{"comma-point", 180, "180.00"},
		{"comma-point", 1234567.891, "1,234,567.89"},
	}
	for _, c := range cases {
		got := locWith(c.numberFormat, "", "", "", "", "EUR").formatNumber(c.in)
		if got != c.want {
			t.Errorf("%s %v: got %q want %q", c.numberFormat, c.in, got, c.want)
		}
	}
}

// Every CurrencyFormat case from the upstream enum.
func TestFormatCurrencyMatchesUpstream(t *testing.T) {
	cases := []struct{ format, want string }{
		{"iso-code-after-with-space", "1.234,50 EUR"},
		{"iso-code-before-with-space", "EUR 1.234,50"},
		{"symbol-after", "1.234,50€"},
		{"symbol-before", "€1.234,50"},
		{"symbol-before-with-space", "€ 1.234,50"},
		{"symbol-after-with-space", "1.234,50 €"},
	}
	for _, c := range cases {
		got := locWith("point-comma", c.format, "", "", "", "EUR").formatCurrency(123450)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.format, got, c.want)
		}
	}
}

// Upstream DateFormat::toCarbonFormat.
func TestFormatDateMatchesUpstream(t *testing.T) {
	d := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"point-separated-d-m-yyyy":     "7.3.2026",
		"slash-separated-mm-dd-yyyy":   "03/07/2026",
		"slash-separated-dd-mm-yyyy":   "07/03/2026",
		"hyphen-separated-dd-mm-yyyy":  "07-03-2026",
		"hyphen-separated-mm-dd-yyyy":  "03-07-2026",
		"hyphen-separated-yyyy-mm-dd":  "2026-03-07",
	}
	for format, want := range cases {
		got := locWith("", "", format, "", "", "EUR").formatDate(d)
		if got != want {
			t.Errorf("%s: got %q want %q", format, got, want)
		}
	}
}

// Upstream TimeFormat: 'H:i' and 'h:i a' (zero-padded hour, lowercase meridiem).
func TestFormatTimeMatchesUpstream(t *testing.T) {
	am := time.Date(2026, 3, 7, 1, 2, 0, 0, time.UTC)
	pm := time.Date(2026, 3, 7, 23, 1, 0, 0, time.UTC)
	twelve := locWith("", "", "", "", "12-hours", "EUR")
	if got := twelve.formatTime(am); got != "01:02 am" {
		t.Errorf("12h am: got %q", got)
	}
	if got := twelve.formatTime(pm); got != "11:01 pm" {
		t.Errorf("12h pm: got %q", got)
	}
	tf := locWith("", "", "", "", "24-hours", "EUR")
	if got := tf.formatTime(pm); got != "23:01" {
		t.Errorf("24h: got %q", got)
	}
}

// Upstream LocalizationService::formatInterval, all four formats.
func TestFormatIntervalMatchesUpstream(t *testing.T) {
	// 3h 45m 30s
	seconds := int64(3*3600 + 45*60 + 30)
	cases := map[string]string{
		"decimal":                               "3,76 h",
		"hours-minutes":                         "3h 45m",
		"hours-minutes-colon-separated":         "3:45",
		"hours-minutes-seconds-colon-separated": "3:45:30",
	}
	for format, want := range cases {
		got := locWith("point-comma", "", "", format, "", "EUR").formatInterval(seconds)
		if got != want {
			t.Errorf("%s: got %q want %q", format, got, want)
		}
	}
}

// formatIntervalForReporting promotes the two verbose formats to H:MM:SS and
// leaves the others alone.
func TestFormatIntervalForReportingPromotion(t *testing.T) {
	seconds := int64(3*3600 + 45*60 + 30)
	promoted := map[string]string{
		"hours-minutes":                 "3:45:30",
		"hours-minutes-colon-separated": "3:45:30",
	}
	for format, want := range promoted {
		got := locWith("point-comma", "", "", format, "", "EUR").formatIntervalForReporting(seconds)
		if got != want {
			t.Errorf("promote %s: got %q want %q", format, got, want)
		}
	}
	if got := locWith("point-comma", "", "", "decimal", "", "EUR").formatIntervalForReporting(seconds); got != "3,76 h" {
		t.Errorf("decimal must not be promoted: %q", got)
	}
	if got := locWith("point-comma", "", "", "hours-minutes-seconds-colon-separated", "", "EUR").formatIntervalForReporting(seconds); got != "3:45:30" {
		t.Errorf("hms must stay: %q", got)
	}
}

// formatTimeGroupKey formats day/week/month keys and leaves year keys alone.
func TestFormatTimeGroupKey(t *testing.T) {
	l := locWith("", "", "hyphen-separated-yyyy-mm-dd", "", "", "EUR")
	key := func(s string) *string { return &s }

	if got := l.formatTimeGroupKey(nil, "day"); got != nil {
		t.Errorf("nil key: got %v", got)
	}
	if got := l.formatTimeGroupKey(key("2026-03-07"), "day"); got == nil || *got != "2026-03-07" {
		t.Errorf("day: got %v", got)
	}
	if got := l.formatTimeGroupKey(key("2026-03-02"), "week"); got == nil || *got != "2026-03-02 - 2026-03-08" {
		t.Errorf("week: got %v", got)
	}
	if got := l.formatTimeGroupKey(key("2026-03"), "month"); got == nil || *got != "March 2026" {
		t.Errorf("month: got %v", got)
	}
	if got := l.formatTimeGroupKey(key("2026"), "year"); got == nil || *got != "2026" {
		t.Errorf("year must pass through: got %v", got)
	}
}
