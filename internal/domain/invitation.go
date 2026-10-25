package domain

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// InviteUser ports InvitationService::inviteUser — checks, insert, and the
// invitation row back for the caller to mail. The email is lowercased to match
// UserService::createUser normalization.
func (s *Service) InviteUser(ctx context.Context, q *db.Queries, orgID pgtype.UUID, email, role string) (db.OrganizationInvitation, error) {
	email = strings.ToLower(email)
	isMember, err := s.IsEmailAlreadyMember(ctx, q, orgID, email)
	if err != nil {
		return db.OrganizationInvitation{}, err
	}
	if isMember {
		return db.OrganizationInvitation{}, ErrUserAlreadyMember
	}
	exists, err := q.InvitationExistsForEmail(ctx, db.InvitationExistsForEmailParams{
		OrganizationID: orgID, Email: email,
	})
	if err != nil {
		return db.OrganizationInvitation{}, err
	}
	if exists {
		return db.OrganizationInvitation{}, ErrInvitationAlreadyExists
	}
	return q.CreateInvitation(ctx, db.CreateInvitationParams{
		ID:             newUUID(),
		OrganizationID: orgID,
		Email:          email,
		Role:           ptr(role),
	})
}

// ProcessAcceptedInvitations ports InvitationService::processAcceptedInvitations:
// every invitation row accepted earlier (accept link clicked before the account
// existed) becomes a membership; returns the joined organization IDs.
// q must be transaction-bound.
func (s *Service) ProcessAcceptedInvitations(ctx context.Context, q *db.Queries, userID pgtype.UUID, email string) ([]pgtype.UUID, error) {
	invs, err := q.AcceptedInvitationsForEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	orgIDs := make([]pgtype.UUID, 0, len(invs))
	for _, inv := range invs {
		role := strOrEmpty(inv.Role)
		switch role {
		case RoleAdmin, RoleManager, RoleEmployee:
		default:
			continue // upstream logs and skips invalid roles
		}
		member := db.Member{ID: newUUID(), UserID: userID, OrganizationID: inv.OrganizationID, Role: ptr(role)}
		if err := q.CreateMember(ctx, db.CreateMemberParams{
			ID: member.ID, UserID: userID, OrganizationID: inv.OrganizationID, Role: ptr(role),
		}); err != nil {
			return nil, err
		}
		if err := q.SetUserCurrentOrganization(ctx, db.SetUserCurrentOrganizationParams{ID: userID, CurrentTeamID: inv.OrganizationID}); err != nil {
			return nil, err
		}
		if err := s.mergePlaceholderMembersIntoMember(ctx, q, member, inv.OrganizationID, userID); err != nil {
			return nil, err
		}
		if err := q.DeleteInvitation(ctx, inv.ID); err != nil {
			return nil, err
		}
		orgIDs = append(orgIDs, inv.OrganizationID)
	}
	return orgIDs, nil
}
