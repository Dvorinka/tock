// Package mcpserver exposes tock's REST API as MCP tools over stdio.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Dvorinka/tock/internal/tockapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type organizationArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"organization UUID"`
}

type startTimerArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"organization UUID"`
	Description    string `json:"description,omitempty" jsonschema:"optional time entry description"`
}

type listEntriesArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"organization UUID"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum number of entries (1 to 500, default 100)"`
	Offset         int    `json:"offset,omitempty" jsonschema:"number of entries to skip"`
	Start          string `json:"start,omitempty" jsonschema:"optional start-time filter in RFC3339 format"`
	End            string `json:"end,omitempty" jsonschema:"optional end-time filter in RFC3339 format"`
}

type createEntryArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"organization UUID"`
	Start          string `json:"start" jsonschema:"start timestamp in RFC3339 format"`
	End            string `json:"end,omitempty" jsonschema:"optional end timestamp; omit to create a running timer"`
	Description    string `json:"description,omitempty" jsonschema:"optional description"`
}

type deleteEntryArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"organization UUID"`
	EntryID        string `json:"entry_id" jsonschema:"time entry UUID"`
}

func NewServer(client *tockapi.Client) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "tock", Version: "0.1.0"}, nil)
	addTool(server, "timer_status", "Get the current user's running timer. Returns null data when no timer is active.", func(ctx context.Context, _ struct{}) (json.RawMessage, error) {
		data, _, err := client.Request(ctx, http.MethodGet, "users/me/time-entries/active", nil)
		if apiStatus(err) == http.StatusNotFound {
			return json.RawMessage(`{"data":null}`), nil
		}
		return data, err
	})
	addTool(server, "timer_start", "Start a work timer in an organization the user belongs to.", func(ctx context.Context, args startTimerArgs) (json.RawMessage, error) {
		membership, err := client.Membership(ctx, args.OrganizationID)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"member_id": membership.ID, "start": utcNow(), "end": nil, "billable": false, "type": "work", "description": nullIfEmpty(args.Description)}
		data, _, err := client.Request(ctx, http.MethodPost, tockapi.OrgPath(membership.Organization.ID, "time-entries"), body)
		return data, err
	})
	addTool(server, "timer_stop", "Stop the current user's running timer in an organization.", func(ctx context.Context, args organizationArgs) (json.RawMessage, error) {
		membership, err := client.Membership(ctx, args.OrganizationID)
		if err != nil {
			return nil, err
		}
		active, _, err := client.Request(ctx, http.MethodGet, "users/me/time-entries/active", nil)
		if err != nil {
			if apiStatus(err) == http.StatusNotFound {
				return nil, fmt.Errorf("there is no active timer to stop")
			}
			return nil, err
		}
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(active, &response); err != nil || response.Data.ID == "" {
			return nil, fmt.Errorf("active timer response did not contain an entry ID")
		}
		data, _, err := client.Request(ctx, http.MethodPut, tockapi.OrgPath(membership.Organization.ID, "time-entries/"+url.PathEscape(response.Data.ID)), map[string]string{"end": utcNow()})
		return data, err
	})
	addTool(server, "entries_list", "List an organization's time entries with optional date range and pagination.", func(ctx context.Context, args listEntriesArgs) (json.RawMessage, error) {
		if args.Limit == 0 {
			args.Limit = 100
		}
		if args.Limit < 1 || args.Limit > 500 || args.Offset < 0 {
			return nil, fmt.Errorf("limit must be 1 to 500 and offset must be non-negative")
		}
		query := url.Values{"limit": {fmt.Sprint(args.Limit)}, "offset": {fmt.Sprint(args.Offset)}}
		if args.Start != "" {
			query.Set("start", args.Start)
		}
		if args.End != "" {
			query.Set("end", args.End)
		}
		data, _, err := client.Request(ctx, http.MethodGet, tockapi.OrgPath(args.OrganizationID, "time-entries")+"?"+query.Encode(), nil)
		return data, err
	})
	addTool(server, "entries_create", "Create a time entry. Omit end to create a running timer.", func(ctx context.Context, args createEntryArgs) (json.RawMessage, error) {
		if args.Start == "" {
			return nil, fmt.Errorf("start is required")
		}
		membership, err := client.Membership(ctx, args.OrganizationID)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"member_id": membership.ID, "start": args.Start, "end": nullIfEmpty(args.End), "billable": false, "type": "work", "description": nullIfEmpty(args.Description)}
		data, _, err := client.Request(ctx, http.MethodPost, tockapi.OrgPath(membership.Organization.ID, "time-entries"), body)
		return data, err
	})
	addTool(server, "entries_delete", "Delete a time entry the user is permitted to remove.", func(ctx context.Context, args deleteEntryArgs) (json.RawMessage, error) {
		if args.EntryID == "" {
			return nil, fmt.Errorf("entry_id is required")
		}
		data, _, err := client.Request(ctx, http.MethodDelete, tockapi.OrgPath(args.OrganizationID, "time-entries/"+url.PathEscape(args.EntryID)), nil)
		return data, err
	})
	for _, resource := range []string{"projects", "clients", "reports"} {
		name := resource
		addTool(server, name+"_list", "List an organization's "+resource+".", func(ctx context.Context, args organizationArgs) (json.RawMessage, error) {
			return requestOrgList(ctx, client, args.OrganizationID, name)
		})
	}
	return server
}

func addTool[Args any](server *mcp.Server, name, description string, run func(context.Context, Args) (json.RawMessage, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, args Args) (*mcp.CallToolResult, any, error) {
		data, err := run(ctx, args)
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
		}
		return textResult(data), nil, nil
	})
}

func requestOrgList(ctx context.Context, client *tockapi.Client, organizationID, resource string) (json.RawMessage, error) {
	data, _, err := client.Request(ctx, http.MethodGet, tockapi.OrgPath(organizationID, resource), nil)
	return data, err
}

func textResult(data json.RawMessage) *mcp.CallToolResult {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err == nil {
		data = json.RawMessage(pretty.Bytes())
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func utcNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func apiStatus(err error) int {
	var apiErr *tockapi.APIError
	if err != nil && errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}
