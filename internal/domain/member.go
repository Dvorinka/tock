package domain

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// AddMember ports MemberService::addMember: member row + current-org switch +
// mergePlaceholderMembersIntoExistingMember. Runs in a transaction.
func (s *Service) AddMember(ctx context.Context, userID, orgID pgtype.UUID, role string) (db.Member, error) {
	var member db.Member
	err := s.withTx(ctx, func(q *db.Queries) error {
		member = db.Member{ID: newUUID(), UserID: userID, OrganizationID: orgID, Role: ptr(role)}
		if err := q.CreateMember(ctx, db.CreateMemberParams{
			ID: member.ID, UserID: userID, OrganizationID: orgID, Role: ptr(role),
		}); err != nil {
			return err
		}
		if err := q.SetUserCurrentOrganization(ctx, db.SetUserCurrentOrganizationParams{ID: userID, CurrentTeamID: orgID}); err != nil {
			return err
		}
		return s.mergePlaceholderMembersIntoMember(ctx, q, member, orgID, userID)
	})
	return member, err
}

// mergePlaceholderMembersIntoMember ports MemberService::
// mergePlaceholderMembersIntoExistingMember — placeholder users sharing the
// new member's email in this org are folded into the real membership.
func (s *Service) mergePlaceholderMembersIntoMember(ctx context.Context, q *db.Queries, member db.Member, orgID, userID pgtype.UUID) error {
	email, err := s.userEmail(ctx, q, userID)
	if err != nil {
		return err
	}
	placeholders, err := q.PlaceholderMembersByEmail(ctx, db.PlaceholderMembersByEmailParams{
		OrganizationID: orgID,
		Email:          email,
	})
	if err != nil {
		return err
	}
	for _, p := range placeholders {
		if p.MemberID == member.ID {
			continue
		}
		from := db.Member{ID: p.MemberID, OrganizationID: orgID, UserID: p.UserID}
		if err := s.AssignOrganizationEntitiesToDifferentMember(ctx, q, from, member); err != nil {
			return err
		}
		if err := q.DeleteMember(ctx, p.MemberID); err != nil {
			return err
		}
		if err := q.DeleteUserByID(ctx, p.UserID); err != nil {
			return err
		}
	}
	return nil
}

// AssignOrganizationEntitiesToDifferentMember ports MemberService::
// assignOrganizationEntitiesToDifferentMember. q must be transaction-bound.
func (s *Service) AssignOrganizationEntitiesToDifferentMember(ctx context.Context, q *db.Queries, from, to db.Member) error {
	orgID := from.OrganizationID
	if err := q.DeleteRunningTimeEntriesForMember(ctx, db.DeleteRunningTimeEntriesForMemberParams{
		OrganizationID: orgID, MemberID: from.ID,
	}); err != nil {
		return err
	}
	if err := q.ReassignTimeEntriesToMember(ctx, db.ReassignTimeEntriesToMemberParams{
		OrganizationID: orgID, MemberID: to.ID, UserID: to.UserID, MemberID_2: from.ID,
	}); err != nil {
		return err
	}
	if err := q.ReassignProjectMembersToMember(ctx, db.ReassignProjectMembersToMemberParams{
		MemberID: from.ID, OrganizationID: orgID, MemberID_2: to.ID, UserID: to.UserID,
	}); err != nil {
		return err
	}
	if err := q.DeleteProjectMembersForMember(ctx, db.DeleteProjectMembersForMemberParams{
		MemberID: from.ID, OrganizationID: orgID,
	}); err != nil {
		return err
	}
	if err := q.ReassignGoalsToMember(ctx, db.ReassignGoalsToMemberParams{
		OrganizationID: orgID, MemberID: from.ID, MemberID_2: to.ID,
	}); err != nil {
		return err
	}
	return s.rewriteGoalMemberFilters(ctx, q, orgID, from.ID, to.ID)
}

// rewriteGoalMemberFilters ports the goals-filters memberIds rewrite in
// assignOrganizationEntitiesToDifferentMember (memberId swap + dedupe).
func (s *Service) rewriteGoalMemberFilters(ctx context.Context, q *db.Queries, orgID, fromID, toID pgtype.UUID) error {
	goals, err := q.GoalsWithMemberFilter(ctx, db.GoalsWithMemberFilterParams{
		OrganizationID: orgID,
		Column2:        UUIDString(fromID),
	})
	if err != nil {
		return err
	}
	fromStr, toStr := UUIDString(fromID), UUIDString(toID)
	for _, g := range goals {
		var filters map[string]any
		if err := json.Unmarshal(g.Filters, &filters); err != nil {
			continue // malformed filters are skipped, like upstream's cast
		}
		ids, _ := filters["memberIds"].([]any)
		if len(ids) == 0 {
			continue
		}
		out := make([]string, 0, len(ids))
		seen := map[string]bool{}
		for _, v := range ids {
			s, _ := v.(string)
			if s == fromStr {
				s = toStr
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		filters["memberIds"] = out
		raw, err := json.Marshal(filters)
		if err != nil {
			return err
		}
		if err := q.UpdateGoalFilters(ctx, db.UpdateGoalFiltersParams{ID: g.ID, Filters: raw}); err != nil {
			return err
		}
	}
	return nil
}

// ChangeRole ports MemberService::changeRole.
// q must be transaction-bound when combined with other writes; the update
// handler calls it inside withTx.
func (s *Service) ChangeRole(ctx context.Context, q *db.Queries, member *db.Member, orgID pgtype.UUID, newRole string, allowOwnerChange bool) error {
	oldRole := strOrEmpty(member.Role)
	switch {
	case oldRole == RoleOwner:
		return ErrOrganizationNeedsOwner
	case oldRole == RolePlaceholder:
		return ErrChangingRoleOfPlaceholder
	case newRole == RolePlaceholder:
		return ErrChangingRoleToPlaceholder
	}
	if newRole == RoleOwner {
		if !allowOwnerChange {
			return ErrOnlyOwnerCanChangeOwnership
		}
		return s.ChangeOwnership(ctx, q, orgID, member)
	}
	member.Role = ptr(newRole)
	return q.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{ID: member.ID, Role: ptr(newRole)})
}

// ChangeOwnership ports MemberService::changeOwnership: the org's user_id
// moves to the new owner, the previous owner(s) demote to admin.
func (s *Service) ChangeOwnership(ctx context.Context, q *db.Queries, orgID pgtype.UUID, newOwner *db.Member) error {
	if err := q.SetOrganizationOwner(ctx, db.SetOrganizationOwnerParams{ID: orgID, UserID: newOwner.UserID}); err != nil {
		return err
	}
	newOwner.Role = ptr(RoleOwner)
	if err := q.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{ID: newOwner.ID, Role: ptr(RoleOwner)}); err != nil {
		return err
	}
	return q.DemoteOtherOwners(ctx, db.DemoteOtherOwnersParams{OrganizationID: orgID, ID: newOwner.ID})
}

// RemoveMember ports MemberService::removeMember. Runs in a transaction.
func (s *Service) RemoveMember(ctx context.Context, member db.Member, isPlaceholder bool, deleteRelated bool) error {
	if strOrEmpty(member.Role) == RoleOwner {
		return ErrCanNotRemoveOwner
	}
	orgID := member.OrganizationID
	return s.withTx(ctx, func(q *db.Queries) error {
		userName, userCurrent, err := s.userNameAndCurrent(ctx, q, member.UserID)
		if err != nil {
			return err
		}
		if !isPlaceholder && userCurrent == orgID {
			if err := q.ClearUserCurrentTeam(ctx, member.UserID); err != nil {
				return err
			}
			userCurrent = pgtype.UUID{}
		}
		if deleteRelated {
			if err := q.DeleteTimeEntriesForMemberUser(ctx, db.DeleteTimeEntriesForMemberUserParams{OrganizationID: orgID, UserID: member.UserID}); err != nil {
				return err
			}
			if err := q.DeleteProjectMembersForMemberUser(ctx, db.DeleteProjectMembersForMemberUserParams{OrganizationID: orgID, UserID: member.UserID}); err != nil {
				return err
			}
		} else {
			exists, err := q.TimeEntriesExistForMemberUser(ctx, db.TimeEntriesExistForMemberUserParams{OrganizationID: orgID, UserID: member.UserID})
			if err != nil {
				return err
			}
			if exists {
				return ErrEntityStillInUse("member", "time_entry")
			}
			exists, err = q.ProjectMembersExistForMemberUser(ctx, db.ProjectMembersExistForMemberUserParams{OrganizationID: orgID, UserID: member.UserID})
			if err != nil {
				return err
			}
			if exists {
				return ErrEntityStillInUse("member", "project_member")
			}
		}
		if err := q.DeleteGoalsForMember(ctx, db.DeleteGoalsForMemberParams{OrganizationID: orgID, MemberID: member.ID}); err != nil {
			return err
		}
		if err := q.DeleteMember(ctx, member.ID); err != nil {
			return err
		}
		if isPlaceholder {
			return q.DeleteUserByID(ctx, member.UserID)
		}
		if err := s.MakeSureUserHasAtLeastOneOrganization(ctx, q, member.UserID, userName); err != nil {
			return err
		}
		return s.MakeSureUserHasCurrentOrganization(ctx, q, member.UserID, userCurrent)
	})
}

// MakeMemberToPlaceholder ports MemberService::makeMemberToPlaceholder:
// a fresh placeholder user replicates the member's user (auth fields nulled),
// the membership re-points at it, and org entities move to the placeholder user.
// ensureOrg maps $makeSureUserHasAtLeastOneOrganization.
// q must be transaction-bound.
func (s *Service) MakeMemberToPlaceholder(ctx context.Context, q *db.Queries, member *db.Member, ensureOrg bool) error {
	orgID := member.OrganizationID
	userName, userCurrent, err := s.userNameAndCurrent(ctx, q, member.UserID)
	if err != nil {
		return err
	}
	if userCurrent == orgID {
		if err := q.ClearUserCurrentTeam(ctx, member.UserID); err != nil {
			return err
		}
	}
	oldUserID := member.UserID
	placeholder, err := q.InsertPlaceholderUser(ctx, db.InsertPlaceholderUserParams{ID: oldUserID, ID_2: newUUID()})
	if err != nil {
		return err
	}
	member.UserID = placeholder.ID
	member.Role = ptr(RolePlaceholder)
	if err := q.UpdateMemberUser(ctx, db.UpdateMemberUserParams{ID: member.ID, UserID: placeholder.ID}); err != nil {
		return err
	}
	if err := q.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{ID: member.ID, Role: ptr(RolePlaceholder)}); err != nil {
		return err
	}
	if err := q.ReassignTimeEntriesToUser(ctx, db.ReassignTimeEntriesToUserParams{
		OrganizationID: orgID, UserID: oldUserID, UserID_2: placeholder.ID,
	}); err != nil {
		return err
	}
	if err := q.ReassignProjectMembersToUser(ctx, db.ReassignProjectMembersToUserParams{
		OrganizationID: orgID, UserID: oldUserID, UserID_2: placeholder.ID,
	}); err != nil {
		return err
	}
	if !ensureOrg {
		return nil
	}
	if err := s.MakeSureUserHasAtLeastOneOrganization(ctx, q, oldUserID, userName); err != nil {
		return err
	}
	return s.MakeSureUserHasCurrentOrganization(ctx, q, oldUserID, pgtype.UUID{})
}

// MergePlaceholderInto ports MemberController::mergeInto's service part:
// entities move to memberTo, then the placeholder member + user are deleted.
func (s *Service) MergePlaceholderInto(ctx context.Context, from, to db.Member) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		if err := s.AssignOrganizationEntitiesToDifferentMember(ctx, q, from, to); err != nil {
			return err
		}
		if err := q.DeleteMember(ctx, from.ID); err != nil {
			return err
		}
		return q.DeleteUserByID(ctx, from.UserID)
	})
}

// IsEmailAlreadyMember ports MemberService::isEmailAlreadyMember.
func (s *Service) IsEmailAlreadyMember(ctx context.Context, q *db.Queries, orgID pgtype.UUID, email string) (bool, error) {
	return q.IsEmailMemberOfOrganization(ctx, db.IsEmailMemberOfOrganizationParams{
		OrganizationID: orgID, Email: email,
	})
}

func (s *Service) userEmail(ctx context.Context, q *db.Queries, id pgtype.UUID) (string, error) {
	u, err := q.GetUserByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}

func (s *Service) userNameAndCurrent(ctx context.Context, q *db.Queries, id pgtype.UUID) (string, pgtype.UUID, error) {
	u, err := q.GetUserByID(ctx, id)
	if err != nil {
		return "", pgtype.UUID{}, err
	}
	return u.Name, u.CurrentTeamID, nil
}
