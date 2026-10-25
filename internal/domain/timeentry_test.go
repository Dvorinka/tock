package domain

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// seedTimeEntryEnv builds org + owner member + project and returns the ids.
func seedTimeEntryEnv(t *testing.T, s *Service) (ctx context.Context, org db.Organization, member db.Member, project db.Project) {
	t.Helper()
	ctx = context.Background()
	q := db.New(s.pool)
	owner := newTestUser(t, s.pool, false)
	org, err := s.CreateOrganization(ctx, "TE Org", owner, false, "")
	if err != nil {
		t.Fatal(err)
	}
	member, err = q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})
	if err != nil {
		t.Fatal(err)
	}
	project, err = q.InsertProject(ctx, db.InsertProjectParams{
		ID: newUUID(), Name: "P1", Color: "#aabbcc", IsPublic: true,
		OrganizationID: org.ID, IsBillable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, org, member, project
}

func insertEntry(t *testing.T, s *Service, org db.Organization, member db.Member, project pgtype.UUID, start, end time.Time, billable bool) db.TimeEntry {
	t.Helper()
	entry, err := db.New(s.pool).InsertTimeEntry(context.Background(), db.InsertTimeEntryParams{
		ID: newUUID(), Description: "e", Start: pgtype.Timestamp{Time: start, Valid: true},
		End: pgtype.Timestamp{Time: end, Valid: end.Year() > 1},
		UserID: member.UserID, OrganizationID: org.ID, MemberID: member.ID,
		ProjectID: project, Billable: billable, Type: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestBillableRatePrecedence(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx, org, member, project := seedTimeEntryEnv(t, s)
	q := db.New(pool)

	// No rates anywhere → null.
	rate, err := BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, true)
	if err != nil || rate != nil {
		t.Fatalf("expected nil rate, got %v err=%v", rate, err)
	}
	// Org rate applies when nothing else set.
	if _, err := pool.Exec(ctx, `UPDATE organizations SET billable_rate = 1000 WHERE id = $1`, org.ID); err != nil {
		t.Fatal(err)
	}
	rate, _ = BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, true)
	if rate == nil || *rate != 1000 {
		t.Fatalf("expected org rate 1000, got %v", rate)
	}
	// Member rate beats org.
	if _, err := pool.Exec(ctx, `UPDATE members SET billable_rate = 2000 WHERE id = $1`, member.ID); err != nil {
		t.Fatal(err)
	}
	rate, _ = BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, true)
	if rate == nil || *rate != 2000 {
		t.Fatalf("expected member rate 2000, got %v", rate)
	}
	// Project rate beats member.
	if _, err := pool.Exec(ctx, `UPDATE projects SET billable_rate = 3000 WHERE id = $1`, project.ID); err != nil {
		t.Fatal(err)
	}
	rate, _ = BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, true)
	if rate == nil || *rate != 3000 {
		t.Fatalf("expected project rate 3000, got %v", rate)
	}
	// Project-member rate beats all.
	if _, err := pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, member_id, billable_rate, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 4000, now(), now())`, newUUID(), project.ID, member.UserID, member.ID); err != nil {
		t.Fatal(err)
	}
	rate, _ = BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, true)
	if rate == nil || *rate != 4000 {
		t.Fatalf("expected project-member rate 4000, got %v", rate)
	}
	// Non-billable → nil regardless.
	rate, _ = BillableRateForTimeEntry(ctx, q, org.ID, member.UserID, project.ID, false)
	if rate != nil {
		t.Fatalf("expected nil for non-billable, got %v", rate)
	}
}

func TestAssertNoOverlap(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx, org, member, project := seedTimeEntryEnv(t, s)
	q := db.New(pool)

	start := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	insertEntry(t, s, org, member, project.ID, start, end, false)

	// Disabled by default → no error even with a real overlap.
	if err := AssertNoOverlap(ctx, q, org, member.ID,
		pgtype.Timestamp{Time: start.Add(30 * time.Minute), Valid: true},
		pgtype.Timestamp{Time: end.Add(time.Hour), Valid: true}, pgtype.UUID{}); err != nil {
		t.Fatalf("overlap should pass when disabled: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET prevent_overlapping_time_entries = true WHERE id = $1`, org.ID); err != nil {
		t.Fatal(err)
	}
	org.PreventOverlappingTimeEntries = true

	cases := []struct {
		name      string
		start     time.Time
		end       time.Time
		wantError bool
	}{
		{"inside", start.Add(10 * time.Minute), start.Add(20 * time.Minute), true},
		{"left-overlap", start.Add(-30 * time.Minute), start.Add(30 * time.Minute), true},
		{"right-overlap", start.Add(30 * time.Minute), end.Add(30 * time.Minute), true},
		{"surrounds", start.Add(-time.Hour), end.Add(time.Hour), true},
		{"touching-end", end, end.Add(time.Hour), false},
		{"after", end.Add(time.Hour), end.Add(2 * time.Hour), false},
	}
	for _, c := range cases {
		err := AssertNoOverlap(ctx, q, org, member.ID,
			pgtype.Timestamp{Time: c.start, Valid: true},
			pgtype.Timestamp{Time: c.end, Valid: true}, pgtype.UUID{})
		if c.wantError && err == nil {
			t.Fatalf("%s: expected overlap error", c.name)
		}
		if !c.wantError && err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
	}
	// Excluding self must not self-collide.
	entry := insertEntry(t, s, org, member, project.ID, end.Add(3*time.Hour), end.Add(4*time.Hour), false)
	if err := AssertNoOverlap(ctx, q, org, member.ID,
		pgtype.Timestamp{Time: entry.Start.Time, Valid: true},
		pgtype.Timestamp{Time: entry.End.Time, Valid: true}, entry.ID); err != nil {
		t.Fatalf("self-exclusion failed: %v", err)
	}
}

func TestRecalculateSpentTime(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx, org, member, project := seedTimeEntryEnv(t, s)
	q := db.New(pool)

	insertEntry(t, s, org, member, project.ID,
		time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), false)
	// Running entry contributes nothing.
	insertEntry(t, s, org, member, project.ID,
		time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC), time.Time{}, false)
	if err := RecalculateSpentTime(ctx, q, project.ID, pgtype.UUID{}); err != nil {
		t.Fatal(err)
	}
	p, err := q.GetProjectByID(ctx, project.ID)
	if err != nil || p.SpentTime != 7200 {
		t.Fatalf("expected 7200 spent, got %v err=%v", p.SpentTime, err)
	}
}

func TestRunningTimeEntryUnique(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx, org, member, project := seedTimeEntryEnv(t, s)
	q := db.New(pool)
	insertEntry(t, s, org, member, project.ID,
		time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), time.Time{}, false)
	n, err := q.CountRunningTimeEntriesForUser(ctx, member.UserID)
	if err != nil || n != 1 {
		t.Fatalf("expected 1 running, got %v err=%v", n, err)
	}
}
