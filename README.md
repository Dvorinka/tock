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
- Importers: Toggl, Clockify, Harvest, generic CSV, solidtime exports, with
  queued progress for SPA imports
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
local test suite. Login is limited to 10 requests per IP per 15 minutes
(`TOCK_RATE_LIMIT_LOGIN`). A full browser suite from one machine needs both raised.

`GET /health-check/up` returns `200` and `OK`. Probe it from outside the
container. The image is distroless and has no shell or curl.

## Switch from solidtime

The upstream install guide is
[Docker self-hosting](https://docs.solidtime.io/self-hosting/guides/docker).
The compose examples live in
[solidtime-io/self-hosting-examples](https://github.com/solidtime-io/self-hosting-examples).
tock reads that same environment file. Keep Postgres, Gotenberg, and the
file. Remove the PHP app, the scheduler, and the queue worker. Mail and
imports run inside the one tock process.

1. `docker compose down`. Leave the database volume in place.
2. Delete the scheduler and queue services from the example compose file.
3. Replace the `app` service. The solidtime image listens on port 8000 and
   healthchecks itself with curl. tock listens on `PORT` (default 8080) and
   the image cannot run that curl check.

```yaml
  app:
    image: ghcr.io/dvorinka/tock:latest
    restart: always
    ports:
      - "${FORWARD_APP_PORT:-8000}:8000"
    environment:
      PORT: "8000"
      AUTO_DB_MIGRATE: "true"
    volumes:
      - app-storage:/storage
    env_file:
      - laravel.env
    depends_on:
      - database
      - gotenberg
```

4. Leave the environment file as it is. These names are read directly:

| solidtime variable | What tock does with it |
| --- | --- |
| `DB_HOST`, `DB_PORT`, `DB_DATABASE`, `DB_USERNAME`, `DB_PASSWORD` | Builds the Postgres DSN. Passwords are escaped. |
| `DB_SSL_MODE` | `disable` (default), `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. Anything else stays `disable`. |
| `APP_URL` | Public URL for links and mail. |
| `APP_KEY` | Session and signature key. Set it. An empty key changes on every boot and logs everyone out. |
| `PORT` | Port inside the container. |
| `MAIL_HOST`, `MAIL_PORT` | SMTP address. |
| `MAIL_USERNAME`, `MAIL_PASSWORD` | SMTP AUTH when the username is set. Port 587 (STARTTLS) works. Port 465 (implicit TLS) does not. |
| `MAIL_FROM_ADDRESS` | `From` header. |
| `GOTENBERG_URL` | PDF service. The compose examples use `http://gotenberg:3000`. |
| `APP_ENABLE_REGISTRATION` | `true` or `on` opens registration. tock defaults to open when this is unset. solidtime defaults to closed. Set it explicitly. |
| `SUPER_ADMINS` | Comma-separated emails allowed into `/admin`. |
| `AUTO_DB_MIGRATE` | `true` by default. `false` skips migration on boot. |

`TOCK_DB_DSN`, `TOCK_ADDR`, `TOCK_APP_URL`, `TOCK_APP_KEY`, `TOCK_SMTP_ADDR`,
`TOCK_GOTENBERG_URL`, and `TOCK_MAIL_FROM` override the matching solidtime
names when you want a tock-native config.

5. `docker compose up -d`. The first boot recognises an existing solidtime
   schema and applies only the tock additions: invoicing, import-run
   progress, and OAuth PKCE columns. People log in again. Password hashes,
   organizations, time entries, and API tokens stay.

Mount the old storage volume at `/storage`. The Passport private and public
key files already on that volume keep signing API tokens. Desktop and
browser OAuth clients keep the ids already stored in the database.

A dump-and-restore path, and the list of what does not carry over, is in
[docs/migration-from-solidtime.md](docs/migration-from-solidtime.md).

### New install

```yaml
services:
  app:
    image: ghcr.io/dvorinka/tock:latest
    ports: ["8080:8080"]
    environment:
      TOCK_DB_DSN: postgres://tock:tock@database:5432/tock?sslmode=disable
      TOCK_APP_URL: http://localhost:8080
      TOCK_APP_KEY: change-me
      APP_ENABLE_REGISTRATION: "true"
      GOTENBERG_URL: http://gotenberg:3000
      AUTO_DB_MIGRATE: "true"
    depends_on: [database, gotenberg]
  database:
    image: postgres:16
    environment:
      POSTGRES_USER: tock
      POSTGRES_PASSWORD: tock
      POSTGRES_DB: tock
    volumes: [database:/var/lib/postgresql/data]
  gotenberg:
    image: gotenberg/gotenberg:8
volumes:
  database:
```

Open `http://localhost:8080/register`. Put that email in `SUPER_ADMINS` and
recreate the container to open `/admin`.

## Development

```sh
just dev       # postgres, gotenberg, mailpit, minio
just spa       # build the embedded Vue SPA
just serve     # build + run
just verify    # build + lint + tests (pre-push gate)
```

Stack: Go + chi, Postgres + pgx/v5 + sqlc, goose migrations in
`db/migrations/`, the upstream Vue 3 + Inertia SPA served embedded.
