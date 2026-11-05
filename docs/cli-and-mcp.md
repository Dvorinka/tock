# CLI and MCP

The `tock` binary can call a running tock server with a personal access token.
The API host and token can be passed on a command or provided through
environment variables:

```sh
export TOCK_API_URL=https://tock.example.com
export TOCK_API_TOKEN='<personal-access-token>'
tock --org '<organization-id>' timer status
```

`--api-url`, `--token`, and `--org` can also be passed as flags. `--org` may be
omitted when the authenticated user has exactly one organization membership.
When there are several memberships, an explicit organization ID is required.
API output is pretty-printed by default; pass `--json` anywhere in the command
to get compact JSON suitable for scripts.

## Commands

```text
tock timer status
tock timer start [--description TEXT]
tock timer stop
tock entries list [--from RFC3339] [--to RFC3339] [--limit N] [--offset N]
tock entries create --start RFC3339 [--end RFC3339] [--description TEXT]
tock entries delete ID
tock projects list
tock clients list
tock reports list
tock completion bash|zsh|fish
tock --version
```

`timer start` creates a running work entry, and `timer stop` ends the active
entry. `entries create` can create either a completed entry or a running one
when `--end` is omitted. Server permission checks remain authoritative.

## MCP stdio server

Configure `TOCK_API_URL` and `TOCK_API_TOKEN`, then run `tock mcp` as the
command for an MCP host. The server uses the official Go SDK and stdio
transport. It exposes `timer_status`, `timer_start`, `timer_stop`,
`entries_list`, `entries_create`, `entries_delete`, `projects_list`,
`clients_list`, and `reports_list`. Organization-scoped tools require an
`organization_id`; the user's personal access token still controls access.

The server supports the current MCP protocol and legacy protocol revisions
through the official SDK. Logs and diagnostics belong on stderr; stdout is
reserved for MCP messages.
