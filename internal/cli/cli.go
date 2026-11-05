// Package cli implements the user-facing REST CLI for tock.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/tockapi"
)

type settings struct {
	apiURL string
	token  string
	orgID  string
	json   bool
}

func Run(args []string, stdout, stderr io.Writer) error {
	settings, args, err := parseGlobal(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		writeHelp(stdout)
		return nil
	}
	if args[0] == "completion" {
		return writeCompletion(args[1:], stdout)
	}
	client, err := tockapi.New(settings.apiURL, settings.token)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "timer":
		return runTimer(ctx, client, settings, args[1:], stdout)
	case "entries":
		return runEntries(ctx, client, settings, args[1:], stdout)
	case "projects", "clients", "reports":
		return runList(ctx, client, settings, args[0], args[1:], stdout)
	default:
		return fmt.Errorf("unknown tock command %q; run `tock help`", args[0])
	}
}

func parseGlobal(args []string) (settings, []string, error) {
	s := settings{}
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--json" {
			s.json = true
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "--api-url" && name != "--token" && name != "--org" && name != "--organization" {
			remaining = append(remaining, arg)
			continue
		}
		if !hasValue {
			i++
			if i == len(args) {
				return s, nil, fmt.Errorf("%s requires a value", name)
			}
			value = args[i]
		}
		switch name {
		case "--api-url":
			s.apiURL = value
		case "--token":
			s.token = value
		case "--org", "--organization":
			s.orgID = value
		}
	}
	return s, remaining, nil
}

func writeHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `tock — time tracking from your terminal

Usage:
  tock [--api-url URL] [--token PAT] [--org ID] [--json] <command>

Commands:
  timer status|start|stop          Inspect or control the active timer
  entries list|create|delete       List and manage time entries
  projects list                    List projects
  clients list                     List clients
  reports list                     List saved reports
  completion bash|zsh|fish         Print shell completions

Global options:
  --api-url URL   Server base URL (TOCK_API_URL, TOCK_URL, or APP_URL)
  --token PAT     Personal access token (TOCK_API_TOKEN or TOCK_TOKEN)
  --org ID        Organization ID; required if the user belongs to multiple orgs
  --json          Emit compact JSON; accepted before or after the subcommand
  --help          Show command help
`)
}

func writeCompletion(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: tock completion bash|zsh|fish")
	}
	scripts := map[string]string{
		"bash": `# Add to ~/.bashrc: eval "$(tock completion bash)"
_tock() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=( $(compgen -W "serve migrate seed worker timer entries projects clients reports mcp version help completion status start stop list create delete --api-url --token --org --json --help" -- "$cur") )
}
complete -F _tock tock
`,
		"zsh": `# Add to ~/.zshrc: source <(tock completion zsh)
_tock() {
  _arguments '*:command:((serve migrate seed worker timer entries projects clients reports mcp version help completion status start stop list create delete --api-url --token --org --json --help))'
}
compdef _tock tock
`,
		"fish": `# Save with: tock completion fish > ~/.config/fish/completions/tock.fish
complete -c tock -f -a 'serve migrate seed worker timer entries projects clients reports mcp version help completion status start stop list create delete'
complete -c tock -l api-url -r
complete -c tock -l token -r
complete -c tock -l org -r
complete -c tock -l json
complete -c tock -l help
`,
	}
	script, ok := scripts[args[0]]
	if !ok {
		return fmt.Errorf("unsupported shell %q; choose bash, zsh, or fish", args[0])
	}
	_, err := io.WriteString(out, script)
	return err
}

func runTimer(ctx context.Context, client *tockapi.Client, s settings, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprint(out, "Usage: tock timer status|start|stop [--description TEXT]\n")
		return nil
	}
	switch args[0] {
	case "status", "current":
		data, _, err := client.Request(ctx, http.MethodGet, "users/me/time-entries/active", nil)
		if err != nil {
			var apiErr *tockapi.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				return printJSON(out, json.RawMessage(`{"data":null}`), s.json)
			}
			return err
		}
		return printJSON(out, data, s.json)
	case "start":
		fs := flag.NewFlagSet("timer start", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		description := fs.String("description", "", "time entry description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		membership, err := client.Membership(ctx, s.orgID)
		if err != nil {
			return err
		}
		body := map[string]any{
			"member_id":   membership.ID,
			"start":       time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			"end":         nil,
			"billable":    false,
			"type":        "work",
			"description": nullableString(*description),
		}
		data, _, err := client.Request(ctx, http.MethodPost, tockapi.OrgPath(membership.Organization.ID, "time-entries"), body)
		if err != nil {
			return err
		}
		return printJSON(out, data, s.json)
	case "stop":
		membership, err := client.Membership(ctx, s.orgID)
		if err != nil {
			return err
		}
		active, _, err := client.Request(ctx, http.MethodGet, "users/me/time-entries/active", nil)
		if err != nil {
			var apiErr *tockapi.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				return errors.New("there is no active timer to stop")
			}
			return err
		}
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(active, &response); err != nil || response.Data.ID == "" {
			return errors.New("active timer response did not contain an entry ID")
		}
		data, _, err := client.Request(ctx, http.MethodPut,
			tockapi.OrgPath(membership.Organization.ID, "time-entries/"+url.PathEscape(response.Data.ID)),
			map[string]string{"end": time.Now().UTC().Format("2006-01-02T15:04:05Z")})
		if err != nil {
			return err
		}
		return printJSON(out, data, s.json)
	default:
		return fmt.Errorf("unknown timer command %q", args[0])
	}
}

func runEntries(ctx context.Context, client *tockapi.Client, s settings, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprint(out, "Usage: tock entries list [--from RFC3339] [--to RFC3339] [--limit N] [--offset N]\n       tock entries create --start RFC3339 [--end RFC3339] [--description TEXT]\n       tock entries delete ID\n")
		return nil
	}
	membership, err := client.Membership(ctx, s.orgID)
	if err != nil {
		return err
	}
	base := tockapi.OrgPath(membership.Organization.ID, "time-entries")
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("entries list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		from := fs.String("from", "", "include entries starting at or after RFC3339 timestamp")
		to := fs.String("to", "", "include entries ending at or before RFC3339 timestamp")
		limit := fs.Int("limit", 100, "number of entries (1–500)")
		offset := fs.Int("offset", 0, "number of entries to skip")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *limit < 1 || *limit > 500 || *offset < 0 {
			return errors.New("limit must be 1–500 and offset must be non-negative")
		}
		query := url.Values{"limit": {fmt.Sprint(*limit)}, "offset": {fmt.Sprint(*offset)}}
		if *from != "" {
			query.Set("start", *from)
		}
		if *to != "" {
			query.Set("end", *to)
		}
		data, _, err := client.Request(ctx, http.MethodGet, base+"?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		return printJSON(out, data, s.json)
	case "create":
		fs := flag.NewFlagSet("entries create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		start := fs.String("start", "", "start time in RFC3339 format (required)")
		end := fs.String("end", "", "end time in RFC3339 format; omit to start a timer")
		description := fs.String("description", "", "time entry description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *start == "" {
			return errors.New("--start is required")
		}
		body := map[string]any{
			"member_id":   membership.ID,
			"start":       *start,
			"end":         nullableString(*end),
			"billable":    false,
			"type":        "work",
			"description": nullableString(*description),
		}
		data, _, err := client.Request(ctx, http.MethodPost, base, body)
		if err != nil {
			return err
		}
		return printJSON(out, data, s.json)
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: tock entries delete ID")
		}
		data, _, err := client.Request(ctx, http.MethodDelete, base+"/"+url.PathEscape(args[1]), nil)
		if err != nil {
			return err
		}
		return printJSON(out, data, s.json)
	default:
		return fmt.Errorf("unknown entries command %q", args[0])
	}
}

func runList(ctx context.Context, client *tockapi.Client, s settings, resource string, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] != "list" && args[0] != "--help" && args[0] != "-h" {
		return fmt.Errorf("unknown %s command %q", resource, args[0])
	}
	if len(args) > 0 && args[0] != "list" {
		_, _ = fmt.Fprintf(out, "Usage: tock %s list\n", resource)
		return nil
	}
	membership, err := client.Membership(ctx, s.orgID)
	if err != nil {
		return err
	}
	endpoint := map[string]string{"projects": "projects", "clients": "clients", "reports": "reports"}[resource]
	data, _, err := client.Request(ctx, http.MethodGet, tockapi.OrgPath(membership.Organization.ID, endpoint), nil)
	if err != nil {
		return err
	}
	return printJSON(out, data, s.json)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func printJSON(out io.Writer, data json.RawMessage, compact bool) error {
	if compact {
		_, err := fmt.Fprintln(out, string(data))
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, pretty.String())
	return err
}
