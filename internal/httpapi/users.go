package httpapi

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/domain"
)

func gravatarURL(email string) string {
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?d=mp"
}

func userResource(u *auth.User) map[string]any {
	return map[string]any{
		"id":                                  u.ID,
		"name":                                u.Name,
		"email":                               u.Email,
		"pending_email":                       u.PendingEmail,
		"profile_photo_url":                   gravatarURL(u.Email),
		"timezone":                            u.Timezone,
		"week_start":                          u.WeekStart,
		"send_time_entry_still_running_email": u.SendStillRunningEmail,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// apiUser resolves the authenticated API user or writes 401 and returns nil.
func apiUser(w http.ResponseWriter, r *http.Request) *auth.User {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return nil
	}
	return u
}

func (s *Server) handleUsersMe(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": userResource(u)})
}

func (s *Server) handleMyMemberships(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT m.id, m.role, o.id, o.name, o.currency
		FROM members m JOIN organizations o ON o.id = m.organization_id
		WHERE m.user_id = $1`, u.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer rows.Close()
	data := []map[string]any{}
	for rows.Next() {
		var memberID, role, orgID, orgName, currency string
		if err := rows.Scan(&memberID, &role, &orgID, &orgName, &currency); err != nil {
			continue
		}
		data = append(data, map[string]any{
			"id":   memberID,
			"role": role,
			"organization": map[string]any{
				"id": orgID, "name": orgName, "currency": currency,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) handleUpdateCurrentOrganization(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrganizationID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"message": "The organization id field is required.",
			"errors":  map[string][]string{"organization_id": {"The organization id field is required."}},
		})
		return
	}
	var exists bool
	if err := s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM members WHERE user_id = $1 AND organization_id = $2)`,
		u.ID, body.OrganizationID).Scan(&exists); err != nil || !exists {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE users SET current_team_id = $1, updated_at = now() WHERE id = $2`,
		body.OrganizationID, u.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": body.OrganizationID}})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	if chi.URLParam(r, "user") != u.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	var body struct {
		Name                  string `json:"name"`
		Email                 string `json:"email"`
		Timezone              string `json:"timezone"`
		WeekStart             string `json:"week_start"`
		SendStillRunningEmail *bool  `json:"send_time_entry_still_running_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeMessage(w, http.StatusBadRequest, "invalid body")
		return
	}
	errs := map[string][]string{}
	if body.Name == "" {
		errs["name"] = []string{"The name field is required."}
	}
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		errs["email"] = []string{"The email field must be a valid email address."}
	}
	if body.Timezone == "" {
		errs["timezone"] = []string{"The timezone field is required."}
	}
	switch body.WeekStart {
	case "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
	default:
		errs["week_start"] = []string{"The selected week start is invalid."}
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"message": "The given data was invalid.", "errors": errs,
		})
		return
	}
	// Email change goes through pending_email + verification (upstream behavior).
	pending := u.PendingEmail
	if !strings.EqualFold(body.Email, u.Email) {
		var taken bool
		_ = s.pool.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM users WHERE email = $1 AND id <> $2)`,
			body.Email, u.ID).Scan(&taken)
		if taken {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"message": "The given data was invalid.",
				"errors":  map[string][]string{"email": {"The email has already been taken."}},
			})
			return
		}
		pending = &body.Email
	}
	sendMail := u.SendStillRunningEmail
	if body.SendStillRunningEmail != nil {
		sendMail = *body.SendStillRunningEmail
	}
	if _, err := s.pool.Exec(r.Context(), `
		UPDATE users SET name = $2, timezone = $3, week_start = $4,
			send_time_entry_still_running_email = $5, pending_email = $6, updated_at = now()
		WHERE id = $1`,
		u.ID, body.Name, body.Timezone, body.WeekStart, sendMail, pending); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	u.Name, u.Timezone, u.WeekStart, u.PendingEmail = body.Name, body.Timezone, body.WeekStart, pending
	u.SendStillRunningEmail = sendMail
	writeJSON(w, http.StatusOK, map[string]any{"data": userResource(u)})
}

func (s *Server) handleDestroyUser(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	if chi.URLParam(r, "user") != u.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if u.Password != nil && !auth.CheckPassword(*u.Password, body.Password) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"message": "The given data was invalid.",
			"errors":  map[string][]string{"password": {"The password is incorrect."}},
		})
		return
	}
	uid, _ := domain.UUID(u.ID)
	if err := s.domain.DeleteUser(r.Context(), uid); err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": u.ID}})
}

func (s *Server) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	if err := s.sendVerificationEmail(r, u); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetPendingEmail(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	if chi.URLParam(r, "user") != u.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE users SET pending_email = NULL, updated_at = now() WHERE id = $1`, u.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": u.ID}})
}

var _ = time.Now
