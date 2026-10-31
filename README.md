# tock

Self-hosted time tracking, invoicing and reporting — the solidtime
feature set rebuilt as a single Go binary. AGPL-3.0.

tock is a fork of [solidtime](https://github.com/solidtime-io/solidtime)
with the Laravel/PHP backend replaced by Go. Everything that upstream
sells as cloud extensions — invoicing (PDF + EN 16931 UBL e-invoice) and
the audit log — is built in, ungated, and shipped to everyone.

## Features

- Time tracking: timer, calendar, timesheet, breaks
- Projects, tasks, clients, tags, billable rates at every level
- Reporting with CSV/XLSX export and public shared reports
- Goals with live progress tracking
- Invoicing: draft → sent → paid lifecycle, VAT/discounts/reverse charge,
  PDF via Gotenberg, UBL e-invoice export
- Audit log of every mutation
- Importers: Toggl, Clockify, Harvest, generic CSV, solidtime exports
- OAuth2 API — the stock solidtime desktop app and browser extension pair
  unmodified

## Run

One container: Postgres external, everything else inside.

```sh
docker run -e TOCK_DB_DSN=postgres://user:pass@host/db ghcr.io/dvorinka/tock migrate
docker run -e TOCK_DB_DSN=postgres://user:pass@host/db -p 8080:8080 ghcr.io/dvorinka/tock
```

Migrating from an existing solidtime install: see
[docs/migration-from-solidtime.md](docs/migration-from-solidtime.md).

Registration is limited to 100 requests per IP per hour by default. Set
`TOCK_RATE_LIMIT_REGISTER_PER_HOUR` to tune that limit for your deployment or
local test suite.

## Development

```sh
just dev       # postgres, gotenberg, mailpit, minio
just spa       # build the embedded Vue SPA
just serve     # build + run
just verify    # build + lint + tests (pre-push gate)
```

Stack: Go + chi, Postgres + pgx/v5 + sqlc, goose migrations in
`db/migrations/`, the upstream Vue 3 + Inertia SPA served embedded.
