package tockapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestUsesBearerTokenAndVersionedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/projects" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Request(context.Background(), http.MethodGet, OrgPath("org-1", "projects"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipRequiresExplicitOrgWhenUserHasSeveral(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1","organization":{"id":"o1","name":"One"}},{"id":"m2","organization":{"id":"o2","name":"Two"}}]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Membership(context.Background(), ""); err == nil {
		t.Fatal("expected an explicit organization error")
	}
	membership, err := client.Membership(context.Background(), "o2")
	if err != nil || membership.ID != "m2" {
		t.Fatalf("membership = %+v, err = %v", membership, err)
	}
}
