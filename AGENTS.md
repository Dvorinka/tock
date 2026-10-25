# AGENTS.md — tock

Fork of `solidtime-io/solidtime` (AGPL-3.0) rebuilt as a single Go binary.
The PHP tree was deleted in Phase 9 — `openapi.json`, `db/migrations/` and
`PLAN.md` carry the spec now.

## Principles

1. **Port, don't redesign.** Behavior mirrors the deleted upstream PHP —
   consult `git log`/upstream for reference, `openapi.json` for the contract.
2. **Schema is sacred.** Table/column names stay identical to upstream — a
   real upstream `pg_dump` must restore and run on tock unchanged.
3. **No gates.** No plan checks, license checks, subscriptions, or feature
   flags by tier. Invoicing and audit log are built open, in-tree.
4. **Single binary.** API, worker loop, migrations ship in `tock`. Postgres
   is the job queue (`FOR UPDATE SKIP LOCKED`). No Redis, no extensions.
5. **Frontend stays.** `resources/js` is the upstream Vue 3 + Inertia SPA.
   Serve it, don't rewrite it — implement the Inertia server protocol.

## Stack

Go 1.24+, chi, pgx/v5 + sqlc, goose (`db/migrations/`), `slog`, Gotenberg
(PDF), excelize (XLSX), stdlib csv. Dev deps via docker compose: pgsql,
pgsql_test, gotenberg, mailpit, minio.

## Conventions

- Errors: typed in `internal/domain`, wrapped `fmt.Errorf("…: %w")` at edges.
- Migrations: numbered SQL, idempotent, never edit applied ones.
- No secrets or `.env` commits. No new deps without checking `go.mod` first.
- Commits: conventional (`feat(api): …`), trailer
  `Authored By: TDvorak <info@tdvorak.dev>`. Feature branch + PR to `main`.

## Verification

- `just verify` (= `go build ./...`, `go test`, `golangci-lint run`) before
  every push.
- Dev deps: `just dev` (docker-compose.dev.yml). `just spa` rebuilds the
  embedded SPA bundle (npm build → `internal/web/dist`). `just migrate`,
  `just seed`, `just sqlc`.
- API parity = diff generated spec vs committed `openapi.json` snapshot.
- Upstream runs locally via `docker compose` — it is the behavior oracle.
