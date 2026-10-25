// Package httpapi wires the chi router: /api JSON surface, the /oauth
// Passport-compatible endpoints, the Fortify web auth routes, and the
// embedded SPA served through the Inertia shell.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/config"
	"github.com/Dvorinka/tock/internal/oauth2"
	"github.com/Dvorinka/tock/internal/web"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter builds the HTTP handler for `tock serve`.
func NewRouter(cfg config.Config, pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(cfg.Logger))

	sessions := auth.NewStore(pool, cfg.AppKey)
	spa := &spaHandler{}
	oauth, err := oauth2.New(pool,
		filepath.Join(cfg.OAuthKeyDir, "oauth-private.key"),
		filepath.Join(cfg.OAuthKeyDir, "oauth-public.key"))
	if err != nil {
		panic(err)
	}
	srv := NewServer(pool, spa, sessions, oauth, cfg.AppURL, cfg.SMTPAddr, cfg.SecureCookies, cfg.Registration)

	// API bearer guard.
	apiAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, err := oauth.AuthenticateAPI(r)
			if err != nil {
				writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
				return
			}
			u, err := srv.loadUser(r.Context(), uid)
			if err != nil {
				writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	}

	r.Route("/api", func(r chi.Router) {
		r.Route("/v1", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(apiAuth)
				r.Get("/users/me", srv.handleUsersMe)
				r.Put("/users/me/current-organization", srv.handleUpdateCurrentOrganization)
				r.Put("/users/{user}", srv.handleUpdateUser)
				r.Delete("/users/{user}", srv.handleDestroyUser)
				r.Post("/users/{user}/resend-email-verification", srv.handleResendVerification)
				r.Post("/users/{user}/reset-pending-email", srv.handleResetPendingEmail)
				r.Get("/users/me/memberships", srv.handleMyMemberships)
			})
		})
		r.NotFound(apiNotFound)
		r.MethodNotAllowed(apiNotFound)
	})

	// OAuth2 (Passport parity). /token is stateless; the rest ride the web
	// session like upstream's 'web' + 'auth' middleware group.
	r.Post("/oauth/token", oauth.HandleToken)
	r.Route("/oauth", func(r chi.Router) {
		r.Use(sessions.Middleware(cfg.SecureCookies))
		r.Use(srv.webUserMiddleware)
		r.Get("/authorize", func(w http.ResponseWriter, r *http.Request) {
			oauth.Authorize(w, r, srv.renderConsent)
		})
		r.Post("/authorize", oauth.ApprovePost)
		r.Post("/authorize/deny", srv.handleDenyPost)
		r.Delete("/authorize", oauth.DenyPost)
		r.Get("/tokens", oauth.ListTokens)
		r.Delete("/tokens/{token_id}", oauth.RevokeToken)
		r.Get("/personal-access-tokens", oauth.ListPersonalAccessTokens)
		r.Post("/personal-access-tokens", oauth.CreatePersonalAccessToken)
		r.Get("/scopes", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{})
		})
	})

	// Web (Fortify) surface: session + CSRF + Inertia.
	r.Group(func(r chi.Router) {
		r.Use(sessions.Middleware(cfg.SecureCookies))
		r.Use(srv.webUserMiddleware)
		r.Use(csrfMiddleware)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			if auth.UserFromContext(r.Context()) != nil {
				http.Redirect(w, r, "/dashboard", http.StatusFound)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		})

		r.Group(func(r chi.Router) {
			r.Use(guestOnly)
			r.Get("/login", srv.handleLoginForm)
			r.Post("/login", srv.handleLogin)
			r.Get("/register", srv.handleRegisterForm)
			r.Post("/register", srv.handleRegister)
			r.Get("/forgot-password", srv.handleForgotForm)
			r.Post("/forgot-password", srv.handleForgot)
			r.Get("/reset-password/{token}", srv.handleResetForm)
			r.Post("/reset-password", srv.handleReset)
		})
		r.Post("/logout", srv.handleLogout)
		r.Get("/two-factor-challenge", srv.handleTwoFactorChallengeForm)
		r.Post("/two-factor-challenge", srv.handleTwoFactorChallenge)

		r.Group(func(r chi.Router) {
			r.Use(srv.requireAuth)
			r.Get("/email/verify", srv.handleVerifyNotice)
			r.Get("/email/verify/{id}/{hash}", srv.handleVerifyEmail)
			r.Post("/email/verification-notification", srv.handleResendVerificationWeb)
			r.Get("/user/confirm-password", srv.handleConfirmPasswordForm)
			r.Post("/user/confirm-password", srv.handleConfirmPassword)
			r.Get("/user/confirmed-password-status", srv.handlePasswordConfirmationStatus)
			r.Put("/user/profile-information", srv.handleUpdateProfileInformation)
			r.Put("/user/password", srv.handleUpdatePassword)
			r.Post("/user/two-factor-authentication", srv.handleEnableTwoFactor)
			r.Post("/user/confirmed-two-factor-authentication", srv.handleConfirmTwoFactor)
			r.Delete("/user/two-factor-authentication", srv.handleDisableTwoFactor)
			r.Get("/user/two-factor-qr-code", srv.handleTwoFactorQR)
			r.Get("/user/two-factor-secret-key", srv.handleTwoFactorSecret)
			r.Get("/user/two-factor-recovery-codes", srv.handleRecoveryCodes)
			r.Post("/user/two-factor-recovery-codes", srv.handleRegenerateRecoveryCodes)
			r.Get("/user/profile", srv.handleProfileShow)
			r.With(srv.requireVerified).Get("/dashboard", srv.handleDashboard)
		})

		r.NotFound(spaFallback)
	})

	mountStatic(r)

	return r
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeMessage(w, http.StatusNotFound, "API resource not found")
}

// mountStatic serves the embedded public webroot (/build, /favicons, ...).
func mountStatic(r chi.Router) {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))
	for _, prefix := range []string{
		"/build/", "/favicons/", "/images/", "/fonts/", "/desktop-version/",
	} {
		p := prefix
		r.Get(p+"*", func(w http.ResponseWriter, r *http.Request) {
			fileServer.ServeHTTP(w, r)
		})
	}
	for _, file := range []string{"/favicon.ico", "/robots.txt", "/security.txt"} {
		f := file
		r.Get(f, func(w http.ResponseWriter, r *http.Request) {
			fileServer.ServeHTTP(w, r)
		})
	}
}

// spaFallback: unknown GET page → /login (guest) or SPA shell; the Inertia
// router inside the SPA renders its own error state for authed users.
func spaFallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMessage(w, http.StatusNotFound, "Not found")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		apiNotFound(w, r)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// requestLogger logs one slog line per request.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
