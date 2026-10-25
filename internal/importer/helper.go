package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Error mirrors upstream's ImportException: a user-facing import failure that
// is rendered as a 400 with the message verbatim.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func fail(format string, args ...any) *Error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// helper is the Go port of ImportDatabaseHelper with attachToExisting=true:
// existing rows are preloaded once, keyed by a JSON hash of their identifier
// values; getKey returns the match or creates the row. Models fetched via
// modelByID are remembered so the caller can later iterate "used" entities
// (upstream getCachedModels) for spent_time recalculation.
type helper[T any] struct {
	load   func(ctx context.Context) ([]T, error)
	ident  func(T) []any
	idOf   func(T) pgtype.UUID
	loaded bool
	byKey  map[string]pgtype.UUID
	byID   map[pgtype.UUID]T
	byExt  map[string]string
	used   []T
	usedIn map[pgtype.UUID]bool
	created int
}

func newHelper[T any](load func(context.Context) ([]T, error), ident func(T) []any, idOf func(T) pgtype.UUID) *helper[T] {
	return &helper[T]{
		load:   load,
		ident:  ident,
		idOf:   idOf,
		byKey:  map[string]pgtype.UUID{},
		byID:   map[pgtype.UUID]T{},
		byExt:  map[string]string{},
		usedIn: map[pgtype.UUID]bool{},
	}
}

// hash mirrors md5(json_encode($identifierData)): the key only needs to be
// unique and deterministic, so the raw JSON of the ordered values is used.
func hash(vals []any) string {
	b, err := json.Marshal(vals)
	if err != nil {
		panic("importer: identifier values not marshalable")
	}
	return string(b)
}

func uuidVal(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return uuidStr(id)
}

func (h *helper[T]) ensureLoaded(ctx context.Context) error {
	if h.loaded {
		return nil
	}
	rows, err := h.load(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		h.byKey[hash(h.ident(row))] = h.idOf(row)
		h.byID[h.idOf(row)] = row
	}
	h.loaded = true
	return nil
}

// getKey returns the id of the row matching ident, creating it via create
// when absent. ext registers an external identifier alias (empty = none).
func (h *helper[T]) getKey(ctx context.Context, ident []any, create func(context.Context) (T, error), ext string) (pgtype.UUID, error) {
	if err := h.ensureLoaded(ctx); err != nil {
		return pgtype.UUID{}, err
	}
	k := hash(ident)
	if id, ok := h.byKey[k]; ok {
		if ext != "" {
			h.byExt[ext] = k
		}
		return id, nil
	}
	row, err := create(ctx)
	if err != nil {
		return pgtype.UUID{}, err
	}
	id := h.idOf(row)
	h.byKey[k] = id
	h.byID[id] = row
	h.created++
	if ext != "" {
		h.byExt[ext] = k
	}
	return id, nil
}

func (h *helper[T]) keyByExternal(ext string) (pgtype.UUID, bool) {
	k, ok := h.byExt[ext]
	if !ok {
		return pgtype.UUID{}, false
	}
	id, ok := h.byKey[k]
	return id, ok
}

func (h *helper[T]) externalIDs() []string {
	out := make([]string, 0, len(h.byExt))
	for k := range h.byExt {
		out = append(out, k)
	}
	return out
}

// model resolves identifier data to the stored row (upstream getModel()).
func (h *helper[T]) model(ctx context.Context, ident []any) (T, bool, error) {
	var zero T
	if err := h.ensureLoaded(ctx); err != nil {
		return zero, false, err
	}
	id, ok := h.byKey[hash(ident)]
	if !ok {
		return zero, false, nil
	}
	row, ok := h.byID[id]
	return row, ok, nil
}

// modelByID returns a row by primary key and marks it "used"
// (upstream getModelById + getCachedModels semantics).
func (h *helper[T]) modelByID(id pgtype.UUID) (T, bool) {
	var zero T
	row, ok := h.byID[id]
	if !ok {
		return zero, false
	}
	if !h.usedIn[id] {
		h.usedIn[id] = true
		h.used = append(h.used, row)
	}
	return row, true
}

func (h *helper[T]) usedModels() []T { return h.used }

// ---- shared field validators (subset of the Laravel rules used upstream) ----

func requireNameMax(name string, max int) error {
	if name == "" || len(name) > max {
		return fail("Invalid data: The name field is required.")
	}
	return nil
}

func requireTimezone(tz string) error {
	if tz == "" {
		return fail("Invalid data: The timezone field is required.")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fail("Invalid data: The timezone field must be a valid timezone.")
	}
	return nil
}

// ---- misc ports ----

const taskNameMaxLength = 500

func checkTaskNameLength(name string) error {
	if len(name) > taskNameMaxLength {
		return fail("Task name (%q) is too long, maximum length is %d characters", name, taskNameMaxLength)
	}
	return nil
}

// colors mirrors ColorService::COLORS + getRandomColor.
var colors = []string{
	"#ef5350", "#ec407a", "#ab47bc", "#7e57c2", "#5c6bc0", "#42a5f5",
	"#29b6f6", "#26c6da", "#26a69a", "#66bb6a", "#9ccc65", "#d4e157",
	"#ffee58", "#ffca28", "#ffa726", "#ff7043", "#8d6e63", "#bdbdbd",
	"#78909c",
}

var validColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

var colorState = 0

func randomColor() string {
	c := colors[colorState%len(colors)]
	colorState++
	return c
}

// slug ports Str::slug closely enough for generated placeholder emails:
// NFD decomposition strips accents, then any run of non-alphanumerics
// becomes a single dash. (Laravel transliterates ø/ß/æ too — rare enough
// in names that the placeholder email difference is cosmetic.)
// jarvis: ceiling Laravel's portable-ascii table, upgrade if imports hit non-ASCII names.
func slug(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(out) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func uuidStr(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	b := id.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUID(s string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}
	}
	return id
}

// parseISO parses 'Y-m-d\TH:i:s\Z' in UTC (Carbon::createFromFormat(..., 'UTC')).
func parseISO(value string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04:05Z", value, time.UTC)
}

// parseLocal parses 'Y-m-d H:i:s' interpreted in the given timezone,
// returning the instant in UTC.
func parseLocal(layout, value, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// errMissing mirrors the generic upstream Exception that ImportService turns
// into "Unknown error" — kept lowercase to satisfy style checks.
func errMissing(msg string) error { return errors.New(strings.ToLower(msg[:1]) + msg[1:]) }
