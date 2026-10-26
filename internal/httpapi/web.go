package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/skip2/go-qrcode"
	"html/template"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
	"github.com/Dvorinka/tock/internal/importer"
	"github.com/Dvorinka/tock/internal/jobs"
	"github.com/Dvorinka/tock/internal/oauth2"
)

type Server struct {
	pool          *pgxpool.Pool
	spa           *spaHandler
	sessions      *auth.Store
	oauth         *oauth2.Server
	domain        *domain.Service
	importer      *importer.Service
	appURL        string
	appKey        string
	storageDir    string
	gotenbergURL  string
	smtpAddr      string
	mailFrom      string
	secureCookies bool
	registration  bool
	superAdmins   []string
}

func NewServer(pool *pgxpool.Pool, spa *spaHandler, sessions *auth.Store, oauth *oauth2.Server, appURL, appKey, smtpAddr, storageDir, gotenbergURL string, secure, registration bool, superAdmins []string) *Server {
	return &Server{pool: pool, spa: spa, sessions: sessions, oauth: oauth, superAdmins: superAdmins,
		domain:   domain.NewService(pool),
		importer: importer.New(pool, storageDir),
		appURL:   appURL, appKey: appKey, smtpAddr: smtpAddr, storageDir: storageDir, mailFrom: "tock@localhost",
		gotenbergURL:  gotenbergURL,
		secureCookies: secure, registration: registration}
}

func (s *Server) loadUser(ctx context.Context, id string) (*auth.User, error) {
	u := &auth.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, email, password, email_verified_at, pending_email, current_team_id,
		       timezone, week_start, profile_photo_path, two_factor_secret,
		       two_factor_recovery_codes, two_factor_confirmed_at, remember_token,
		       send_time_entry_still_running_email
		FROM users WHERE id = $1`, id).Scan(
		&u.ID, &u.Name, &u.Email, &u.Password, &u.EmailVerifiedAt, &u.PendingEmail,
		&u.CurrentOrganizationID, &u.Timezone, &u.WeekStart, &u.ProfilePhotoPath,
		&u.TwoFactorSecret, &u.TwoFactorRecovery, &u.TwoFactorConfirmedAt,
		&u.RememberToken, &u.SendStillRunningEmail)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Server) userByEmail(ctx context.Context, email string) (*auth.User, error) {
	var id string
	if err := s.pool.QueryRow(ctx,
		`SELECT id FROM users WHERE lower(email) = lower($1) AND is_placeholder = false`,
		email).Scan(&id); err != nil {
		return nil, err
	}
	return s.loadUser(ctx, id)
}

// webUserMiddleware resolves session user + org permissions into the context.
func (s *Server) webUserMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := auth.SessionFromContext(r.Context())
		if sess != nil && sess.UserID != nil {
			if u, err := s.loadUser(r.Context(), *sess.UserID); err == nil {
				ctx := auth.WithUser(r.Context(), u)
				var role string
				var empTasks bool
				if u.CurrentOrganizationID != nil {
					if err := s.pool.QueryRow(ctx, `
						SELECT m.role, o.employees_can_manage_tasks
						FROM members m JOIN organizations o ON o.id = m.organization_id
						WHERE m.user_id = $1 AND m.organization_id = $2`,
						u.ID, *u.CurrentOrganizationID).Scan(&role, &empTasks); err == nil {
						ctx = context.WithValue(ctx, ctxPerms, auth.PermissionsForRole(role, empTasks))
					}
				}
				ctx = context.WithValue(ctx, ctxTeams, s.userTeams(ctx, u.ID))
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// userTeams returns the user's organizations, each with its membership, in the
// shape of Jetstream's `all_teams` relation. The SPA reads it for the
// organization switcher and for the current role.
func (s *Server) userTeams(ctx context.Context, userID string) []map[string]any {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.user_id, o.name, o.personal_team, o.currency,
		       o.created_at, o.updated_at,
		       m.id, m.role, m.billable_rate, m.created_at, m.updated_at
		FROM members m JOIN organizations o ON o.id = m.organization_id
		WHERE m.user_id = $1
		ORDER BY o.created_at`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	teams := []map[string]any{}
	for rows.Next() {
		var (
			orgID, orgUserID, orgName, currency string
			personalTeam                        bool
			orgCreated, orgUpdated              *time.Time
			memberID                            string
			memberRole                          *string
			billableRate                        *int32
			memberCreated, memberUpdated        *time.Time
		)
		if err := rows.Scan(&orgID, &orgUserID, &orgName, &personalTeam, &currency,
			&orgCreated, &orgUpdated,
			&memberID, &memberRole, &billableRate, &memberCreated, &memberUpdated); err != nil {
			return nil
		}
		teams = append(teams, map[string]any{
			"id":            orgID,
			"user_id":       orgUserID,
			"name":          orgName,
			"personal_team": personalTeam,
			"currency":      currency,
			"created_at":    orgCreated,
			"updated_at":    orgUpdated,
			"membership": map[string]any{
				"id":              memberID,
				"organization_id": orgID,
				"user_id":         userID,
				"role":            memberRole,
				"billable_rate":   billableRate,
				"created_at":      memberCreated,
				"updated_at":      memberUpdated,
			},
		})
	}
	return teams
}

// csrfMiddleware guards mutating web requests.
func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if err := auth.CheckCSRF(r); err != nil {
				writeMessage(w, http.StatusForbidden, "CSRF token mismatch")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserFromContext(r.Context()) == nil {
			if r.Header.Get("X-Inertia") != "" || strings.HasPrefix(r.URL.Path, "/api/") {
				writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
				return
			}
			sess := auth.SessionFromContext(r.Context())
			if sess != nil {
				sess.Set("url.intended", r.URL.RequestURI())
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireVerified(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u != nil && u.EmailVerifiedAt == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeMessage(w, http.StatusConflict, "Your email address is not verified.")
				return
			}
			// Inertia page requests (X-Inertia propagates through redirects)
			// must get a real redirect like upstream's verified middleware —
			// a 409 JSON lands on Inertia's "plain JSON" error modal.
			http.Redirect(w, r, "/email/verify", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func guestOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserFromContext(r.Context()) != nil {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- request helpers ----

func decodeBody(r *http.Request, v any) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return json.NewDecoder(r.Body).Decode(v)
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	m := map[string]any{}
	for k := range r.Form {
		m[k] = r.Form.Get(k)
	}
	b, _ := json.Marshal(m)
	return json.Unmarshal(b, v)
}

func flashErrors(r *http.Request, errs map[string]string) {
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		sess.Set("errors", errs)
	}
}

func backWithErrors(w http.ResponseWriter, r *http.Request, errs map[string]string, referer string) {
	// Jetstream forms post with an Inertia error bag — the client sends
	// X-Inertia-Error-Bag and expects errors under errors[bag].
	if bag := r.Header.Get("X-Inertia-Error-Bag"); bag != "" {
		if sess := auth.SessionFromContext(r.Context()); sess != nil {
			sess.Set("errors", map[string]any{bag: errs})
		}
	} else {
		flashErrors(r, errs)
	}
	if referer == "" {
		referer = r.Referer()
	}
	if referer == "" {
		referer = r.URL.RequestURI()
	}
	Redirect(w, r, referer)
}

// ---- login ----

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	status := ""
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		if v, ok := sess.Pull("status").(string); ok {
			status = v
		}
	}
	s.spa.Render(w, r, "Auth/Login", map[string]any{
		"canResetPassword": true,
		"status":           status,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		// Inertia sends the checkbox as 'on'/'' (Laravel boolean-ish), not a bool.
		Remember string `json:"remember"`
	}
	if err := decodeBody(r, &body); err != nil {
		backWithErrors(w, r, map[string]string{"email": "These credentials do not match our records."}, "/login")
		return
	}
	u, err := s.userByEmail(r.Context(), body.Email)
	if err != nil || u.Password == nil || !auth.CheckPassword(*u.Password, body.Password) {
		backWithErrors(w, r, map[string]string{"email": "These credentials do not match our records."}, "/login")
		return
	}
	sess := auth.SessionFromContext(r.Context())
	if u.TwoFactorSecret != nil && u.TwoFactorConfirmedAt != nil {
		sess.Set("login.id", u.ID)
		sess.Set("login.remember", body.Remember == "on")
		Redirect(w, r, "/two-factor-challenge")
		return
	}
	s.login(w, r, u, body.Remember == "on")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, u *auth.User, remember bool) {
	sess := auth.SessionFromContext(r.Context())
	ns := s.sessions.Regenerate(r.Context(), sess)
	*sess = *ns
	sess.UserID = &u.ID
	sess.Dirty = true
	if remember {
		_ = s.sessions.SetRecaller(r.Context(), w, u.ID, s.secureCookies)
	}
	intended := ""
	if v, ok := sess.Pull("url.intended").(string); ok {
		intended = v
	}
	if intended == "" {
		if q := r.URL.Query().Get("redirect"); q != "" && strings.HasPrefix(q, "/") {
			intended = q
		}
	}
	if intended == "" {
		intended = "/dashboard"
	}
	Redirect(w, r, intended)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	if sess != nil {
		if sess.UserID != nil {
			s.sessions.ClearRecaller(r.Context(), w, *sess.UserID)
		}
		sess.Destroy = true
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// ---- register ----

func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	email := ""
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		if v, ok := sess.Pull("registration_email").(string); ok {
			email = v
		}
	}
	// Fortify::registerView props — terms/privacy/newsletter are the upstream
	// config('auth.*') defaults (empty/false in OSS).
	s.spa.Render(w, r, "Auth/Register", map[string]any{
		"email":              email,
		"terms_url":          "",
		"privacy_policy_url": "",
		"newsletter_consent": false,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registration {
		writeMessage(w, http.StatusForbidden, "Registration is disabled.")
		return
	}
	var body struct {
		Name                 string `json:"name"`
		Email                string `json:"email"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
		Timezone             string `json:"timezone"`
	}
	if err := decodeBody(r, &body); err != nil {
		backWithErrors(w, r, map[string]string{"email": "Invalid request."}, "/register")
		return
	}
	errs := map[string]string{}
	if body.Name == "" {
		errs["name"] = "The name field is required."
	}
	if !strings.Contains(body.Email, "@") {
		errs["email"] = "The email field must be a valid email address."
	}
	if len(body.Password) < 8 {
		errs["password"] = "The password field must be at least 8 characters."
	} else if body.Password != body.PasswordConfirmation {
		errs["password"] = "The password field confirmation does not match."
	}
	if len(errs) == 0 {
		var taken bool
		_ = s.pool.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM users WHERE lower(email) = lower($1))`,
			body.Email).Scan(&taken)
		if taken {
			errs["email"] = "The email has already been taken."
		}
	}
	if len(errs) > 0 {
		backWithErrors(w, r, errs, "/register")
		return
	}
	tz := body.Timezone
	if tz == "" {
		tz = "UTC"
	}
	hash, _ := auth.HashPassword(body.Password)
	userID := newUUID()
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err = tx.Exec(r.Context(), `
		INSERT INTO users (id, name, email, password, timezone, week_start, is_placeholder, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'monday', false, now(), now())`,
		userID, body.Name, body.Email, hash, tz); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	// UserService::createUser — accepted invitations become memberships; a
	// personal organization is only created when none were accepted.
	uid, _ := domain.UUID(userID)
	qtx := db.New(s.pool).WithTx(tx)
	orgs, err := s.domain.ProcessAcceptedInvitations(r.Context(), qtx, uid, body.Email)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if len(orgs) > 0 {
		// The invitation link already proved email ownership — upstream
		// verifies invited registrants instead of forcing the verify step.
		if _, err = tx.Exec(r.Context(),
			`UPDATE users SET email_verified_at = now() WHERE id = $1`, uid); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if len(orgs) == 0 {
		if err := s.domain.MakeSureUserHasAtLeastOneOrganization(r.Context(), qtx, uid, body.Name); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	u, _ := s.loadUser(r.Context(), userID)
	if u != nil && u.EmailVerifiedAt == nil {
		_ = s.sendVerificationEmail(r, u)
	}
	s.login(w, r, u, false)
}

// ---- password reset ----

func (s *Server) handleForgotForm(w http.ResponseWriter, r *http.Request) {
	status := ""
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		if v, ok := sess.Pull("status").(string); ok {
			status = v
		}
	}
	s.spa.Render(w, r, "Auth/ForgotPassword", map[string]any{"status": status})
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	_ = decodeBody(r, &body)
	sess := auth.SessionFromContext(r.Context())
	u, err := s.userByEmail(r.Context(), body.Email)
	if err != nil {
		// Upstream Fortify surfaces INVALID_USER on the form instead of a
		// silent success.
		backWithErrors(w, r, map[string]string{
			"email": "We can't find a user with that email address.",
		}, "/forgot-password")
		return
	}
	token := auth.RandomToken(32)
	sum := sha256.Sum256([]byte(token))
	_, _ = s.pool.Exec(r.Context(), `
		INSERT INTO password_reset_tokens (email, token, created_at)
		VALUES ($1, $2, now())
		ON CONFLICT (email) DO UPDATE SET token = $2, created_at = now()`,
		u.Email, hex.EncodeToString(sum[:]))
	link := s.appURL + "/reset-password/" + token + "?email=" + url.QueryEscape(u.Email)
	_ = s.sendMailHTML(r.Context(), u.Email, "Reset your password",
		"You are receiving this email because we received a password reset request for your account. If you did not request a password reset, no further action is required.",
		"Reset Password", link)
	if sess != nil {
		sess.Set("status", "We have emailed your password reset link.")
	}
	Redirect(w, r, "/forgot-password")
}

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "Auth/ResetPassword", map[string]any{
		"email": r.URL.Query().Get("email"),
		"token": chi.URLParam(r, "token"),
	})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token                string `json:"token"`
		Email                string `json:"email"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
	}
	_ = decodeBody(r, &body)
	errs := map[string]string{}
	if len(body.Password) < 8 {
		errs["password"] = "The password field must be at least 8 characters."
	} else if body.Password != body.PasswordConfirmation {
		errs["password"] = "The password field confirmation does not match."
	}
	var hashed string
	var created *time.Time
	if err := s.pool.QueryRow(r.Context(),
		`SELECT token, created_at FROM password_reset_tokens WHERE email = $1`,
		body.Email).Scan(&hashed, &created); err != nil {
		errs["email"] = "This password reset token is invalid."
	} else {
		sum := sha256.Sum256([]byte(body.Token))
		if !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(hashed)) ||
			created == nil || time.Since(*created) > time.Hour {
			errs["email"] = "This password reset token is invalid."
		}
	}
	if len(errs) > 0 {
		backWithErrors(w, r, errs, r.Referer())
		return
	}
	hash, _ := auth.HashPassword(body.Password)
	_, _ = s.pool.Exec(r.Context(), `UPDATE users SET password = $1 WHERE email = $2`, hash, body.Email)
	_, _ = s.pool.Exec(r.Context(), `DELETE FROM password_reset_tokens WHERE email = $1`, body.Email)
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		sess.Set("status", "Your password has been reset.")
	}
	Redirect(w, r, "/login")
}

// ---- email verification ----

func (s *Server) signVerifyURL(userID string, email string, expires time.Time) string {
	hash := fmt.Sprintf("%x", sha1.Sum([]byte(email)))
	mac := hmac.New(sha256.New, []byte("tock-verify"))
	mac.Write([]byte(userID + "|" + hash + "|" + strconv.FormatInt(expires.Unix(), 10)))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s/email/verify/%s/%s?expires=%d&signature=%s",
		s.appURL, userID, hash, expires.Unix(), sig)
}

func (s *Server) verifySignedURL(r *http.Request) bool {
	userID := chi.URLParam(r, "id")
	hash := chi.URLParam(r, "hash")
	expires, _ := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	if expires < time.Now().Unix() {
		return false
	}
	mac := hmac.New(sha256.New, []byte("tock-verify"))
	mac.Write([]byte(userID + "|" + hash + "|" + strconv.FormatInt(expires, 10)))
	return hmac.Equal([]byte(r.URL.Query().Get("signature")), []byte(hex.EncodeToString(mac.Sum(nil))))
}

func (s *Server) sendVerificationEmail(r *http.Request, u *auth.User) error {
	link := s.signVerifyURL(u.ID, u.Email, time.Now().Add(24*time.Hour))
	return s.sendMailHTML(r.Context(), u.Email, "Verify Email Address",
		"Please click the button below to verify your email address. If you did not create an account, no further action is required.",
		"Verify Email Address", link)
}

// sendMailHTML sends a branded HTML notification (upstream mails are HTML).
func (s *Server) sendMailHTML(ctx context.Context, to, subject, text, action, link string) error {
	if s.smtpAddr == "" {
		return nil // mail disabled
	}
	escaped := template.HTMLEscapeString(text)
	html := `<!doctype html><html><body style="font-family:ui-sans-serif,system-ui,sans-serif;background:#f6f7f9;padding:32px">
	  <div style="max-width:480px;margin:0 auto;background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:32px">
	  <div style="font-size:20px;font-weight:700;letter-spacing:-0.02em;margin-bottom:24px">tock</div>
	  <p style="font-size:14px;color:#374151;line-height:1.5;margin:0 0 24px">` + escaped + `</p>
	  <a href="` + link + `" style="display:inline-block;background:#111827;color:#fff;font-size:14px;font-weight:600;text-decoration:none;border-radius:8px;padding:10px 20px">` + action + `</a>
	  <p style="font-size:12px;color:#9ca3af;margin:24px 0 0;word-break:break-all">` + link + `</p>
	  </div></body></html>`
	if s.pool != nil {
		return jobs.EnqueueMailHTML(ctx, s.pool, to, subject, text, html)
	}
	return s.sendMail(ctx, to, subject, text)
}

func (s *Server) sendMail(ctx context.Context, to, subject, body string) error {
	if s.smtpAddr == "" {
		return nil // mail disabled
	}
	// Queue like upstream's queue driver — web requests must not block on SMTP.
	if s.pool != nil {
		return jobs.EnqueueMail(ctx, s.pool, to, subject, body)
	}
	msg := "From: " + s.mailFrom + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n\r\n" + body + "\r\n"
	return smtp.SendMail(s.smtpAddr, nil, s.mailFrom, []string{to}, []byte(msg))
}

func (s *Server) handleVerifyNotice(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "Auth/VerifyEmail", map[string]any{"status": ""})
}

func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !s.verifySignedURL(r) || chi.URLParam(r, "id") != u.ID {
		http.Error(w, "Invalid signature.", http.StatusForbidden)
		return
	}
	if u.EmailVerifiedAt == nil {
		_, _ = s.pool.Exec(r.Context(),
			`UPDATE users SET email_verified_at = now() WHERE id = $1`, u.ID)
	}
	http.Redirect(w, r, "/dashboard?verified=1", http.StatusFound)
}

func (s *Server) handleResendVerificationWeb(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	if u.EmailVerifiedAt != nil {
		Redirect(w, r, "/dashboard")
		return
	}
	_ = s.sendVerificationEmail(r, u)
	if sess := auth.SessionFromContext(r.Context()); sess != nil {
		sess.Set("status", "A new verification link has been sent to the email address you provided during registration.")
	}
	backWithErrors(w, r, map[string]string{}, "/email/verify")
}

// ---- password confirm ----

func (s *Server) handleConfirmPasswordForm(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "Auth/ConfirmPassword", map[string]any{})
}

func (s *Server) handleConfirmPassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var body struct {
		Password string `json:"password"`
	}
	_ = decodeBody(r, &body)
	if u == nil || u.Password == nil || !auth.CheckPassword(*u.Password, body.Password) {
		if r.Header.Get("X-Inertia") == "" {
			// Jetstream's axios confirm modal expects the Laravel 422 shape.
			validationErr(w, map[string][]string{"password": {"The password is incorrect."}})
			return
		}
		backWithErrors(w, r, map[string]string{"password": "The password is incorrect."}, "/user/confirm-password")
		return
	}
	sess := auth.SessionFromContext(r.Context())
	sess.Set("auth.password_confirmed_at", time.Now().Unix())
	if r.Header.Get("X-Inertia") == "" {
		writeJSON(w, http.StatusOK, map[string]any{"message": ""})
		return
	}
	Redirect(w, r, "/dashboard")
}

func (s *Server) handlePasswordConfirmationStatus(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	confirmed := false
	if sess != nil {
		if ts, ok := sess.Get("auth.password_confirmed_at").(float64); ok {
			confirmed = time.Since(time.Unix(int64(ts), 0)) < 3*time.Hour
		} else if ts, ok := sess.Get("auth.password_confirmed_at").(int64); ok {
			confirmed = time.Since(time.Unix(ts, 0)) < 3*time.Hour
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"confirmed": confirmed})
}

// ---- two-factor ----

func (s *Server) handleTwoFactorChallengeForm(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	if sess == nil || sess.GetString("login.id") == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.spa.Render(w, r, "Auth/TwoFactorChallenge", map[string]any{})
}

func (s *Server) handleTwoFactorChallenge(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	if sess == nil || sess.GetString("login.id") == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	u, err := s.loadUser(r.Context(), sess.GetString("login.id"))
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	var body struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	_ = decodeBody(r, &body)
	ok := false
	if body.Code != "" && u.TwoFactorSecret != nil {
		ok = totp.Validate(body.Code, *u.TwoFactorSecret)
	}
	if !ok && body.RecoveryCode != "" && u.TwoFactorRecovery != nil {
		var codes []string
		if json.Unmarshal([]byte(*u.TwoFactorRecovery), &codes) == nil {
			for i, c := range codes {
				if subtle_EQ(c, body.RecoveryCode) {
					codes = append(codes[:i], codes[i+1:]...)
					enc, _ := json.Marshal(codes)
					_, _ = s.pool.Exec(r.Context(),
						`UPDATE users SET two_factor_recovery_codes = $1 WHERE id = $2`,
						string(enc), u.ID)
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		errs := map[string]string{"code": "The provided two factor authentication code was invalid."}
		if body.RecoveryCode != "" {
			errs = map[string]string{"recovery_code": "The provided two factor recovery code was invalid."}
		}
		backWithErrors(w, r, errs, "/two-factor-challenge")
		return
	}
	remember := sess.Get("login.remember") == true
	sess.Delete("login.id")
	sess.Delete("login.remember")
	s.login(w, r, u, remember)
}

func subtle_EQ(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func (s *Server) handleEnableTwoFactor(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "tock", AccountName: u.Email})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	codes := make([]string, 8)
	for i := range codes {
		codes[i] = auth.RandomID(5) + "-" + auth.RandomID(5)
	}
	enc, _ := json.Marshal(codes)
	if _, err := s.pool.Exec(r.Context(), `
		UPDATE users SET two_factor_secret = $1, two_factor_recovery_codes = $2,
			two_factor_confirmed_at = NULL WHERE id = $3`,
		key.Secret(), string(enc), u.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	Redirect(w, r, "/user/profile")
}

func (s *Server) handleConfirmTwoFactor(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = decodeBody(r, &body)
	if u.TwoFactorSecret == nil || !totp.Validate(body.Code, *u.TwoFactorSecret) {
		backWithErrors(w, r, map[string]string{"code": "The provided two factor authentication code was invalid."}, r.Referer())
		return
	}
	_, _ = s.pool.Exec(r.Context(),
		`UPDATE users SET two_factor_confirmed_at = now() WHERE id = $1`, u.ID)
	Redirect(w, r, "/user/profile")
}

func (s *Server) handleDisableTwoFactor(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `
		UPDATE users SET two_factor_secret = NULL, two_factor_recovery_codes = NULL,
			two_factor_confirmed_at = NULL WHERE id = $1`, u.ID)
	Redirect(w, r, "/user/profile")
}

func (s *Server) handleTwoFactorQR(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil || u.TwoFactorSecret == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	otpauth := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s",
		url.QueryEscape("tock"), url.QueryEscape(u.Email), *u.TwoFactorSecret, url.QueryEscape("tock"))
	svg := qrSVG(otpauth)
	writeJSON(w, http.StatusOK, map[string]any{"svg": svg, "url": otpauth})
}

func (s *Server) handleTwoFactorSecret(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil || u.TwoFactorSecret == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secretKey": *u.TwoFactorSecret})
}

func (s *Server) handleRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	var codes []string
	if u.TwoFactorRecovery != nil {
		_ = json.Unmarshal([]byte(*u.TwoFactorRecovery), &codes)
	}
	if codes == nil {
		codes = []string{}
	}
	writeJSON(w, http.StatusOK, codes)
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	codes := make([]string, 8)
	for i := range codes {
		codes[i] = auth.RandomID(5) + "-" + auth.RandomID(5)
	}
	enc, _ := json.Marshal(codes)
	_, _ = s.pool.Exec(r.Context(),
		`UPDATE users SET two_factor_recovery_codes = $1 WHERE id = $2`, string(enc), u.ID)
	writeJSON(w, http.StatusOK, codes)
}

// ---- dashboard + oauth views ----

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.spa.Render(w, r, "Dashboard", map[string]any{})
}

// consentPage is the passport::authorize blade equivalent.
var consentTemplate = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize — tock</title>
<style>body{font-family:sans-serif;max-width:480px;margin:80px auto;padding:0 20px}
.btn{display:inline-block;padding:10px 24px;margin-right:12px;border:0;border-radius:6px;cursor:pointer;text-decoration:none;font-size:14px}
.approve{background:#16a34a;color:#fff}.deny{background:#dc2626;color:#fff}
.card{border:1px solid #e5e7eb;border-radius:8px;padding:24px}</style></head>
<body><div class="card">
<h2>Authorization Request</h2>
<p><strong>{{.ClientName}}</strong> is requesting permission to access your account.</p>
{{if .Scopes}}<p>This application will be able to: {{range .Scopes}}<em>{{.}}</em> {{end}}</p>{{end}}
<form method="POST" action="/oauth/authorize" style="display:inline">
<input type="hidden" name="_token" value="{{.CSRF}}">
<input type="hidden" name="auth_token" value="{{.AuthToken}}">
<button type="submit" class="btn approve">Authorize</button>
</form>
<form method="POST" action="/oauth/authorize/deny" style="display:inline">
<input type="hidden" name="_token" value="{{.CSRF}}">
<input type="hidden" name="auth_token" value="{{.AuthToken}}">
<button type="submit" class="btn deny">Deny</button>
</form>
</div></body></html>`))

func (s *Server) renderConsent(w http.ResponseWriter, r *http.Request, clientID, clientName string, scopes []string) {
	sess := auth.SessionFromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentTemplate.Execute(w, map[string]any{
		"ClientName": clientName,
		"Scopes":     scopes,
		"CSRF":       sess.GetString("_token"),
		"AuthToken":  sess.GetString("authToken"),
	})
}

func (s *Server) handleDenyPost(w http.ResponseWriter, r *http.Request) {
	if err := auth.CheckCSRF(r); err != nil {
		http.Error(w, "CSRF token mismatch", http.StatusForbidden)
		return
	}
	r.Method = http.MethodDelete
	s.oauth.DenyPost(w, r)
}

// ---- profile ----

func (s *Server) handleUpdateProfileInformation(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	_ = decodeBody(r, &body)
	errs := map[string]string{}
	if body.Name == "" {
		errs["name"] = "The name field is required."
	}
	if !strings.Contains(body.Email, "@") {
		errs["email"] = "The email field must be a valid email address."
	}
	if len(errs) > 0 {
		backWithErrors(w, r, errs, r.Referer())
		return
	}
	pending := u.PendingEmail
	if !strings.EqualFold(body.Email, u.Email) {
		var taken bool
		_ = s.pool.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM users WHERE lower(email) = lower($1) AND id <> $2)`,
			body.Email, u.ID).Scan(&taken)
		if taken {
			backWithErrors(w, r, map[string]string{"email": "The email has already been taken."}, r.Referer())
			return
		}
		pending = &body.Email
	}
	_, _ = s.pool.Exec(r.Context(),
		`UPDATE users SET name = $1, pending_email = $2, updated_at = now() WHERE id = $3`,
		body.Name, pending, u.ID)
	Redirect(w, r, "/user/profile")
}

func (s *Server) handleUpdatePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var body struct {
		CurrentPassword      string `json:"current_password"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
	}
	_ = decodeBody(r, &body)
	errs := map[string]string{}
	if u.Password != nil && !auth.CheckPassword(*u.Password, body.CurrentPassword) {
		errs["current_password"] = "The provided password does not match your current password."
	}
	if len(body.Password) < 8 {
		errs["password"] = "The password field must be at least 8 characters."
	} else if body.Password != body.PasswordConfirmation {
		errs["password"] = "The password field confirmation does not match."
	}
	if len(errs) > 0 {
		backWithErrors(w, r, errs, r.Referer())
		return
	}
	hash, _ := auth.HashPassword(body.Password)
	_, _ = s.pool.Exec(r.Context(), `UPDATE users SET password = $1 WHERE id = $2`, hash, u.ID)
	Redirect(w, r, "/user/profile")
}

// profilePlaceholder renders the Jetstream profile Inertia page.
func (s *Server) handleProfileShow(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFromContext(r.Context())
	s.spa.Render(w, r, "Profile/Show", map[string]any{
		// Upstream: Features::optionEnabled('two-factor-authentication',
		// 'confirmPassword') — a static feature flag, not session state.
		"confirmsTwoFactorAuthentication": true,
		"sessions":                        s.userSessions(r, sess),
	})
}

// userSessions builds the Jetstream `sessions` prop — recent rows from the
// shared sessions table parsed into {agent, ip_address, is_current_device,
// last_active}.
func (s *Server) userSessions(r *http.Request, sess *auth.Session) []map[string]any {
	u := auth.UserFromContext(r.Context())
	out := []map[string]any{}
	if u == nil {
		return out
	}
	currentID := ""
	if sess != nil {
		currentID = sess.ID
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, coalesce(ip_address,''), coalesce(user_agent,''), last_activity
		FROM sessions WHERE user_id = $1 ORDER BY last_activity DESC LIMIT 10`, u.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, ip, ua string
		var last int64
		if err := rows.Scan(&id, &ip, &ua, &last); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"agent":             sessionAgent(ua),
			"ip_address":        ip,
			"is_current_device": id == currentID,
			"last_active":       time.Since(time.Unix(last, 0)).Round(time.Second).String(),
		})
	}
	return out
}

func sessionAgent(ua string) map[string]any {
	platform, browser := "Unknown", "Unknown"
	desktop := true
	switch {
	case strings.Contains(ua, "Windows"):
		platform = "Windows"
	case strings.Contains(ua, "Mac OS"):
		platform = "macOS"
	case strings.Contains(ua, "Android"):
		platform = "Android"
		desktop = false
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		platform = "iOS"
		desktop = false
	case strings.Contains(ua, "Linux"):
		platform = "Linux"
	}
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	return map[string]any{"platform": platform, "browser": browser, "is_desktop": desktop}
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// qrSVG renders a minimal QR placeholder — see shortcut note.
// shortcut: emits the otpauth URL for manual entry; real QR rasterization
// lands with a qrcode dependency if the FE needs the SVG.
func qrSVG(data string) string {
	png, err := qrcode.Encode(data, qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	// BaconQrCode upstream emits an <svg>; img data-URL renders identically.
	return fmt.Sprintf("<img src='data:image/png;base64,%s' alt='QR code'>", base64.StdEncoding.EncodeToString(png))
}

var _ = pgx.ErrNoRows
