package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// createOrganizationQ ports OrganizationService::createOrganization — a row in
// organizations plus an owner membership. Defaults mirror
// config('app.localization.default_*') = EUR / point-comma / etc.
// q must be transaction-bound.
func createOrganizationQ(ctx context.Context, q *db.Queries, name string, owner pgtype.UUID, personal bool, currency string) (db.Organization, error) {
	if currency == "" {
		currency = "EUR"
	}
	org, err := q.InsertOrganization(ctx, db.InsertOrganizationParams{
		ID:           newUUID(),
		UserID:       owner,
		Name:         name,
		PersonalTeam: personal,
		Currency:     currency,
	})
	if err != nil {
		return db.Organization{}, fmt.Errorf("insert organization: %w", err)
	}
	if err := q.CreateMember(ctx, db.CreateMemberParams{
		ID:             newUUID(),
		UserID:         owner,
		OrganizationID: org.ID,
		Role:           ptr(RoleOwner),
	}); err != nil {
		return db.Organization{}, err
	}
	return org, nil
}

// CreateOrganization is the tx-wrapped entry point (API store handler).
func (s *Service) CreateOrganization(ctx context.Context, name string, owner pgtype.UUID, personal bool, currency string) (db.Organization, error) {
	var org db.Organization
	err := s.withTx(ctx, func(q *db.Queries) error {
		var err error
		org, err = createOrganizationQ(ctx, q, name, owner, personal, currency)
		return err
	})
	return org, err
}

// SwitchCurrentOrganization ports UserService::switchCurrentOrganization.
func (s *Service) SwitchCurrentOrganization(ctx context.Context, q *db.Queries, userID, orgID pgtype.UUID) error {
	return q.SetUserCurrentOrganization(ctx, db.SetUserCurrentOrganizationParams{ID: userID, CurrentTeamID: orgID})
}

// organizationNameForUser mirrors UserService::getOrganizationNameForUserName
// (explode(' ', $username, 2)[0] — first word).
func organizationNameForUser(userName string) string {
	first, _, _ := strings.Cut(userName, " ")
	return first + "'s Organization"
}

// MakeSureUserHasAtLeastOneOrganization ports the UserService guard: a user
// with zero memberships gets a fresh personal organization set as current.
// q must be transaction-bound.
func (s *Service) MakeSureUserHasAtLeastOneOrganization(ctx context.Context, q *db.Queries, userID pgtype.UUID, userName string) error {
	n, err := q.CountUserMemberships(ctx, userID)
	if err != nil || n > 0 {
		return err
	}
	org, err := createOrganizationQ(ctx, q, organizationNameForUser(userName), userID, true, "")
	if err != nil {
		return err
	}
	return q.SetUserCurrentOrganization(ctx, db.SetUserCurrentOrganizationParams{ID: userID, CurrentTeamID: org.ID})
}

// MakeSureUserHasCurrentOrganization ports the UserService guard: users with
// a null current_team_id get their first membership assigned.
// q must be transaction-bound.
func (s *Service) MakeSureUserHasCurrentOrganization(ctx context.Context, q *db.Queries, userID pgtype.UUID, currentTeamID pgtype.UUID) error {
	if currentTeamID.Valid {
		return nil
	}
	orgID, err := q.FirstUserOrganization(ctx, userID)
	if err != nil {
		return nil // no memberships left — stays null, like upstream first() === null
	}
	return q.SetUserCurrentOrganization(ctx, db.SetUserCurrentOrganizationParams{ID: userID, CurrentTeamID: orgID})
}

// DeleteOrganization ports DeletionService::deleteOrganization verbatim.
// ignoreUserID mirrors $ignoreUser — that user's post-delete fixups are skipped.
func (s *Service) DeleteOrganization(ctx context.Context, orgID, ignoreUserID pgtype.UUID) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		return s.deleteOrganizationQ(ctx, q, orgID, ignoreUserID)
	})
}

func (s *Service) userName(ctx context.Context, q *db.Queries, id pgtype.UUID) (string, error) {
	u, err := q.GetUserByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Name, nil
}

func ptr[T any](v T) *T { return &v }
