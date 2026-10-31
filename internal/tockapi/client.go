// Package tockapi contains the PAT-authenticated REST client shared by the CLI
// and MCP adapters.
package tockapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

type APIError struct {
	StatusCode int
	Status     string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tock API %s: %s", e.Status, e.Message)
}

type Membership struct {
	ID           string `json:"id"`
	Organization struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"organization"`
}

func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("TOCK_API_URL"), "/")
	}
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("TOCK_URL"), "/")
	}
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("APP_URL"), "/")
	}
	if baseURL == "" {
		return nil, errors.New("set --api-url or TOCK_API_URL to the tock server URL")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("API URL must be an absolute http or https URL without user info")
	}
	if strings.TrimSpace(token) == "" {
		token = os.Getenv("TOCK_API_TOKEN")
	}
	if strings.TrimSpace(token) == "" {
		token = os.Getenv("TOCK_TOKEN")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("set --token or TOCK_API_TOKEN to a personal access token")
	}
	return &Client{baseURL: parsed, token: strings.TrimSpace(token), http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *Client) Request(ctx context.Context, method, path string, body any) (json.RawMessage, int, error) {
	endpoint := *c.baseURL
	requestPath, query, _ := strings.Cut(path, "?")
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/" + strings.TrimLeft(requestPath, "/")
	endpoint.RawQuery = query
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request to tock: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		message := problem.Message
		if message == "" {
			message = problem.Error
		}
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, resp.StatusCode, &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Message: message}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage("{}"), resp.StatusCode, nil
	}
	if !json.Valid(data) {
		return nil, resp.StatusCode, errors.New("tock API returned invalid JSON")
	}
	return json.RawMessage(data), resp.StatusCode, nil
}

func (c *Client) Memberships(ctx context.Context) ([]Membership, error) {
	data, _, err := c.Request(ctx, http.MethodGet, "users/me/memberships", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []Membership `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode memberships: %w", err)
	}
	return response.Data, nil
}

func (c *Client) Membership(ctx context.Context, organizationID string) (Membership, error) {
	memberships, err := c.Memberships(ctx)
	if err != nil {
		return Membership{}, err
	}
	if organizationID != "" {
		for _, membership := range memberships {
			if membership.Organization.ID == organizationID {
				return membership, nil
			}
		}
		return Membership{}, fmt.Errorf("no membership found for organization %q", organizationID)
	}
	if len(memberships) == 1 {
		return memberships[0], nil
	}
	if len(memberships) == 0 {
		return Membership{}, errors.New("the authenticated user has no organization memberships")
	}
	return Membership{}, errors.New("--org is required when the user belongs to more than one organization")
}

func OrgPath(organizationID, endpoint string) string {
	return "organizations/" + url.PathEscape(organizationID) + "/" + strings.TrimLeft(endpoint, "/")
}
