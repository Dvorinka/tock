package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type ctxKey int

const (
	ctxSession ctxKey = iota
	ctxUser
)

// User mirrors the users columns Phase 1 needs.
type User struct {
	ID                    string
	Name                  string
	Email                 string
	Password              *string
	EmailVerifiedAt       *time.Time
	PendingEmail          *string
	CurrentOrganizationID *string
	Timezone              string
	WeekStart             string
	ProfilePhotoPath      *string
	TwoFactorSecret       *string
	TwoFactorRecovery     *string
	TwoFactorConfirmedAt  *time.Time
	RememberToken         *string
	SendStillRunningEmail bool
}

func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(ctxUser).(*User)
	return u
}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxUser, u)
}

func CheckPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	// Laravel bcrypt hashes use $2y$; x/crypto accepts 2a/2b — normalize.
	if strings.HasPrefix(hash, "$2y$") {
		hash = "$2a$" + hash[4:]
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func RandomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Session is a DB-backed session in the upstream sessions table.
type Session struct {
	ID      string
	UserID  *string
	Data    map[string]any
	Dirty   bool
	Destroy bool
}

func (s *Session) Get(key string) any        { return s.Data[key] }
func (s *Session) Set(key string, v any)     { s.Data[key] = v; s.Dirty = true }
func (s *Session) Delete(key string)         { delete(s.Data, key); s.Dirty = true }
func (s *Session) GetString(key string) string {
	v, _ := s.Data[key].(string)
	return v
}

// Errors returns and clears the validation error bag (Laravel session flash "errors").
func (s *Session) PullErrors() map[string]string {
	raw, ok := s.Data["errors"]
	if !ok {
		return nil
	}
	delete(s.Data, "errors")
	s.Dirty = true
	m := map[string]string{}
	if mm, ok := raw.(map[string]string); ok {
		return mm
	}
	if mm, ok := raw.(map[string]any); ok {
		for k, v := range mm {
			m[k], _ = v.(string)
		}
	}
	return m
}

// Flash returns and clears a one-shot value.
func (s *Session) Pull(key string) any {
	v, ok := s.Data[key]
	if ok {
		delete(s.Data, key)
		s.Dirty = true
	}
	return v
}

type Store struct {
	pool     *pgxpool.Pool
	key      []byte
	Lifetime time.Duration
}

func NewStore(pool *pgxpool.Pool, appKey string) *Store {
	if appKey == "" {
		appKey = RandomToken(32) // sessions invalidated on restart
	}
	return &Store{pool: pool, key: []byte(appKey), Lifetime: 120 * time.Minute}
}

func (s *Store) sign(id string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))
}

// CookieName matches Laravel's slug(APP_NAME)_session convention for tock.
const CookieName = "tock_session"

func (s *Store) Load(r *http.Request) *Session {
	sess := &Session{ID: RandomID(20), Data: map[string]any{}, Dirty: true}
	c, err := r.Cookie(CookieName)
	if err != nil {
		sess.Set("_token", RandomToken(20))
		return sess
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(s.sign(parts[0]))) || s.pool == nil {
		sess.Set("_token", RandomToken(20))
		return sess
	}
	var payload []byte
	var lastActivity int64
	err = s.pool.QueryRow(r.Context(),
		`SELECT payload, last_activity FROM sessions WHERE id = $1`, parts[0]).Scan(&payload, &lastActivity)
	if err != nil || time.Now().Unix()-lastActivity > int64(s.Lifetime.Seconds()) {
		sess.Set("_token", RandomToken(20))
		return sess
	}
	var data map[string]any
	var userID *string
	var wire struct {
		UserID *string        `json:"u"`
		Data   map[string]any `json:"d"`
	}
	if json.Unmarshal(payload, &wire) == nil {
		data = wire.Data
		userID = wire.UserID
	}
	if data == nil {
		data = map[string]any{}
	}
	if data["_token"] == nil {
		data["_token"] = RandomToken(20)
	}
	return &Session{ID: parts[0], UserID: userID, Data: data}
}

func (s *Store) Save(ctx context.Context, sess *Session, r *http.Request) error {
	if s.pool == nil {
		return nil // tests without a database
	}
	if sess.Destroy {
		_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sess.ID)
		return err
	}
	payload, _ := json.Marshal(struct {
		UserID *string        `json:"u"`
		Data   map[string]any `json:"d"`
	}{sess.UserID, sess.Data})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, ip_address, user_agent, payload, last_activity)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET user_id = $2, payload = $5, last_activity = $6`,
		sess.ID, sess.UserID, clientIP(r), r.UserAgent(), string(payload), time.Now().Unix())
	return err
}

func (s *Store) SetCookie(w http.ResponseWriter, sess *Session, secure bool) {
	if sess.Destroy {
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sess.ID + "." + s.sign(sess.ID),
		Path:     "/",
		MaxAge:   int(s.Lifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Store) Regenerate(ctx context.Context, sess *Session) *Session {
	_ = s.DestroySession(ctx, sess.ID)
	ns := &Session{ID: RandomID(20), UserID: sess.UserID, Data: sess.Data, Dirty: true}
	return ns
}

func (s *Store) DestroySession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

func SessionFromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(ctxSession).(*Session)
	return s
}

func WithSession(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, ctxSession, sess)
}

// Middleware loads the session into context. The session is persisted and the
// session + XSRF cookies are written at the moment the response headers are
// first flushed — handlers mutate session state before writing, so this hook
// captures the final state.
func (s *Store) Middleware(secureCookies bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := s.Load(r)
			if sess.UserID == nil {
				if uid := s.recaller(r); uid != "" {
					sess.UserID = &uid
					sess.Dirty = true
				}
			}
			ctx := WithSession(r.Context(), sess)
			sw := &sessionWriter{ResponseWriter: w, store: s, sess: sess, r: r, secure: secureCookies}
			next.ServeHTTP(sw, r.WithContext(ctx))
			sw.flush()
		})
	}
}

type sessionWriter struct {
	http.ResponseWriter
	store  *Store
	sess   *Session
	r      *http.Request
	secure bool
	done   bool
}

func (sw *sessionWriter) flush() {
	if sw.done {
		return
	}
	sw.done = true
	_ = sw.store.Save(sw.r.Context(), sw.sess, sw.r)
	sw.store.SetCookie(sw.ResponseWriter, sw.sess, sw.secure)
	SetXSRFCookie(sw.ResponseWriter, sw.sess, sw.secure)
}

func (sw *sessionWriter) WriteHeader(code int) {
	sw.flush()
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *sessionWriter) Write(b []byte) (int, error) {
	sw.flush()
	return sw.ResponseWriter.Write(b)
}

// recaller implements Laravel's remember-me cookie: remember_web_<guard> = userID|token|hmac.
func (s *Store) recaller(r *http.Request) string {
	c, err := r.Cookie("remember_web")
	if err != nil {
		return ""
	}
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 {
		return ""
	}
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(parts[0] + "|" + parts[1]))
	if !hmac.Equal([]byte(parts[2]), []byte(hex.EncodeToString(m.Sum(nil)))) {
		return ""
	}
	var rt *string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT remember_token FROM users WHERE id = $1`, parts[0]).Scan(&rt); err != nil {
		return ""
	}
	if rt == nil || subtle.ConstantTimeCompare([]byte(*rt), []byte(parts[1])) != 1 {
		return ""
	}
	return parts[0]
}

// SetRecaller issues the remember-me cookie (5 years, like Laravel).
func (s *Store) SetRecaller(ctx context.Context, w http.ResponseWriter, userID string, secure bool) error {
	token := RandomToken(30)
	if _, err := s.pool.Exec(ctx, `UPDATE users SET remember_token = $1 WHERE id = $2`, token, userID); err != nil {
		return fmt.Errorf("set remember token: %w", err)
	}
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(userID + "|" + token))
	http.SetCookie(w, &http.Cookie{
		Name: "remember_web", Value: userID + "|" + token + "|" + hex.EncodeToString(m.Sum(nil)),
		Path: "/", MaxAge: 5 * 365 * 24 * 3600, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Store) ClearRecaller(ctx context.Context, w http.ResponseWriter, userID string) {
	_, _ = s.pool.Exec(ctx, `UPDATE users SET remember_token = NULL WHERE id = $1`, userID)
	http.SetCookie(w, &http.Cookie{Name: "remember_web", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// CheckCSRF enforces Laravel-style double-submit: session _token vs X-XSRF-TOKEN/_token.
func CheckCSRF(r *http.Request) error {
	sess := SessionFromContext(r.Context())
	if sess == nil {
		return errors.New("no session")
	}
	want := sess.GetString("_token")
	got := r.Header.Get("X-XSRF-TOKEN")
	if got == "" {
		got = r.FormValue("_token")
	}
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errors.New("csrf token mismatch")
	}
	return nil
}

func SetXSRFCookie(w http.ResponseWriter, sess *Session, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: "XSRF-TOKEN", Value: sess.GetString("_token"),
		Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// DeleteOtherSessions removes all sessions for a user except keepID —
// OtherBrowserSessionsController::deleteOtherSessionRecords.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID, keepID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND id != $2`, userID, keepID)
	return err
}
