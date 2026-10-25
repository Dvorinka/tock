package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// spaRouteNames are the route names the Vue SPA calls via route() or
// route().current(). Ziggy throws for unknown names, so the manifest has to
// cover every one of them.
var spaRouteNames = []string{ //nolint:gochecknoglobals // test fixture
	"dashboard", "time", "calendar", "timesheet", "reporting", "reporting.detailed",
	"reporting.shared", "goals", "projects", "projects.show", "clients", "members",
	"tags", "invoices", "import", "login", "logout", "register", "password.request",
	"password.email", "password.update", "password.confirm", "password.confirmation",
	"two-factor.login", "two-factor.enable", "two-factor.confirm", "two-factor.disable",
	"two-factor.qr-code", "two-factor.secret-key", "two-factor.recovery-codes",
	"user-password.update", "verification.send", "profile.show",
	"other-browser-sessions.destroy", "organizations.create", "organizations.show",
}

func TestZiggyManifestCoversSPARoutes(t *testing.T) {
	for _, name := range spaRouteNames {
		route, ok := spaRouteManifest[name]
		if !ok {
			t.Fatalf("ziggy manifest is missing route %q", name)
		}
		if route.URI == "" || len(route.Methods) == 0 {
			t.Fatalf("route %q has no uri or methods: %+v", name, route)
		}
	}
}

func TestZiggyManifestParametersMatchURI(t *testing.T) {
	for name, route := range spaRouteManifest {
		for _, param := range route.Parameters {
			if !strings.Contains(route.URI, "{"+param.Name+"}") {
				t.Fatalf("route %q: parameter %q is not in uri %q", name, param.Name, route.URI)
			}
		}
	}
}

func TestZiggyJSONIsValidConfig(t *testing.T) {
	var config struct {
		URL    string                     `json:"url"`
		Routes map[string]json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal([]byte(ziggyJS()), &config); err != nil {
		t.Fatalf("ziggy config is not valid JSON: %v", err)
	}
	if len(config.Routes) != len(spaRouteManifest) {
		t.Fatalf("expected %d routes, got %d", len(spaRouteManifest), len(config.Routes))
	}
}
