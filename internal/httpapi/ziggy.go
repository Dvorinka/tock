package httpapi

import (
	"encoding/json"
	"html/template"
	"sync"
)

// ziggyParameter is one URL parameter of a route, in the shape Ziggy expects.
type ziggyParameter struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// ziggyRoute is one entry of the Ziggy route manifest — the payload Laravel's
// `@routes` directive injects into the SPA shell.
type ziggyRoute struct {
	URI        string            `json:"uri"`
	Methods    []string          `json:"methods"`
	Parameters []ziggyParameter  `json:"parameters,omitempty"`
	Bindings   map[string]string `json:"bindings,omitempty"`
}

func webRoute(methods []string, uri string, params ...string) ziggyRoute {
	r := ziggyRoute{URI: uri, Methods: methods}
	for _, p := range params {
		r.Parameters = append(r.Parameters, ziggyParameter{Name: p, Required: true})
	}
	return r
}

// spaRouteManifest mirrors the named web routes the upstream SPA resolves via
// `route()` / `route().current()`. Names and URIs follow Laravel's route list;
// only the routes the frontend actually asks for are listed.
//
//nolint:gochecknoglobals // static route table
var spaRouteManifest = map[string]ziggyRoute{
	// application pages
	"dashboard":          webRoute([]string{"GET", "HEAD"}, "/"),
	"time":               webRoute([]string{"GET", "HEAD"}, "/time"),
	"calendar":           webRoute([]string{"GET", "HEAD"}, "/calendar"),
	"timesheet":          webRoute([]string{"GET", "HEAD"}, "/timesheet"),
	"reporting":          webRoute([]string{"GET", "HEAD"}, "/reporting"),
	"reporting.detailed": webRoute([]string{"GET", "HEAD"}, "/reporting/detailed"),
	"reporting.shared":   webRoute([]string{"GET", "HEAD"}, "/reporting/shared"),
	"goals":              webRoute([]string{"GET", "HEAD"}, "/goals"),
	"projects":           webRoute([]string{"GET", "HEAD"}, "/projects"),
	"projects.show":      webRoute([]string{"GET", "HEAD"}, "/projects/{project}", "project"),
	"clients":            webRoute([]string{"GET", "HEAD"}, "/clients"),
	"members":            webRoute([]string{"GET", "HEAD"}, "/members"),
	"tags":               webRoute([]string{"GET", "HEAD"}, "/tags"),
	"invoices":           webRoute([]string{"GET", "HEAD"}, "/invoices"),
	"audit-log":          webRoute([]string{"GET", "HEAD"}, "/audit-log"),
	"import":             webRoute([]string{"GET", "HEAD"}, "/import"),
	"shared-report":      webRoute([]string{"GET", "HEAD"}, "/shared-report"),

	// authentication
	"login":            webRoute([]string{"GET", "HEAD"}, "/login"),
	"logout":           webRoute([]string{"POST"}, "/logout"),
	"register":         webRoute([]string{"GET", "HEAD"}, "/register"),
	"password.request": webRoute([]string{"GET", "HEAD"}, "/forgot-password"),
	"password.email":   webRoute([]string{"POST"}, "/forgot-password"),
	"password.reset":   webRoute([]string{"GET", "HEAD"}, "/reset-password/{token}", "token"),
	"password.update":  webRoute([]string{"POST"}, "/reset-password"),
	"password.confirm": webRoute([]string{"GET", "HEAD"}, "/user/confirm-password"),
	"password.confirmation": webRoute(
		[]string{"GET", "HEAD"}, "/user/confirmed-password-status",
	),
	"verification.notice": webRoute([]string{"GET", "HEAD"}, "/email/verify"),
	"verification.verify": webRoute(
		[]string{"GET", "HEAD"}, "/email/verify/{id}/{hash}", "id", "hash",
	),
	"verification.send": webRoute([]string{"POST"}, "/email/verification-notification"),
	"two-factor.login":  webRoute([]string{"GET", "HEAD"}, "/two-factor-challenge"),

	// two factor management
	"two-factor.enable":               webRoute([]string{"POST"}, "/user/two-factor-authentication"),
	"two-factor.confirm":              webRoute([]string{"POST"}, "/user/confirmed-two-factor-authentication"),
	"two-factor.disable":              webRoute([]string{"DELETE"}, "/user/two-factor-authentication"),
	"two-factor.qr-code":              webRoute([]string{"GET", "HEAD"}, "/user/two-factor-qr-code"),
	"two-factor.secret-key":           webRoute([]string{"GET", "HEAD"}, "/user/two-factor-secret-key"),
	"two-factor.recovery-codes":       webRoute([]string{"GET", "HEAD"}, "/user/two-factor-recovery-codes"),
	"user-profile-information.update": webRoute([]string{"PUT"}, "/user/profile-information"),
	"user-password.update":            webRoute([]string{"PUT"}, "/user/password"),
	"profile.show":                    webRoute([]string{"GET", "HEAD"}, "/user/profile"),
	"other-browser-sessions.destroy":  webRoute([]string{"DELETE"}, "/user/other-browser-sessions"),

	// organizations (upstream Jetstream team routes)
	"organizations.create": webRoute([]string{"GET", "HEAD"}, "/organizations/create"),
	"organizations.show": webRoute(
		[]string{"GET", "HEAD"}, "/organizations/{organizationId}", "organizationId",
	),
	"teams.create": webRoute([]string{"GET", "HEAD"}, "/organizations/create"),
	"teams.show": webRoute(
		[]string{"GET", "HEAD"}, "/organizations/{organizationId}", "organizationId",
	),
}

// ziggyJS is the Ziggy route config injected into the SPA shell. It replaces
// the upstream `@routes` Blade directive: without it every `route('…')` call
// throws "route is not in the route list".
//
//nolint:gochecknoglobals // computed once from the static table
var ziggyJS = sync.OnceValue(func() template.JS {
	payload, err := json.Marshal(map[string]any{
		"url":      "",
		"port":     nil,
		"defaults": map[string]any{},
		"routes":   spaRouteManifest,
	})
	if err != nil {
		return template.JS(`{"url":"","port":null,"defaults":{},"routes":{}}`) //nolint:gosec // constant
	}
	return template.JS(payload) //nolint:gosec // JSON we produced
})
