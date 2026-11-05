package domain

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	// unused

	"github.com/Dvorinka/tock/internal/db"
)

// Regression for #208: entries spanning a period boundary ("between two
// days") must be counted by overlap, so the Last-7-Days window sum and the
// This-Week widget agree for the shared part of the period.
func TestChartsOverlapClipping(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	q := db.New(pool)
	ctx := context.Background()
	uid := newTestUser(t, pool, false)
	org, err := s.CreateOrganization(ctx, "Acme", uid, false, "")
	if err != nil {
		t.Fatal(err)
	}

	memberID := func() pgtype.UUID {
		var mid pgtype.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM members WHERE user_id = $1 AND organization_id = $2 LIMIT 1`, uid, org.ID).Scan(&mid); err != nil {
			t.Fatalf("lookup member: %v", err)
		}
		return mid
	}()

	mkEntry := func(t *testing.T, start, end *time.Time) {
		t.Helper()
		var endTs pgtype.Timestamp
		if end != nil {
			endTs = pgtype.Timestamp{Time: *end, Valid: true}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO time_entries (id, description, start, "end", type, user_id, organization_id, member_id)
			VALUES ($1, 'crossing entry', $2, $3, 'work', $4, $5, $6)`,
			pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true},
			pgtype.Timestamp{Time: *start, Valid: true}, endTs, uid, org.ID, memberID); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}

	// Monday-start week: entry starts Sunday 23:50, ends Monday 00:20.
	weekStart := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC) // Monday
	sun := weekStart.Add(-10 * time.Minute)                  // Sun 23:50
	mon := weekStart.Add(20 * time.Minute)                   // Mon 00:20
	mkEntry(t, &sun, &mon)

	periodEnd := weekStart.Add(24 * time.Hour).Add(-time.Second) // Mon 23:59:59
	start := pgtype.Timestamp{Time: weekStart, Valid: true}
	end := pgtype.Timestamp{Time: periodEnd, Valid: true}

	sum, err := q.TimeEntriesSumBetween(ctx, db.TimeEntriesSumBetweenParams{
		UserID: uid, OrganizationID: org.ID, Column3: start, Column4: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum != 20*60 {
		t.Fatalf("This Week total = %d s, want 1200 (only the in-period overlap)", sum)
	}

	rows, err := q.WeeklyProjectOverview(ctx, db.WeeklyProjectOverviewParams{
		UserID: uid, OrganizationID: org.ID, Column3: start, Column4: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Aggregate != 20*60 {
		t.Fatalf("WeeklyProjectOverview = %+v, want [{<nil> 1200}]", rows)
	}
	if rows[0].ProjectID.Valid {
		t.Fatalf("expected the No-project bucket, got project %s", rows[0].ProjectID)
	}
}
