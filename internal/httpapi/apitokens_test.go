package httpapi

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ApiTokenResource: id is a string (not a UUID), scopes are decoded from the
// JSON text column and expires_at is null for personal access tokens.
func TestAPITokenResourceShape(t *testing.T) {
	scopes := `["*"]`
	name := "CI"
	row := apiTokenRow{
		ID:        "abc123",
		Name:      &name,
		Scopes:    &scopes,
		Revoked:   false,
		CreatedAt: pgtype.Timestamp{Time: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), Valid: true},
	}
	m := row.resource()

	if m["id"] != "abc123" {
		t.Errorf("id: %v", m["id"])
	}
	if m["name"] != "CI" {
		t.Errorf("name: %v", m["name"])
	}
	if m["revoked"] != false {
		t.Errorf("revoked: %v", m["revoked"])
	}
	if m["created_at"] != "2026-10-25T12:00:00Z" {
		t.Errorf("created_at: %v", m["created_at"])
	}
	if m["expires_at"] != nil {
		t.Errorf("expires_at must be null: %v", m["expires_at"])
	}
	scopesOut, ok := m["scopes"].([]string)
	if !ok || len(scopesOut) != 1 || scopesOut[0] != "*" {
		t.Errorf("scopes: %#v", m["scopes"])
	}
	for key := range m {
		switch key {
		case "id", "name", "revoked", "scopes", "created_at", "expires_at":
		default:
			t.Errorf("unexpected key %s", key)
		}
	}
}

func TestAPITokenResourceNullScopes(t *testing.T) {
	row := apiTokenRow{ID: "x", Revoked: true}
	m := row.resource()
	scopesOut, ok := m["scopes"].([]string)
	if !ok || len(scopesOut) != 0 {
		t.Errorf("null scopes must serialize as []: %#v", m["scopes"])
	}
	if m["name"] != nil {
		t.Errorf("name must be null: %v", m["name"])
	}
	if m["revoked"] != true {
		t.Errorf("revoked: %v", m["revoked"])
	}
}
