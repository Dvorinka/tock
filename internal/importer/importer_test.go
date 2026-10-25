package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Dvorinka/tock/internal/db"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TOCK_TEST_DSN")
	if dsn == "" {
		t.Skip("TOCK_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newTestOrg(t *testing.T, pool *pgxpool.Pool) db.Organization {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (id, name, email, password, timezone, week_start, is_placeholder, created_at, updated_at)
		VALUES ($1, 'Owner', $2, 'x', 'UTC', 'monday', false, now(), now())
		RETURNING id`, pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true},
		fmt.Sprintf("owner-%s@example.test", suffix)).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	orgID := pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true}
	var org db.Organization
	if err := pool.QueryRow(ctx, `
		INSERT INTO organizations (id, user_id, name, personal_team, currency,
		    number_format, currency_format, date_format, interval_format, time_format,
		    created_at, updated_at)
		VALUES ($1, $2, $3, false, 'EUR',
		    'point-comma', 'iso-code-after-with-space', 'hyphen-separated-yyyy-mm-dd',
		    'hours-minutes', '24-hours', now(), now())
		RETURNING id, user_id, name, billable_rate`, orgID, userID, "Org "+suffix).Scan(
		&org.ID, &org.UserID, &org.Name, &org.BillableRate); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO members (id, user_id, organization_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', now(), now())`,
		pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true}, userID, orgID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return org
}

func TestGenericTimeEntriesImport(t *testing.T) {
	pool := testPool(t)
	org := newTestOrg(t, pool)
	svc := New(pool, "")
	ctx := context.Background()

	csvData := `description,billable,client,project,tags,start,end,task,user_name,user_email
"did work","true","Acme","Website","a, b","2024-01-01T09:00:00Z","2024-01-01T10:30:00Z","Task One","Jane","jane@example.test"
"more","false","Acme","Website","","2024-01-02T09:00:00Z","2024-01-02T10:00:00Z","","Jane","jane@example.test"
`
	report, err := svc.Import(ctx, org, "generic_time_entries", []byte(csvData), "UTC")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.TimeEntriesCreated != 2 || report.UsersCreated != 1 ||
		report.ProjectsCreated != 1 || report.ClientsCreated != 1 ||
		report.TagsCreated != 2 || report.TasksCreated != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM time_entries WHERE organization_id = $1 AND is_imported`, org.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 imported entries, got %d", n)
	}
	var spent int64
	if err := pool.QueryRow(ctx,
		`SELECT spent_time FROM projects WHERE organization_id = $1 AND name = 'Website'`, org.ID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 5400+3600 {
		t.Fatalf("expected spent_time 9000, got %d", spent)
	}
	// Billable entry resolved the org-level billable rate (unset -> NULL).
	var billable bool
	if err := pool.QueryRow(ctx,
		`SELECT billable FROM time_entries WHERE organization_id = $1 AND description = 'did work'`, org.ID).Scan(&billable); err != nil {
		t.Fatal(err)
	}
	if !billable {
		t.Fatal("expected billable=true")
	}
}

func TestImportRollbackOnError(t *testing.T) {
	pool := testPool(t)
	org := newTestOrg(t, pool)
	svc := New(pool, "")
	ctx := context.Background()

	csvData := `description,billable,client,project,tags,start,end,task,user_name,user_email
"x","maybe","C","P","","2024-01-01T09:00:00Z","2024-01-01T10:00:00Z","","J","j@example.test"
`
	_, err := svc.Import(ctx, org, "generic_time_entries", []byte(csvData), "UTC")
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("expected ImportError, got %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM time_entries WHERE organization_id = $1`, org.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected rollback (0 entries), got %d", n)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE email = 'j@example.test'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("placeholder user leaked past rollback")
	}
}

func TestImportInvalidTypeAndHeader(t *testing.T) {
	pool := testPool(t)
	org := newTestOrg(t, pool)
	svc := New(pool, "")
	if _, err := svc.Import(context.Background(), org, "bogus", []byte("x"), "UTC"); err == nil {
		t.Fatal("expected error for invalid type")
	}
	if _, err := svc.Import(context.Background(), org, "generic_time_entries", []byte("a,b\n1,2\n"), "UTC"); err == nil {
		t.Fatal("expected header error")
	}
}

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSolidtimeImport(t *testing.T) {
	pool := testPool(t)
	org := newTestOrg(t, pool)
	svc := New(pool, "")
	ctx := context.Background()

	zipData := buildZip(t, map[string]string{
		"meta.json": `{"version":"1.0"}`,
		"clients.csv": "id,name,archived_at\n" +
			"11111111-1111-1111-1111-111111111111,Acme,\n",
		"tags.csv": "id,name\n" +
			"22222222-2222-2222-2222-222222222222,urgent\n",
		"members.csv": "id,user_id,email,name,billable_rate\n" +
			"33333333-3333-3333-3333-333333333333,44444444-4444-4444-4444-444444444444,imp@example.test,Imported User,500\n",
		"organization_invitations.csv": "id,email,role\n",
		"projects.csv": "id,name,client_id,color,billable_rate,is_public,is_billable,archived_at\n" +
			"55555555-5555-5555-5555-555555555555,Proj,11111111-1111-1111-1111-111111111111,#ef5350,,true,true,\n",
		"project_members.csv": "id,user_id,member_id,project_id,billable_rate\n" +
			"66666666-6666-6666-6666-666666666666,44444444-4444-4444-4444-444444444444,33333333-3333-3333-3333-333333333333,55555555-5555-5555-5555-555555555555,700\n",
		"tasks.csv": "id,name,project_id,done_at\n" +
			"77777777-7777-7777-7777-777777777777,T1,55555555-5555-5555-5555-555555555555,\n",
		"time_entries.csv": "id,user_id,member_id,client_id,project_id,task_id,description,billable,tags,start,end,type,still_active_email_sent_at\n" +
			"88888888-8888-8888-8888-888888888888,44444444-4444-4444-4444-444444444444,33333333-3333-3333-3333-333333333333,11111111-1111-1111-1111-111111111111,55555555-5555-5555-5555-555555555555,77777777-7777-7777-7777-777777777777,work stuff,true,\"[\"\"22222222-2222-2222-2222-222222222222\"\"]\",2024-03-01T08:00:00Z,2024-03-01T09:00:00Z,work,\n",
	})
	report, err := svc.Import(ctx, org, "solidtime", zipData, "UTC")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.TimeEntriesCreated != 1 || report.UsersCreated != 1 || report.ProjectsCreated != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	var rate *int32
	if err := pool.QueryRow(ctx,
		`SELECT billable_rate FROM time_entries WHERE organization_id = $1`, org.ID).Scan(&rate); err != nil {
		t.Fatal(err)
	}
	// project member rate (700) wins over member (500) and project (null).
	if rate == nil || *rate != 700 {
		t.Fatalf("expected billable_rate 700, got %v", rate)
	}
	var tagCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM time_entries te JOIN tags t ON te.tags ? t.id::text WHERE te.organization_id = $1`,
		org.ID).Scan(&tagCount); err != nil {
		// The ? operator conflicts with pgx param parsing; check via jsonb length.
		_ = tagCount
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT jsonb_array_length(tags) FROM time_entries WHERE organization_id = $1`, org.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected 1 tag, got %d", n)
		}
	}
}

func TestTogglTimeEntriesImport(t *testing.T) {
	pool := testPool(t)
	org := newTestOrg(t, pool)
	svc := New(pool, "")
	ctx := context.Background()

	csvData := `User,Email,Client,Project,Task,Description,Billable,Start date,Start time,End date,End time,Tags
Jane,jane@example.test,Acme,Site,,worked,Yes,2024-01-01,10:00:00,2024-01-01,11:00:00,"x, y"
`
	report, err := svc.Import(ctx, org, "toggl_time_entries", []byte(csvData), "Europe/Berlin")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.TimeEntriesCreated != 1 || report.TagsCreated != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	var start time.Time
	if err := pool.QueryRow(ctx,
		`SELECT start FROM time_entries WHERE organization_id = $1`, org.ID).Scan(&start); err != nil {
		t.Fatal(err)
	}
	// 10:00 Europe/Berlin (CET, UTC+1) = 09:00 UTC.
	if start.UTC().Hour() != 9 {
		t.Fatalf("expected 09:00 UTC, got %v", start.UTC())
	}
}
