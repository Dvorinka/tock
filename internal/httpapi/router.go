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
	srv := NewServer(pool, spa, sessions, oauth, cfg.AppURL, cfg.AppKey, cfg.SMTPAddr, cfg.StorageDir, cfg.GotenbergURL, cfg.SecureCookies, cfg.Registration)

	// API bearer guard — 'auth:api' + 'verified' like the upstream group.
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
			if u.EmailVerifiedAt == nil {
				writeMessage(w, http.StatusConflict, "Your email address is not verified.")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	}

	r.Route("/api", func(r chi.Router) {
		r.Route("/v1", func(r chi.Router) {
			r.Get("/currencies", srv.handleCurrencies)
			r.Get("/time-zones", srv.handleTimeZones)

			r.Get("/public/reports", srv.handlePublicReport)
			r.Group(func(r chi.Router) {
				r.Use(apiAuth)
				r.Get("/users/me", srv.handleUsersMe)
				r.Put("/users/me/current-organization", srv.handleUpdateCurrentOrganization)
				r.Put("/users/{user}", srv.handleUpdateUser)
				r.Delete("/users/{user}", srv.handleDestroyUser)
				r.Post("/users/{user}/resend-email-verification", srv.handleResendVerification)
				r.Post("/users/{user}/reset-pending-email", srv.handleResetPendingEmail)
				r.Get("/users/me/memberships", srv.handleMyMemberships)

				r.Post("/organizations", srv.handleStoreOrganization)
				r.Get("/organizations/{organization}", srv.handleShowOrganization)
				r.Put("/organizations/{organization}", srv.handleUpdateOrganization)
				r.Delete("/organizations/{organization}", srv.handleDestroyOrganization)

				r.Get("/organizations/{organization}/members", srv.handleListMembers)
				r.Put("/organizations/{organization}/members/{member}", srv.handleUpdateMember)
				r.Delete("/organizations/{organization}/members/{member}", srv.handleDestroyMember)
				r.Post("/organizations/{organization}/members/{member}/invite-placeholder", srv.handleInvitePlaceholder)
				r.Post("/organizations/{organization}/members/{member}/make-placeholder", srv.handleMakePlaceholder)
				r.Post("/organizations/{organization}/member/{member}/merge-into", srv.handleMergeInto)

				r.Get("/organizations/{organization}/invitations", srv.handleListInvitations)
				r.Post("/organizations/{organization}/invitations", srv.handleStoreInvitation)
				r.Post("/organizations/{organization}/invitations/{invitation}/resend", srv.handleResendInvitation)
				r.Delete("/organizations/{organization}/invitations/{invitation}", srv.handleDestroyInvitation)

				r.Get("/organizations/{organization}/importers", srv.handleImporters)
				r.Post("/organizations/{organization}/import", srv.handleImport)

				// Entity surface — clients, projects, project members,
				// tags, tasks (routes/api.php verbatim).
				r.Get("/organizations/{organization}/clients", srv.handleListClients)
				r.Post("/organizations/{organization}/clients", srv.handleStoreClient)
				r.Put("/organizations/{organization}/clients/{client}", srv.handleUpdateClient)
				r.Delete("/organizations/{organization}/clients/{client}", srv.handleDestroyClient)

				r.Get("/organizations/{organization}/projects", srv.handleListProjects)
				r.Get("/organizations/{organization}/projects/{project}", srv.handleShowProject)
				r.Post("/organizations/{organization}/projects", srv.handleStoreProject)
				r.Put("/organizations/{organization}/projects/{project}", srv.handleUpdateProject)
				r.Delete("/organizations/{organization}/projects/{project}", srv.handleDestroyProject)

				r.Get("/organizations/{organization}/projects/{project}/project-members", srv.handleListProjectMembers)
				r.Post("/organizations/{organization}/projects/{project}/project-members", srv.handleStoreProjectMember)
				r.Put("/organizations/{organization}/project-members/{projectMember}", srv.handleUpdateProjectMember)
				r.Delete("/organizations/{organization}/project-members/{projectMember}", srv.handleDestroyProjectMember)

				r.Get("/organizations/{organization}/tags", srv.handleListTags)
				r.Post("/organizations/{organization}/tags", srv.handleStoreTag)
				r.Put("/organizations/{organization}/tags/{tag}", srv.handleUpdateTag)
				r.Delete("/organizations/{organization}/tags/{tag}", srv.handleDestroyTag)

				r.Get("/organizations/{organization}/tasks", srv.handleListTasks)
				r.Post("/organizations/{organization}/tasks", srv.handleStoreTask)
				r.Put("/organizations/{organization}/tasks/{task}", srv.handleUpdateTask)
				r.Delete("/organizations/{organization}/tasks/{task}", srv.handleDestroyTask)

				r.Get("/organizations/{organization}/reports", srv.handleListReports)
				r.Get("/organizations/{organization}/reports/{report}", srv.handleShowReport)
				r.Post("/organizations/{organization}/reports", srv.handleStoreReport)
				r.Put("/organizations/{organization}/reports/{report}", srv.handleUpdateReport)
				r.Delete("/organizations/{organization}/reports/{report}", srv.handleDeleteReport)

				r.Get("/organizations/{organization}/goals", srv.handleIndexGoals)
				r.Get("/organizations/{organization}/goals/{goal}", srv.handleShowGoal)
				r.Post("/organizations/{organization}/goals", srv.handleStoreGoal)
				r.Put("/organizations/{organization}/goals/{goal}", srv.handleUpdateGoal)
				r.Delete("/organizations/{organization}/goals/{goal}", srv.handleDeleteGoal)

				// Time entries — order matters: literal segments before {timeEntry}.
				r.Get("/organizations/{organization}/time-entries", srv.handleListTimeEntries)
				r.Get("/organizations/{organization}/time-entries/export", srv.handleExportTimeEntries)
				r.Get("/organizations/{organization}/time-entries/aggregate", srv.handleAggregateTimeEntries)
				r.Get("/organizations/{organization}/time-entries/aggregate/export", srv.handleAggregateExportTimeEntries)
				r.Post("/organizations/{organization}/time-entries", srv.handleStoreTimeEntry)
				r.Patch("/organizations/{organization}/time-entries", srv.handleUpdateMultipleTimeEntries)
				r.Delete("/organizations/{organization}/time-entries", srv.handleDestroyMultipleTimeEntries)
				r.Put("/organizations/{organization}/time-entries/{timeEntry}", srv.handleUpdateTimeEntry)
				r.Delete("/organizations/{organization}/time-entries/{timeEntry}", srv.handleDestroyTimeEntry)

				r.Get("/users/me/time-entries/active", srv.handleMyActiveTimeEntry)

				// Dashboard charts.
				r.Get("/organizations/{organization}/charts/weekly-project-overview", srv.handleWeeklyProjectOverview)
				r.Get("/organizations/{organization}/charts/latest-tasks", srv.handleLatestTasks)
				r.Get("/organizations/{organization}/charts/last-seven-days", srv.handleLastSevenDays)
				r.Get("/organizations/{organization}/charts/latest-team-activity", srv.handleLatestTeamActivity)
				r.Get("/organizations/{organization}/charts/daily-tracked-hours", srv.handleDailyTrackedHours)
				r.Get("/organizations/{organization}/charts/total-weekly-time", srv.handleTotalWeeklyTime)
				r.Get("/organizations/{organization}/charts/total-weekly-billable-time", srv.handleTotalWeeklyBillableTime)
				r.Get("/organizations/{organization}/charts/total-weekly-billable-amount", srv.handleTotalWeeklyBillableAmount)
				r.Get("/organizations/{organization}/charts/weekly-history", srv.handleWeeklyHistory)
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

		// Invitation accept links — public like upstream; 'signed' on the
		// legacy route (absolute URL), 'signed:relative' on the current one.
		r.Get("/team-invitations/{invitation}", func(w http.ResponseWriter, r *http.Request) {
			srv.handleAcceptInvitation(w, r, false)
		})
		r.Get("/organization-invitations/{invitation}", func(w http.ResponseWriter, r *http.Request) {
			srv.handleAcceptInvitation(w, r, true)
		})

		// Public shared report page (upstream web.php /shared-report).
		r.Get("/shared-report", srv.handleSharedReportPage)

		// Signed export downloads — Laravel local-disk temporaryUrl port.
		r.Get("/exports/{file}", srv.handleExportDownload)

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
			r.With(srv.requireVerified).Get("/time", srv.renderPage("Time"))
			r.With(srv.requireVerified).Get("/calendar", srv.renderPage("Calendar"))
			r.With(srv.requireVerified).Get("/timesheet", srv.renderPage("Timesheet"))
			r.With(srv.requireVerified).Get("/reporting", srv.renderPage("Reporting"))
			r.With(srv.requireVerified).Get("/reporting/detailed", srv.renderPage("ReportingDetailed"))
			r.With(srv.requireVerified).Get("/reporting/shared", srv.renderPage("ReportingShared"))
			r.With(srv.requireVerified).Get("/goals", srv.renderPage("Goals"))
			r.With(srv.requireVerified).Get("/projects", srv.renderPage("Projects"))
			r.With(srv.requireVerified).Get("/projects/{project}", srv.renderPage("ProjectShow"))
			r.With(srv.requireVerified).Get("/clients", srv.renderPage("Clients"))
			r.With(srv.requireVerified).Get("/members", srv.handleMembersPage)
			r.With(srv.requireVerified).Get("/tags", srv.renderPage("Tags"))
			r.With(srv.requireVerified).Get("/import", srv.renderPage("Import"))
			r.With(srv.requireVerified).Get("/organizations/create", srv.handleTeamCreatePage)
			r.With(srv.requireVerified).Get("/organizations/{organizationId}", srv.handleTeamShowPage)
			r.With(srv.requireVerified).Get("/teams/create", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/organizations/create", http.StatusFound)
			})
			r.With(srv.requireVerified).Get("/teams/{organizationId}", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/organizations/"+chi.URLParam(r, "organizationId"), http.StatusFound)
			})
			r.Delete("/user/other-browser-sessions", srv.handleLogoutOtherSessions)
			r.Get("/users/{user}/verify-email-change", srv.handleVerifyEmailChange)
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
