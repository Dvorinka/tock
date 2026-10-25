package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// SPA page routes — every upstream Inertia::render web.php route.

func (s *Server) handleSharedReportPage(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "SharedReport", map[string]any{})
}

func (s *Server) renderPage(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.spa.Render(w, r, component, map[string]any{})
	}
}

func (s *Server) handleMembersPage(w http.ResponseWriter, r *http.Request) {
	roles := auth.RoleDefinitions()
	available := make([]map[string]any, 0, len(roles))
	for _, rd := range roles {
		available = append(available, map[string]any{
			"key":         rd.Key,
			"name":        rd.Name,
			"description": rd.Description,
		})
	}
	s.spa.Render(w, r, "Members", map[string]any{"availableRoles": available})
}

func (s *Server) handleTeamCreatePage(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "Teams/Create", map[string]any{})
}

// handleTeamShowPage ports OrganizationController::show — renders Teams/Show
// with team + currencies + permission props; redirects to dashboard when the
// organization is unknown or the user lacks organizations:view.
func (s *Server) handleTeamShowPage(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "organizationId")
	id, isUUID := domain.UUID(orgID)
	if !isUUID {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	u := auth.UserFromContext(r.Context())
	q := db.New(s.pool)
	org, err := q.GetOrganization(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	uid, _ := domain.UUID(u.ID)
	member, err := q.GetMember(r.Context(), db.GetMemberParams{
		OrganizationID: org.ID, UserID: uid,
	})
	if err != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	perms := map[string]bool{}
	for _, p := range auth.PermissionsForRole(deref(member.Role), org.EmployeesCanManageTasks) {
		perms[p] = true
	}
	if !perms["organizations:view"] {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	owner, err := q.GetUserByID(r.Context(), org.UserID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	currencies := []string{}
	for _, c := range domain.CurrencyList() {
		currencies = append(currencies, c.Name)
	}
	s.spa.Render(w, r, "Teams/Show", map[string]any{
		"team": map[string]any{
			"id":       domain.UUIDString(org.ID),
			"name":     org.Name,
			"currency": org.Currency,
			"owner": map[string]any{
				"id":                domain.UUIDString(owner.ID),
				"name":              owner.Name,
				"profile_photo_url": gravatarURL(owner.Email),
			},
		},
		"currencies": currencies,
		"permissions": map[string]any{
			"canDeleteTeam": perms["organizations:delete"],
			"canUpdateTeam": perms["organizations:update"],
		},
	})
}

// handleVerifyEmailChange ports UserController::verifyEmailChange — signed
// relative URL, applies pending_email after ownership + uniqueness checks.
func (s *Server) handleVerifyEmailChange(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	id, isUUID := domain.UUID(chi.URLParam(r, "user"))
	if !isUUID || domain.UUIDString(id) != u.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if !s.verifyURLSignature(r, true) {
		writeMessage(w, http.StatusForbidden, "Invalid signature.")
		return
	}
	email := strings.ToLower(r.URL.Query().Get("email"))
	if email == "" || u.PendingEmail == nil || *u.PendingEmail != email {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	uid2, _ := domain.UUID(u.ID)
	q := db.New(s.pool)
	exists, err := q.UserWithEmailExists(r.Context(), db.UserWithEmailExistsParams{
		Email: email, ID: uid2,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if exists {
		flashBanner(r, "danger", "The email address is already in use.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if err := q.ApplyPendingEmail(r.Context(), db.ApplyPendingEmailParams{ID: uid2, Email: email}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	flashBanner(r, "success", "Your email address has been updated successfully.")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func flashBanner(r *http.Request, style, text string) {
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		sess.Set("bannerStyle", style)
		sess.Set("bannerText", text)
	}
}

// handleLogoutOtherSessions ports OtherBrowserSessionsController::destroy —
// password confirmation, then delete all sessions except the current one.
func (s *Server) handleLogoutOtherSessions(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	password, _, _ := jsonStr(body, "password")
	if u.Password == nil || !auth.CheckPassword(*u.Password, password) {
		validationErr(w, map[string][]string{"password": {"The password is incorrect."}})
		return
	}
	sess := auth.SessionFromContext(r.Context())
	keep := ""
	if sess != nil {
		keep = sess.ID
	}
	if err := s.sessions.DeleteOtherSessions(r.Context(), u.ID, keep); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
