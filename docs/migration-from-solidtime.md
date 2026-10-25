# Migrating from solidtime to tock

tock keeps the upstream database schema identical, so an existing solidtime
Postgres database works unchanged.

## Steps

1. Stop the solidtime app (PHP-FPM / Octane / FrankenPHP workers).
2. Dump the database: `pg_dump -Fc solidtime > backup.dump` (or run tock
   against the live database — it is additive only).
3. Point tock at the same Postgres: `TOCK_DB_DSN=postgres://user:pass@host/solidtime?sslmode=disable`.
4. Run `tock migrate`. Existing upstream dumps are detected (the `users` table
   exists and `goose_db_version` does not) and the baseline migration is marked
   applied; only the invoicing tables are created on top.
5. Run `tock serve`. The SPA is embedded; no PHP, queue workers, or scheduler
   processes remain.

## What carries over

- Users, organizations, members, roles, invitations
- Password hashes (bcrypt/argon2id verify unchanged — existing logins work)
- Sessions are Laravel-shaped but stored per-driver; users log in again
- OAuth clients, access tokens, personal access tokens (token hashes match
  Passport semantics; the stock solidtime desktop app pairs unmodified)
- Time entries, projects, tasks, clients, tags, reports, goals
- Organization export ZIPs import through the `solidtime` importer

## Differences

- Invoicing and the audit log are built in and ungated.
- Filament admin and the Laravel extension system do not exist; nothing needs
  them.
- Background jobs (report recalculation, mail) run in-process inside `tock
  serve` against the `jobs` table — no Redis, no separate worker.
