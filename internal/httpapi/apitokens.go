package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/domain"
)

// ---- ApiTokenController port ----
//
// API tokens live outside the organization scope: they belong to the user and
// are managed from the profile page.

type apiTokenRow struct {
	ID        string
	Name      *string
	Scopes    *string
	Revoked   bool
	CreatedAt pgtype.Timestamp
	ExpiresAt pgtype.Timestamp
}

func (t apiTokenRow) resource() map[string]any {
	scopes := []string{}
	if t.Scopes != nil {
		_ = json.Unmarshal([]byte(*t.Scopes), &scopes)
	}
	var name any
	if t.Name != nil {
		name = *t.Name
	}
	return map[string]any{
		"id":         t.ID,
		"name":       name,
		"revoked":    t.Revoked,
		"scopes":     scopes,
		"created_at": tsFormat(t.CreatedAt),
		"expires_at": tsFormat(t.ExpiresAt),
	}
}

const apiTokenSelect = `
SELECT t.id, t.name, t.scopes, t.revoked, t.created_at, t.expires_at
FROM oauth_access_tokens t
JOIN oauth_clients c ON c.id = t.client_id
WHERE t.user_id = $1 AND c.grant_types::jsonb @> '"personal_access"'`

// handleIndexAPITokens: GET /api/v1/users/me/api-tokens
func (s *Server) handleIndexAPITokens(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	rows, err := s.pool.Query(r.Context(), apiTokenSelect+` ORDER BY t.created_at DESC`, user.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list api tokens", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var t apiTokenRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Scopes, &t.Revoked, &t.CreatedAt, &t.ExpiresAt); err != nil {
			slog.ErrorContext(r.Context(), "scan api token", "error", err)
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		out = append(out, t.resource())
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(r.Context(), "list api tokens", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// handleStoreAPIToken: POST /api/v1/users/me/api-tokens
//
// The access token is only in this response; it can not be retrieved later.
func (s *Server) handleStoreAPIToken(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	name, present, valid := jsonStr(body, "name")
	rawName := body["name"]
	switch {
	case !present || string(rawName) == "null" || (valid && name == ""):
		// Laravel treats null and "" alike for 'required'.
		errs["name"] = []string{"The name field is required."}
	case !valid:
		errs["name"] = []string{"The name field must be a string."}
	case len(name) > 255:
		errs["name"] = []string{"The name field must not be greater than 255 characters."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	tokenID, accessToken, err := s.oauth.IssuePersonalAccessToken(r.Context(), user.ID, name)
	if err != nil {
		slog.ErrorContext(r.Context(), "issue api token", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}

	row, err := s.findAPIToken(r, tokenID)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload api token", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	resource := row.resource()
	resource["access_token"] = accessToken
	writeJSON(w, http.StatusOK, map[string]any{"data": resource})
}

// handleRevokeAPIToken: POST /api/v1/users/me/api-tokens/{apiToken}/revoke
func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	token, ok := s.scopedAPIToken(w, r)
	if !ok {
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE oauth_access_tokens SET revoked = true WHERE id = $1`, token.ID); err != nil {
		slog.ErrorContext(r.Context(), "revoke api token", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.revokeRefreshTokens(r, token.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleDestroyAPIToken: DELETE /api/v1/users/me/api-tokens/{apiToken}
func (s *Server) handleDestroyAPIToken(w http.ResponseWriter, r *http.Request) {
	token, ok := s.scopedAPIToken(w, r)
	if !ok {
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`DELETE FROM oauth_access_tokens WHERE id = $1`, token.ID); err != nil {
		slog.ErrorContext(r.Context(), "delete api token", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.revokeRefreshTokens(r, token.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeRefreshTokens(r *http.Request, tokenID string) {
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE oauth_refresh_tokens SET revoked = true WHERE access_token_id = $1`, tokenID); err != nil {
		slog.ErrorContext(r.Context(), "revoke api token refresh tokens", "error", err)
	}
}

// scopedAPIToken resolves {apiToken} the way ApiTokenController does: the
// token has to exist, belong to the caller and be a personal access token.
func (s *Server) scopedAPIToken(w http.ResponseWriter, r *http.Request) (apiTokenRow, bool) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return apiTokenRow{}, false
	}
	id := chi.URLParam(r, "apiToken")
	var token apiTokenRow
	var owner pgtype.UUID
	err := s.pool.QueryRow(r.Context(), apiTokenSelect+` AND t.id = $2`, user.ID, id).
		Scan(&token.ID, &token.Name, &token.Scopes, &token.Revoked, &token.CreatedAt, &token.ExpiresAt)
	if err != nil {
		// Distinguish "belongs to somebody else" from "does not exist".
		ownerErr := s.pool.QueryRow(r.Context(),
			`SELECT user_id FROM oauth_access_tokens WHERE id = $1`, id).Scan(&owner)
		if ownerErr != nil {
			writeMessage(w, http.StatusNotFound, modelNotFound("Passport\\Token", id))
			return apiTokenRow{}, false
		}
		if !owner.Valid || domain.UUIDString(owner) != user.ID {
			writeMessage(w, http.StatusForbidden, "API token does not belong to user")
			return apiTokenRow{}, false
		}
		writeMessage(w, http.StatusForbidden, "API token is not a personal access token")
		return apiTokenRow{}, false
	}
	return token, true
}

func (s *Server) findAPIToken(r *http.Request, id string) (apiTokenRow, error) {
	var t apiTokenRow
	err := s.pool.QueryRow(r.Context(), apiTokenSelect+` AND t.id = $2`,
		auth.UserFromContext(r.Context()).ID, id).
		Scan(&t.ID, &t.Name, &t.Scopes, &t.Revoked, &t.CreatedAt, &t.ExpiresAt)
	if err != nil {
		return t, err
	}
	return t, nil
}
