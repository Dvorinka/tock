package oauth2

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

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

func TestScopesCover(t *testing.T) {
	if !scopesCover([]string{"read"}, nil) {
		t.Fatal("empty request should not need extra consent")
	}
	if !scopesCover([]string{"*"}, []string{"delete"}) {
		t.Fatal("wildcard should cover delete")
	}
	if !scopesCover([]string{"read", "update"}, []string{"read"}) {
		t.Fatal("subset should be covered")
	}
	if scopesCover([]string{"read"}, []string{"read", "delete"}) {
		t.Fatal("missing delete should require consent")
	}
}

func TestPKCEMatches(t *testing.T) {
	verifier := "desktop-verifier-value"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !pkceMatches("S256", challenge, verifier) {
		t.Fatal("S256 verifier should match")
	}
	if pkceMatches("S256", challenge, "other") {
		t.Fatal("wrong S256 verifier should fail")
	}
	if !pkceMatches("plain", verifier, verifier) {
		t.Fatal("plain verifier should match")
	}
	if pkceMatches("plain", verifier, "") {
		t.Fatal("empty plain verifier should fail")
	}
	if !pkceMatches("", challenge, verifier) {
		t.Fatal("omitted method should still accept S256")
	}
	if !pkceMatches("", "", "") {
		t.Fatal("no challenge should allow the exchange")
	}
	if pkceMatches("S256", challenge, "") {
		t.Fatal("missing verifier should fail when a challenge was stored")
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
