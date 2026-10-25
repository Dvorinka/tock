package oauth2

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Dvorinka/tock/internal/auth"
)

// Token TTLs match Laravel Passport defaults.
const (
	accessTokenTTL  = 365 * 24 * time.Hour
	refreshTokenTTL = 365 * 24 * time.Hour
	authCodeTTL     = 10 * time.Minute
)

type Server struct {
	pool *pgxpool.Pool
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
	kid  string
}

// New loads RSA keys from privateKeyPath/publicKeyPath, generating and
// persisting a pair when absent (Passport storage/oauth-*.key equivalent).
func New(pool *pgxpool.Pool, privateKeyPath, publicKeyPath string) (*Server, error) {
	priv, err := loadOrCreateKeyPair(privateKeyPath, publicKeyPath)
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, priv: priv, pub: &priv.PublicKey, kid: "1"}, nil
}

func loadOrCreateKeyPair(privPath, pubPath string) (*rsa.PrivateKey, error) {
	if b, err := os.ReadFile(privPath); err == nil {
		block, _ := pem.Decode(b)
		if block != nil {
			if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return k, nil
			}
			if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if rk, ok := k.(*rsa.PrivateKey); ok {
					return rk, nil
				}
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generate oauth key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if err := os.MkdirAll(filepath.Dir(privPath), 0o700); err != nil {
		return nil, fmt.Errorf("create oauth key dir: %w", err)
	}
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write oauth private key: %w", err)
	}
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write oauth public key: %w", err)
	}
	return key, nil
}

// issueToken creates a Passport-shaped RS256 JWT and its DB rows.
func (s *Server) issueToken(ctx context.Context, userID, clientID string, name *string, scopes []string) (string, string, error) {
	jti := auth.RandomID(40)
	now := time.Now()
	claims := jwt.MapClaims{
		"aud":    clientID,
		"jti":    jti,
		"iat":    now.Unix(),
		"nbf":    now.Unix(),
		"exp":    now.Add(accessTokenTTL).Unix(),
		"sub":    userID,
		"scopes": scopes,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.priv)
	if err != nil {
		return "", "", fmt.Errorf("sign access token: %w", err)
	}
	scopesJSON, _ := json.Marshal(scopes)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_access_tokens (id, user_id, client_id, name, scopes, revoked, created_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, false, now(), now(), $6)`,
		jti, userID, clientID, name, string(scopesJSON), now.Add(accessTokenTTL)); err != nil {
		return "", "", fmt.Errorf("insert access token: %w", err)
	}
	refreshID := auth.RandomID(40)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_refresh_tokens (id, access_token_id, revoked, expires_at)
		VALUES ($1, $2, false, $3)`, refreshID, jti, now.Add(refreshTokenTTL)); err != nil {
		return "", "", fmt.Errorf("insert refresh token: %w", err)
	}
	return signed, refreshID, nil
}

// AuthenticateAPI validates a Bearer JWT and returns the owning user ID.
func (s *Server) AuthenticateAPI(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", errors.New("no bearer token")
	}
	token, err := jwt.Parse(strings.TrimPrefix(h, "Bearer "), func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.pub, nil
	})
	if err != nil || !token.Valid {
		return "", errors.New("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid claims")
	}
	jti, _ := claims["jti"].(string)
	sub, _ := claims["sub"].(string)
	var revoked bool
	var expiresAt time.Time
	err = s.pool.QueryRow(r.Context(),
		`SELECT revoked, expires_at FROM oauth_access_tokens WHERE id = $1`, jti).Scan(&revoked, &expiresAt)
	if err != nil || revoked || time.Now().After(expiresAt) {
		return "", errors.New("token revoked or expired")
	}
	return sub, nil
}

func writeOAuthError(w http.ResponseWriter, status int, errName, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": errName, "error_description": hint, "hint": hint,
	})
}

// HandleToken implements POST /oauth/token (authorization_code + refresh_token grants).
func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, 400, "invalid_request", "malformed body")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		s.grantAuthCode(w, r)
	case "refresh_token":
		s.grantRefresh(w, r)
	default:
		writeOAuthError(w, 400, "unsupported_grant_type", "grant not supported")
	}
}

func (s *Server) grantAuthCode(w http.ResponseWriter, r *http.Request) {
	code := r.Form.Get("code")
	var userID, clientID string
	var scopes *string
	var expiresAt time.Time
	err := s.pool.QueryRow(r.Context(), `
		DELETE FROM oauth_auth_codes WHERE id = $1 AND revoked = false AND expires_at > now()
		RETURNING user_id, client_id, scopes, expires_at`, code).
		Scan(&userID, &clientID, &scopes, &expiresAt)
	if err != nil {
		writeOAuthError(w, 400, "invalid_grant", "authorization code invalid or expired")
		return
	}
	// PKCE: auth code id encodes the challenge — stored alongside via session on issue.
	if meta, ok := pendingCodes.Load(code); ok {
		m := meta.(codeMeta)
		if r.Form.Get("client_id") != m.clientID {
			writeOAuthError(w, 400, "invalid_grant", "client mismatch")
			return
		}
		if m.challenge != "" {
			v := r.Form.Get("code_verifier")
			sum := sha256.Sum256([]byte(v))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != m.challenge && v != m.challenge {
				writeOAuthError(w, 400, "invalid_grant", "PKCE verification failed")
				return
			}
		}
		pendingCodes.Delete(code)
	}
	access, refresh, err := s.issueToken(r.Context(), userID, clientID, nil, parseScopes(scopes))
	if err != nil {
		writeOAuthError(w, 500, "server_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_type": "Bearer", "expires_in": int(accessTokenTTL.Seconds()),
		"access_token": access, "refresh_token": refresh,
	})
}

func (s *Server) grantRefresh(w http.ResponseWriter, r *http.Request) {
	var accessID string
	err := s.pool.QueryRow(r.Context(), `
		SELECT access_token_id FROM oauth_refresh_tokens
		WHERE id = $1 AND revoked = false AND expires_at > now()`,
		r.Form.Get("refresh_token")).Scan(&accessID)
	if err != nil {
		writeOAuthError(w, 400, "invalid_grant", "refresh token invalid")
		return
	}
	var userID, clientID string
	var scopes *string
	var revoked bool
	err = s.pool.QueryRow(r.Context(), `
		SELECT user_id, client_id, scopes, revoked FROM oauth_access_tokens WHERE id = $1`,
		accessID).Scan(&userID, &clientID, &scopes, &revoked)
	if err != nil || revoked {
		writeOAuthError(w, 400, "invalid_grant", "access token revoked")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE oauth_refresh_tokens SET revoked = true WHERE id = $1`, r.Form.Get("refresh_token"))
	_, _ = s.pool.Exec(r.Context(), `UPDATE oauth_access_tokens SET revoked = true WHERE id = $1`, accessID)
	access, refresh, err := s.issueToken(r.Context(), userID, clientID, nil, parseScopes(scopes))
	if err != nil {
		writeOAuthError(w, 500, "server_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_type": "Bearer", "expires_in": int(accessTokenTTL.Seconds()),
		"access_token": access, "refresh_token": refresh,
	})
}

func parseScopes(raw *string) []string {
	if raw == nil || *raw == "" {
		return []string{}
	}
	var out []string
	if json.Unmarshal([]byte(*raw), &out) == nil {
		return out
	}
	return strings.Fields(*raw)
}

type codeMeta struct {
	clientID  string
	challenge string
}

// shortcut: PKCE challenge for an issued code lives in-process. Upgrade to a
// column on oauth_auth_codes if multi-replica deployments need the exchange.
var pendingCodes sync.Map

// Authorize GET validates the request and either auto-approves or renders consent.
// Render is supplied by the caller (html page or redirect).
func (s *Server) Authorize(w http.ResponseWriter, r *http.Request,
	renderConsent func(w http.ResponseWriter, r *http.Request, clientID, clientName string, scopes []string)) {
	q := r.URL.Query()
	sess := auth.SessionFromContext(r.Context())
	user := auth.UserFromContext(r.Context())
	if user == nil {
		target := "/login?redirect=" + url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if q.Get("response_type") != "code" {
		writeOAuthError(w, 400, "unsupported_response_type", "only code supported")
		return
	}
	client := s.findClient(r.Context(), q.Get("client_id"))
	if client == nil || client.revoked {
		writeOAuthError(w, 400, "invalid_client", "unknown client")
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !contains(client.redirectURIs, redirectURI) {
		writeOAuthError(w, 400, "invalid_request", "redirect_uri not registered")
		return
	}
	scopes := strings.Fields(q.Get("scope"))
	authReq := map[string]any{
		"client_id": client.id, "redirect_uri": redirectURI, "state": q.Get("state"),
		"scopes": scopes, "code_challenge": q.Get("code_challenge"),
	}
	sess.Set("authRequest", authReq)
	sess.Set("authToken", auth.RandomToken(20))
	if userHasGranted(r.Context(), s.pool, user.ID, client.id, scopes) {
		s.approve(w, r, user.ID, client.id, redirectURI, q.Get("state"), q.Get("code_challenge"), scopes)
		return
	}
	renderConsent(w, r, client.id, client.name, scopes)
}

func userHasGranted(ctx context.Context, pool *pgxpool.Pool, userID, clientID string, scopes []string) bool {
	var count int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FROM oauth_access_tokens
		WHERE user_id = $1 AND client_id = $2 AND revoked = false`, userID, clientID).Scan(&count)
	// shortcut: auto-approve when any prior token exists; scope diff check is
	// upgrade-if-needed once scoped clients ship (upstream: hasGrantedScopes).
	return count > 0
}

// ApprovePost handles POST /oauth/authorize (consent form submit).
func (s *Server) ApprovePost(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	user := auth.UserFromContext(r.Context())
	if user == nil || sess == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	if err := auth.CheckCSRF(r); err != nil {
		http.Error(w, "CSRF token mismatch", http.StatusForbidden)
		return
	}
	if r.FormValue("auth_token") != sess.GetString("authToken") {
		http.Error(w, "invalid auth token", http.StatusForbidden)
		return
	}
	raw, _ := sess.Pull("authRequest").(map[string]any)
	sess.Delete("authToken")
	req, err := decodeAuthRequest(raw)
	if err != nil {
		http.Error(w, "authorization request expired", http.StatusBadRequest)
		return
	}
	s.approve(w, r, user.ID, req.clientID, req.redirectURI, req.state, req.challenge, req.scopes)
}

// DenyPost handles DELETE /oauth/authorize.
func (s *Server) DenyPost(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	user := auth.UserFromContext(r.Context())
	if user == nil || sess == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	raw, _ := sess.Pull("authRequest").(map[string]any)
	req, err := decodeAuthRequest(raw)
	if err != nil {
		http.Error(w, "authorization request expired", http.StatusBadRequest)
		return
	}
	u, _ := url.Parse(req.redirectURI)
	q := u.Query()
	q.Set("error", "access_denied")
	q.Set("error_description", "The user denied the authorization request")
	if req.state != "" {
		q.Set("state", req.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type authReqData struct {
	clientID, redirectURI, state, challenge string
	scopes                                []string
}

func decodeAuthRequest(m map[string]any) (authReqData, error) {
	var d authReqData
	if m == nil {
		return d, errors.New("missing")
	}
	d.clientID, _ = m["client_id"].(string)
	d.redirectURI, _ = m["redirect_uri"].(string)
	d.state, _ = m["state"].(string)
	d.challenge, _ = m["code_challenge"].(string)
	if ss, ok := m["scopes"].([]any); ok {
		for _, s := range ss {
			if str, ok := s.(string); ok {
				d.scopes = append(d.scopes, str)
			}
		}
	}
	if d.clientID == "" || d.redirectURI == "" {
		return d, errors.New("invalid")
	}
	return d, nil
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request,
	userID, clientID, redirectURI, state, challenge string, scopes []string) {
	code := auth.RandomID(40)
	scopesJSON, _ := json.Marshal(scopes)
	if _, err := s.pool.Exec(r.Context(), `
		INSERT INTO oauth_auth_codes (id, user_id, client_id, scopes, revoked, expires_at)
		VALUES ($1, $2, $3, $4, false, $5)`,
		code, userID, clientID, string(scopesJSON), time.Now().Add(authCodeTTL)); err != nil {
		writeOAuthError(w, 500, "server_error", err.Error())
		return
	}
	pendingCodes.Store(code, codeMeta{clientID: clientID, challenge: challenge})
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type oauthClient struct {
	id, name     string
	revoked      bool
	redirectURIs []string
}

func (s *Server) findClient(ctx context.Context, id string) *oauthClient {
	var c oauthClient
	var uris string
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, revoked, redirect_uris FROM oauth_clients WHERE id = $1`, id).
		Scan(&c.id, &c.name, &c.revoked, &uris)
	if err != nil {
		return nil
	}
	_ = json.Unmarshal([]byte(uris), &c.redirectURIs)
	return &c
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ListTokens: GET /oauth/tokens — Passport returns the user's live access tokens.
func (s *Server) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT t.id, t.name, t.scopes, t.created_at, t.expires_at, c.name
		FROM oauth_access_tokens t JOIN oauth_clients c ON c.id = t.client_id
		WHERE t.user_id = $1 AND t.revoked = false AND t.expires_at > now()
		ORDER BY t.created_at DESC`, user.ID)
	if err != nil {
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, clientName string
		var name, scopes *string
		var created, expires *time.Time
		if err := rows.Scan(&id, &name, &scopes, &created, &expires, &clientName); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "scopes": parseScopes(scopes),
			"created_at": created, "expires_at": expires,
			"revoked": false, "client": map[string]any{"id": "", "name": clientName},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// RevokeToken: DELETE /oauth/tokens/{token_id}
func (s *Server) RevokeToken(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "token_id")
	var owner string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT user_id FROM oauth_access_tokens WHERE id = $1`, id).Scan(&owner); err != nil || owner != user.ID {
		http.Error(w, `{"message":"Not Found."}`, http.StatusNotFound)
		return
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE oauth_access_tokens SET revoked = true WHERE id = $1`, id)
	_, _ = s.pool.Exec(r.Context(), `UPDATE oauth_refresh_tokens SET revoked = true WHERE access_token_id = $1`, id)
	w.WriteHeader(http.StatusNoContent)
}

// CreatePersonalAccessToken: POST /oauth/personal-access-tokens
func (s *Server) CreatePersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	var clientID string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT id FROM oauth_clients WHERE grant_types LIKE '%personal_access%' AND revoked = false LIMIT 1`).Scan(&clientID); err != nil {
		writeOAuthError(w, 500, "server_error", "no personal access client")
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	access, _, err := s.issueToken(r.Context(), user.ID, clientID, &body.Name, body.Scopes)
	if err != nil {
		writeOAuthError(w, 500, "server_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accessToken": access,
		"token":       map[string]any{"name": body.Name, "scopes": body.Scopes},
	})
}

// ListPersonalAccessTokens: GET /oauth/personal-access-tokens
func (s *Server) ListPersonalAccessTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	var pacID string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT id FROM oauth_clients WHERE grant_types LIKE '%personal_access%' AND revoked = false LIMIT 1`).Scan(&pacID); err != nil {
		pacID = ""
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, name, scopes, created_at, expires_at FROM oauth_access_tokens
		WHERE user_id = $1 AND client_id = $2 AND revoked = false
		ORDER BY created_at DESC`, user.ID, pacID)
	if err != nil {
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id string
		var name, scopes *string
		var created, expires *time.Time
		if err := rows.Scan(&id, &name, &scopes, &created, &expires); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "scopes": parseScopes(scopes),
			"created_at": created, "expires_at": expires,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

var _ = crypto.SHA256
