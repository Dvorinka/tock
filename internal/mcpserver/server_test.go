package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dvorinka/tock/internal/tockapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerListsToolsAndCallsRESTTool(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pat" {
			t.Errorf("authorization = %q", got)
		}
		if r.URL.Path != "/api/v1/organizations/org-1/projects" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"project-1"}]}`))
	}))
	defer api.Close()

	apiClient, err := tockapi.New(api.URL, "pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(apiClient)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 9 {
		t.Fatalf("tool count = %d, want 9", len(tools.Tools))
	}
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "projects_list",
		Arguments: map[string]any{"organization_id": "org-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "{\n  \"data\": [\n    {\n      \"id\": \"project-1\"\n    }\n  ]\n}" {
		t.Fatalf("tool response = %#v", result.Content)
	}
}
