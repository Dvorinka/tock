package domain

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// DeleteUser ports DeletionService::deleteUser: owner-of-multi-member-org is a
// 400; owner memberships delete their organization, other memberships turn into
// placeholders; OAuth tokens/codes go before the user row.
func (s *Service) DeleteUser(ctx context.Context, userID pgtype.UUID) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		memberships, err := q.ListUserMemberships(ctx, userID)
		if err != nil {
			return err
		}
		for _, m := range memberships {
			if strOrEmpty(m.Role) != RoleOwner {
				continue
			}
			n, err := q.CountNonPlaceholderMembers(ctx, m.OrganizationID)
			if err != nil {
				return err
			}
			if n > 1 {
				return ErrOwnerWithMultipleMembers
			}
		}
		for _, m := range memberships {
			if strOrEmpty(m.Role) == RoleOwner {
				if err := s.deleteOrganizationQ(ctx, q, m.OrganizationID, userID); err != nil {
					return err
				}
			} else {
				member := db.Member{ID: m.MemberID, OrganizationID: m.OrganizationID, UserID: userID, Role: m.Role}
				if err := s.MakeMemberToPlaceholder(ctx, q, &member, false); err != nil {
					return err
				}
			}
		}
		if err := q.DeleteRefreshTokensForUser(ctx, userID); err != nil {
			return err
		}
		if err := q.DeleteAccessTokensForUser(ctx, userID); err != nil {
			return err
		}
		if err := q.DeleteAuthCodesForUser(ctx, userID); err != nil {
			return err
		}
		// shortcut: profile photo file deletion — upstream removes it from the
		// storage disk; tock has no storage layer yet (Phase 6/7). The row dies
		// with the user, so only the file itself may linger.
		return q.DeleteUserByID(ctx, userID)
	})
}

// deleteOrganizationQ is DeleteOrganization's body for callers already inside
// a transaction (deleteUser runs the whole cascade in one tx, like upstream).
func (s *Service) deleteOrganizationQ(ctx context.Context, q *db.Queries, orgID, ignoreUserID pgtype.UUID) error {
	steps := []func(context.Context, pgtype.UUID) error{
		q.DeleteInvitationsForOrg,
		q.DeleteTimeEntriesForOrg,
		q.DeleteTagsForOrg,
		q.DeleteTasksForOrg,
		q.DeleteProjectMembersForOrg,
		q.DeleteProjectsForOrg,
		q.DeleteClientsForOrg,
		q.DeleteReportsForOrg,
		q.DeleteGoalsForOrg,
	}
	for _, step := range steps {
		if err := step(ctx, orgID); err != nil {
			return err
		}
	}
	if err := q.ClearCurrentTeamForOrg(ctx, orgID); err != nil {
		return err
	}
	members, err := q.ListMembersForOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if err := q.DeleteMembersForOrg(ctx, orgID); err != nil {
		return err
	}
	for _, m := range members {
		if ignoreUserID.Valid && m.UserID == ignoreUserID {
			continue
		}
		if m.IsPlaceholder {
			if err := q.DeleteUserByID(ctx, m.UserID); err != nil {
				return err
			}
			continue
		}
		name, err := s.userName(ctx, q, m.UserID)
		if err != nil {
			return err
		}
		if err := s.MakeSureUserHasAtLeastOneOrganization(ctx, q, m.UserID, name); err != nil {
			return err
		}
		if err := s.MakeSureUserHasCurrentOrganization(ctx, q, m.UserID, pgtype.UUID{}); err != nil {
			return err
		}
	}
	return q.DeleteOrganizationRow(ctx, orgID)
}
