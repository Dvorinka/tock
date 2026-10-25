package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Dvorinka/tock/internal/auth"
)

// Render implements the Inertia server protocol: JSON for X-Inertia requests,
// the HTML shell otherwise, with asset-version negotiation.
func (s *spaHandler) Render(w http.ResponseWriter, r *http.Request, component string, props map[string]any) {
	s.once.Do(s.load)
	if s.loadErr != nil {
		http.Error(w, s.loadErr.Error(), http.StatusInternalServerError)
		return
	}
	merged := s.sharedProps(r)
	for k, v := range props {
		merged[k] = v
	}
	page := inertiaPage{Component: component, Props: merged, URL: r.URL.RequestURI(), Version: s.version}

	if r.Header.Get("X-Inertia") != "" {
		if r.Method == http.MethodGet &&
			r.Header.Get("X-Inertia-Version") != "" &&
			r.Header.Get("X-Inertia-Version") != s.version {
			// Asset version mismatch: force a full reload (inertia-laravel 409).
			w.Header().Set("X-Inertia-Location", absoluteURL(r))
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Inertia", "true")
		w.Header().Set("Vary", "X-Inertia")
		_ = json.NewEncoder(w).Encode(page)
		return
	}

	pageJSON, err := json.Marshal(page)
	if err != nil {
		http.Error(w, "render page", http.StatusInternalServerError)
		return
	}
	var out strings.Builder
	err = shellTemplate.Execute(&out, map[string]any{
		"Title":  "tock",
		"Ziggy":  ziggyJS,
		"Script": s.entryFile,
		"Styles": s.entryCSS,
		"Page":   string(pageJSON),
	})
	if err != nil {
		http.Error(w, "render shell", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(out.String()))
}

func absoluteURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}

// Redirect mirrors inertia-laravel: 303 for non-GET so the browser follows
// with GET; 302 for plain GET redirects.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	code := http.StatusFound
	if r.Method != http.MethodGet {
		code = http.StatusSeeOther
	}
	http.Redirect(w, r, url, code)
}

// sharedProps builds the shared Inertia props for the current request:
// auth user + permissions (current org role), flashed errors and flash data.
func (s *spaHandler) sharedProps(r *http.Request) map[string]any {
	sess := auth.SessionFromContext(r.Context())
	user := auth.UserFromContext(r.Context())

	errors := map[string]any{}
	flash := map[string]any{"message": nil, "bannerText": nil, "bannerStyle": nil}
	if sess != nil {
		for k, v := range sess.PullErrors() {
			errors[k] = v
		}
		if m := sess.Pull("flash.message"); m != nil {
			flash["message"] = m
		}
		if m := sess.Pull("flash.bannerText"); m != nil {
			flash["bannerText"] = m
		}
		if m := sess.Pull("flash.bannerStyle"); m != nil {
			flash["bannerStyle"] = m
		}
	}

	authProps := map[string]any{
		"user":        nil,
		"permissions": []string{},
	}
	if user != nil {
		authProps["user"] = map[string]any{
			"id":                          user.ID,
			"name":                        user.Name,
			"email":                       user.Email,
			"pending_email":               user.PendingEmail,
			"profile_photo_url":           gravatarURL(user.Email),
			"timezone":                    user.Timezone,
			"week_start":                  user.WeekStart,
			"current_organization_id":     user.CurrentOrganizationID,
			"send_time_entry_still_running_email": user.SendStillRunningEmail,
			"two_factor_enabled":          user.TwoFactorSecret != nil && user.TwoFactorConfirmedAt != nil,
		}
		if perms, ok := r.Context().Value(ctxPerms).([]string); ok {
			authProps["permissions"] = perms
		}
	}

	return map[string]any{
		"auth":                    authProps,
		"errors":                  errors,
		"has_billing_extension":   false,
		"has_invoicing_extension": true,
		"has_services_extension":  false,
		"has_goals_extension":     true,
		"billing":                 nil,
		"flash":                   flash,
	}
}

// ctxPerms carries the resolved permission list for the current org member.
type permsKey struct{}

var ctxPerms = permsKey{}
