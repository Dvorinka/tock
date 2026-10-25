// Package importer ports upstream app/Service/Import: keyed "attach to
// existing" helpers plus the ten CSV/ZIP importer engines. The import runs
// synchronously inside one DB transaction behind a per-organization advisory
// lock, mirroring ImportService's Cache::lock + DB::transaction.
package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Report mirrors ReportDto::toArray.
type Report struct {
	ClientsCreated     int
	ProjectsCreated    int
	TasksCreated       int
	TimeEntriesCreated int
	TagsCreated        int
	UsersCreated       int
}

// Map renders the upstream JSON shape.
func (r Report) Map() map[string]any {
	return map[string]any{
		"clients":      map[string]int{"created": r.ClientsCreated},
		"projects":     map[string]int{"created": r.ProjectsCreated},
		"tasks":        map[string]int{"created": r.TasksCreated},
		"time_entries": map[string]int{"created": r.TimeEntriesCreated},
		"tags":         map[string]int{"created": r.TagsCreated},
		"users":        map[string]int{"created": r.UsersCreated},
	}
}

// Service executes imports against the database.
type Service struct {
	pool       *pgxpool.Pool
	storageDir string
}

func New(pool *pgxpool.Pool, storageDir string) *Service {
	return &Service{pool: pool, storageDir: storageDir}
}

// ImporterKeys returns the known importer type keys.
func ImporterKeys() map[string]bool {
	return map[string]bool{
		"toggl_time_entries":     true,
		"toggl_data_importer":    true,
		"clockify_time_entries":  true,
		"clockify_projects":      true,
		"solidtime":              true,
		"harvest_projects":       true,
		"harvest_time_entries":   true,
		"harvest_clients":        true,
		"generic_projects":       true,
		"generic_time_entries":   true,
	}
}

// Import runs the named importer over data for the organization. The whole
// import is one transaction: any ImportException (or unexpected error) rolls
// back, matching upstream semantics.
func (s *Service) Import(ctx context.Context, org db.Organization, importerType string, data []byte, timezone string) (Report, error) {
	if s.storageDir != "" {
		name := fmt.Sprintf("%s-%s-%s", time.Now().Format("2006-01-02"), uuidStr(org.ID), uuid.Must(uuid.NewV7()).String())
		dir := filepath.Join(s.storageDir, "import")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name), data, 0o644)
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Report{}, fmt.Errorf("begin import tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := db.New(tx)
	locked, err := q.TryImportLock(ctx, "import:"+uuidStr(org.ID))
	if err != nil {
		return Report{}, fmt.Errorf("import lock: %w", err)
	}
	if !locked {
		return Report{}, &Error{msg: "Import is already in progress"}
	}

	b := newBase(ctx, q, org)
	switch importerType {
	case "toggl_time_entries":
		err = b.importTogglTimeEntries(ctx, data, timezone)
	case "toggl_data_importer":
		err = b.importTogglData(ctx, data, timezone)
	case "clockify_time_entries":
		err = b.importClockifyTimeEntries(ctx, data, timezone)
	case "clockify_projects":
		err = b.importClockifyProjects(ctx, data)
	case "solidtime":
		err = b.importSolidtime(ctx, data)
	case "harvest_projects":
		err = b.importHarvestProjects(ctx, data)
	case "harvest_time_entries":
		err = b.importHarvestTimeEntries(ctx, data, timezone)
	case "harvest_clients":
		err = b.importHarvestClients(ctx, data)
	case "generic_projects":
		err = b.importGenericProjects(ctx, data)
	case "generic_time_entries":
		err = b.importGenericTimeEntries(ctx, data)
	default:
		return Report{}, &Error{msg: "Invalid importer type"}
	}
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			return Report{}, ie
		}
		return Report{}, &Error{msg: "Unknown error"}
	}

	if err := tx.Commit(ctx); err != nil {
		return Report{}, fmt.Errorf("commit import: %w", err)
	}
	return Report{
		ClientsCreated:     b.clients.created,
		ProjectsCreated:    b.projects.created,
		TasksCreated:       b.tasks.created,
		TimeEntriesCreated: b.timeEntriesCreated,
		TagsCreated:        b.tags.created,
		UsersCreated:       b.users.created,
	}, nil
}

// readCSV parses a full CSV document (league/csv with header offset 0,
// comma delimiter, double-quote enclosure). The first record is the header.
func readCSV(data []byte) ([]string, []map[string]string, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("empty csv")
	}
	header := rows[0]
	records := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		rec := map[string]string{}
		for i, col := range header {
			if i < len(row) {
				rec[col] = row[i]
			} else {
				rec[col] = ""
			}
		}
		records = append(records, rec)
	}
	return header, records, nil
}

func validateHeader(header []string, required ...string) error {
	set := map[string]bool{}
	for _, h := range header {
		set[h] = true
	}
	for _, req := range required {
		if !set[req] {
			return fail("Invalid CSV header, missing field: %s", req)
		}
	}
	return nil
}

// newID returns a fresh UUIDv7 for imported rows (matches the rest of tock).
func newID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true}
}

func ts(t time.Time) pgtype.Timestamp {
	return pgtype.Timestamp{Time: t, Valid: true}
}

var _ = io.EOF
