package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpDoesNotNeedAPIConfiguration(t *testing.T) {
	var out strings.Builder
	if err := Run([]string{"help"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "timer status|start|stop") || !strings.Contains(out.String(), "--json") {
		t.Fatalf("help missing expected commands/options: %s", out.String())
	}
}

func TestShellCompletionDoesNotNeedAPIConfiguration(t *testing.T) {
	var out strings.Builder
	if err := Run([]string{"completion", "fish"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "complete -c tock") {
		t.Fatalf("fish completion missing command declarations: %s", out.String())
	}
}

func TestTimerStartUsesOrganizationMembershipAndJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pat" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/me/memberships":
			_, _ = w.Write([]byte(`{"data":[{"id":"member-1","organization":{"id":"org-1","name":"One"}}]}`))
		case "/api/v1/organizations/org-1/time-entries":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			if body["member_id"] != "member-1" || body["end"] != nil || body["description"] != "shipping" {
				t.Errorf("request body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"data":{"id":"entry-1"}}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var out strings.Builder
	err := Run([]string{"--api-url", server.URL, "--token", "pat", "--org", "org-1", "timer", "start", "--description", "shipping", "--json"}, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"data":{"id":"entry-1"}}` {
		t.Fatalf("output = %q", got)
	}
}
