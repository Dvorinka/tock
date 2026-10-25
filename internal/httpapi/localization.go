package httpapi

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// localization ports LocalizationService — formatting driven by the
// organization's number/currency/date/interval/time format columns.
type localization struct {
	numberFormat   string
	currencyFormat string
	dateFormat     string
	intervalFormat string
	timeFormat     string
	currency       string
}

func localizationFor(org db.Organization) localization {
	return localization{
		numberFormat:   org.NumberFormat,
		currencyFormat: org.CurrencyFormat,
		dateFormat:     org.DateFormat,
		intervalFormat: org.IntervalFormat,
		timeFormat:     org.TimeFormat,
		currency:       org.Currency,
	}
}

// numberFormatSep returns (decimal, thousands) separators for number_format.
func (l localization) numberFormatSep() (dec, thou byte) {
	switch l.numberFormat {
	case "point-comma":
		return ',', '.'
	case "space-point":
		return '.', ' '
	case "space-comma":
		return ',', ' '
	case "apostrophe-point":
		return '.', '\''
	default: // comma-point
		return '.', ','
	}
}

// formatNumber ports LocalizationService::formatNumber — always 2 decimals.
func (l localization) formatNumber(f float64) string {
	dec, thou := l.numberFormatSep()
	s := strconv.FormatFloat(math.Abs(f), 'f', 2, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(thou)
		}
		b.WriteRune(c)
	}
	if f < 0 {
		return "-" + b.String() + string(dec) + frac
	}
	return b.String() + string(dec) + frac
}

// formatInterval ports LocalizationService::formatInterval (cascade semantics).
func (l localization) formatInterval(seconds int64) string {
	hours := float64(seconds) / 3600
	whole := seconds / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60
	switch l.intervalFormat {
	case "decimal":
		return l.formatNumber(hours) + " h"
	case "hours-minutes":
		return fmt.Sprintf("%dh %02dm", whole, mins)
	case "hours-minutes-colon-separated":
		return fmt.Sprintf("%d:%02d", whole, mins)
	default: // hours-minutes-seconds-colon-separated
		return fmt.Sprintf("%d:%02d:%02d", whole, mins, secs)
	}
}

// formatIntervalForReporting promotes verbose formats to HH:MM:SS.
func (l localization) formatIntervalForReporting(seconds int64) string {
	if l.intervalFormat == "hours-minutes" || l.intervalFormat == "hours-minutes-colon-separated" {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
	}
	return l.formatInterval(seconds)
}

// formatCurrency ports LocalizationService::formatCurrency; minorUnits is the
// integer minor-unit amount (BigDecimal::ofUnscaledValue(_, 2)).
func (l localization) formatCurrency(minorUnits int64) string {
	amount := l.formatNumber(float64(minorUnits) / 100)
	symbol := domain.CurrencySymbol(l.currency)
	switch l.currencyFormat {
	case "iso-code-after-with-space":
		return amount + " " + l.currency
	case "iso-code-before-with-space":
		return l.currency + " " + amount
	case "symbol-after":
		return amount + symbol
	case "symbol-before":
		return symbol + amount
	case "symbol-after-with-space":
		return amount + " " + symbol
	default: // symbol-before-with-space
		return symbol + " " + amount
	}
}

// formatDate ports LocalizationService::formatDate (DateFormat::toCarbonFormat).
func (l localization) formatDate(t time.Time) string {
	y, m, d := t.Date()
	yStr := fmt.Sprintf("%d", y)
	switch l.dateFormat {
	case "point-separated-d-m-yyyy": // j.n.Y
		return fmt.Sprintf("%d.%d.%s", d, int(m), yStr)
	case "slash-separated-mm-dd-yyyy": // m/d/Y
		return fmt.Sprintf("%02d/%02d/%s", int(m), d, yStr)
	case "slash-separated-dd-mm-yyyy": // d/m/Y
		return fmt.Sprintf("%02d/%02d/%s", d, int(m), yStr)
	case "hyphen-separated-dd-mm-yyyy": // d-m-Y
		return fmt.Sprintf("%02d-%02d-%s", d, int(m), yStr)
	case "hyphen-separated-mm-dd-yyyy": // m-d-Y
		return fmt.Sprintf("%02d-%02d-%s", int(m), d, yStr)
	default: // hyphen-separated-yyyy-mm-dd → Y-m-d
		return fmt.Sprintf("%s-%02d-%02d", yStr, int(m), d)
	}
}

// formatTime ports LocalizationService::formatTime.
func (l localization) formatTime(t time.Time) string {
	if l.timeFormat == "12-hours" {
		// Upstream 'h:i a' — zero-padded hour, lowercase meridiem.
		return t.Format("03:04 pm")
	}
	return t.Format("15:04")
}

// formatTimeGroupKey ports LocalizationService::formatTimeGroupKey.
func (l localization) formatTimeGroupKey(key *string, groupType string) *string {
	if key == nil {
		return nil
	}
	out := *key
	switch groupType {
	case "day":
		if t, err := time.Parse("2006-01-02", *key); err == nil {
			out = l.formatDate(t)
		}
	case "week":
		if t, err := time.Parse("2006-01-02", *key); err == nil {
			out = l.formatDate(t) + " - " + l.formatDate(t.AddDate(0, 0, 6))
		}
	case "month":
		if t, err := time.Parse("2006-01", *key); err == nil {
			out = t.Format("January 2006")
		}
	}
	return &out
}
