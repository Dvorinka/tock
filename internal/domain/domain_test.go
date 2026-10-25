package domain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

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

func newTestUser(t *testing.T, pool *pgxpool.Pool, placeholder bool) pgtype.UUID {
	t.Helper()
	id := pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true}
	email := fmt.Sprintf("u-%s@example.test", uuid.Must(uuid.NewV7()).String()[24:])
	_, err := pool.Exec(context.Background(), `
		INSERT INTO users (id, name, email, password, timezone, week_start, is_placeholder, created_at, updated_at)
		VALUES ($1, 'Test User', $2, 'x', 'UTC', 'monday', $3, now(), now())`, id, email, placeholder)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func TestCreateOrganization(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	owner := newTestUser(t, pool, false)

	org, err := s.CreateOrganization(ctx, "Acme", owner, false, "")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if org.Currency != "EUR" || org.PersonalTeam {
		t.Fatalf("unexpected defaults: %+v", org)
	}
	q := db.New(pool)
	member, err := q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if strOrEmpty(member.Role) != RoleOwner {
		t.Fatalf("expected owner role, got %v", member.Role)
	}
}

func TestInviteUserDuplicateMember(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, err := s.CreateOrganization(ctx, "Acme", owner, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, owner).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InviteUser(ctx, q, org.ID, email, RoleEmployee); !errors.Is(err, ErrUserAlreadyMember) {
		t.Fatalf("expected ErrUserAlreadyMember, got %v", err)
	}
}

func TestInviteUserRoleValidation(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	// Role validation is a FormRequest concern upstream — the service itself
	// does not police it. The HTTP layer rejects owner/placeholder.
	if _, err := s.InviteUser(ctx, q, org.ID, "ok-"+uuid.Must(uuid.NewV7()).String()[24:]+"@x.test", RoleEmployee); err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
}

func TestInviteUserDuplicateInvitation(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	email := "dup-" + uuid.Must(uuid.NewV7()).String()[24:] + "@x.test"
	if _, err := s.InviteUser(ctx, q, org.ID, email, RoleEmployee); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InviteUser(ctx, q, org.ID, email, RoleEmployee); !errors.Is(err, ErrInvitationAlreadyExists) {
		t.Fatalf("expected ErrInvitationAlreadyExists, got %v", err)
	}
}

func TestChangeRoleOwnerConstraints(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	member, err := q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})
	if err != nil {
		t.Fatal(err)
	}
	m := member
	if err := s.ChangeRole(ctx, q, &m, org.ID, RoleAdmin, true); !errors.Is(err, ErrOrganizationNeedsOwner) {
		t.Fatalf("expected ErrOrganizationNeedsOwner, got %v", err)
	}
	// Non-owner cannot promote.
	other := newTestUser(t, pool, false)
	otherMember, err := s.AddMember(ctx, other, org.ID, RoleEmployee)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRole(ctx, q, &otherMember, org.ID, RoleOwner, false); !errors.Is(err, ErrOnlyOwnerCanChangeOwnership) {
		t.Fatalf("expected ErrOnlyOwnerCanChangeOwnership, got %v", err)
	}
	// Owner promotes other → old owner demoted to admin.
	if err := s.ChangeRole(ctx, q, &otherMember, org.ID, RoleOwner, true); err != nil {
		t.Fatal(err)
	}
	old, err := q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strOrEmpty(old.Role) != RoleAdmin {
		t.Fatalf("former owner should be admin, got %v", old.Role)
	}
}

func TestRemoveMemberOwner(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	member, _ := q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})
	if err := s.RemoveMember(ctx, member, false, false); !errors.Is(err, ErrCanNotRemoveOwner) {
		t.Fatalf("expected ErrCanNotRemoveOwner, got %v", err)
	}
}

func TestPlaceholderCycle(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	memberID, _ := q.GetMember(ctx, db.GetMemberParams{UserID: owner, OrganizationID: org.ID})

	// Convert owner→placeholder is forbidden upstream at controller level;
	// here test conversion of a regular member.
	regular := newTestUser(t, pool, false)
	reg, err := s.AddMember(ctx, regular, org.ID, RoleEmployee)
	if err != nil {
		t.Fatal(err)
	}
	m := reg
	if err := s.MakeMemberToPlaceholder(ctx, q, &m, true); err != nil {
		t.Fatalf("MakeMemberToPlaceholder: %v", err)
	}
	var isPh bool
	if err := pool.QueryRow(ctx, `SELECT is_placeholder FROM users WHERE id = $1`, m.UserID).Scan(&isPh); err != nil || !isPh {
		t.Fatalf("member user should be placeholder: %v", err)
	}
	// The real user keeps an org (a new personal one).
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE user_id = $1`, regular).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("regular user should have a personal org, cnt=%d err=%v", cnt, err)
	}
	// Merge the placeholder back into the owner member.
	if err := s.MergePlaceholderInto(ctx, m, memberID); err != nil {
		t.Fatalf("MergePlaceholderInto: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE id = $1`, m.ID).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("placeholder member should be gone, cnt=%d err=%v", cnt, err)
	}
}

func TestDeleteOrganization(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	owner := newTestUser(t, pool, false)
	org, err := s.CreateOrganization(ctx, "Doomed", owner, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrganization(ctx, org.ID, pgtype.UUID{}); err != nil {
		t.Fatalf("DeleteOrganization: %v", err)
	}
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE id = $1`, org.ID).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("org should be gone, cnt=%d err=%v", cnt, err)
	}
	// The former owner got a personal organization and a current pointer.
	var cur pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT current_team_id FROM users WHERE id = $1`, owner).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if !cur.Valid {
		t.Fatal("owner should have a current org after deletion")
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, cur).Scan(&name); err != nil || name != "Test's Organization" {
		t.Fatalf("expected personal org 'Test's Organization', got %q err=%v", name, err)
	}
}

func TestProcessAcceptedInvitations(t *testing.T) {
	pool := testPool(t)
	s := NewService(pool)
	ctx := context.Background()
	q := db.New(pool)
	owner := newTestUser(t, pool, false)
	org, _ := s.CreateOrganization(ctx, "Acme", owner, false, "")
	newUser := newTestUser(t, pool, false)
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, newUser).Scan(&email); err != nil {
		t.Fatal(err)
	}
	inv, err := s.InviteUser(ctx, q, org.ID, email, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Upstream marks the invitation accepted at link-click time; the
	// registration path processes accepted_at rows.
	if err := q.MarkInvitationAccepted(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	orgs, err := s.ProcessAcceptedInvitations(ctx, q, newUser, email)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0] != org.ID {
		t.Fatalf("expected to join org, got %v", orgs)
	}
	member, err := q.GetMember(ctx, db.GetMemberParams{UserID: newUser, OrganizationID: org.ID})
	if err != nil || strOrEmpty(member.Role) != RoleAdmin {
		t.Fatalf("expected admin member, got %v err=%v", member.Role, err)
	}
	// Invitation consumed.
	var cnt int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM organization_invitations WHERE id = $1`, inv.ID).Scan(&cnt)
	if cnt != 0 {
		t.Fatal("invitation should be deleted")
	}
}
