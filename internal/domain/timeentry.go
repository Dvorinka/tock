package domain

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// BillableRateForTimeEntry resolves the effective billable rate for a
// time entry (project member > project > member > organization).
// Port of BillableRateService::getBillableRateForTimeEntry — non-billable
// entries always get a null rate.
func BillableRateForTimeEntry(ctx context.Context, q *db.Queries, orgID, userID, projectID pgtype.UUID, billable bool) (*int32, error) {
	if !billable {
		return nil, nil
	}
	v, err := q.MemberBillableRateForTimeEntry(ctx, db.MemberBillableRateForTimeEntryParams{
		OrganizationID: orgID,
		UserID:         userID,
		ProjectID:      projectID,
	})
	if err != nil || v == nil {
		return nil, err
	}
	switch r := v.(type) {
	case int32:
		return &r, nil
	case int64:
		v32 := int32(r)
		return &v32, nil
	default:
		return nil, err
	}
}

// AssertNoOverlap ports TimeEntryController::assertNoOverlap. The check only
// applies when organization.prevent_overlapping_time_entries is enabled.
func AssertNoOverlap(ctx context.Context, q *db.Queries, org db.Organization, memberID pgtype.UUID, start, end pgtype.Timestamp, excludeID pgtype.UUID) error {
	if !org.PreventOverlappingTimeEntries {
		return nil
	}
	exists, err := q.OverlappingTimeEntryExists(ctx, db.OverlappingTimeEntryExistsParams{
		OrganizationID: org.ID,
		MemberID:       memberID,
		Column3:        excludeID,
		End:            start,
		Column5:        end,
	})
	if err != nil {
		return err
	}
	if exists {
		return ErrOverlappingTimeEntry
	}
	return nil
}

// RecalculateSpentTime ports the RecalculateSpentTimeForProject/Task jobs —
// executed inline instead of dispatched (tock runs jobs in-process; the
// recalculation is a single UPDATE).
func RecalculateSpentTime(ctx context.Context, q *db.Queries, projectID, taskID pgtype.UUID) error {
	if projectID.Valid {
		if err := q.RecalculateSpentTimeForProject(ctx, projectID); err != nil {
			return err
		}
	}
	if taskID.Valid {
		if err := q.RecalculateSpentTimeForTask(ctx, taskID); err != nil {
			return err
		}
	}
	return nil
}
