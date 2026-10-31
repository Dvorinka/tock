package oauth2

import "testing"

func TestScopeAllows(t *testing.T) {
	tests := []struct {
		name     string
		scopes   []string
		required string
		want     bool
	}{
		{name: "matching scope", scopes: []string{"read"}, required: "read", want: true},
		{name: "wildcard", scopes: []string{"*"}, required: "delete", want: true},
		{name: "missing scope", scopes: []string{"read"}, required: "update", want: false},
		{name: "empty scopes", required: "read", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeAllows(tt.scopes, tt.required); got != tt.want {
				t.Fatalf("ScopeAllows(%v, %q) = %t, want %t", tt.scopes, tt.required, got, tt.want)
			}
		})
	}
}

func TestClaimScopes(t *testing.T) {
	got := claimScopes([]any{"read", 42, "update"})
	if len(got) != 2 || got[0] != "read" || got[1] != "update" {
		t.Fatalf("claimScopes() = %#v", got)
	}
	if got := claimScopes("read"); len(got) != 0 {
		t.Fatalf("claimScopes() for non-array = %#v", got)
	}
}

func TestIsKnownScope(t *testing.T) {
	for _, scope := range []string{"create", "read", "update", "delete"} {
		if !isKnownScope(scope) {
			t.Errorf("isKnownScope(%q) = false", scope)
		}
	}
	if isKnownScope("admin") {
		t.Error("isKnownScope(admin) = true")
	}
}
