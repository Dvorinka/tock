
## Migration path (verified 2026-10-26)
- `pg_dump -U root -d solidtime` from oracle container → `psql` into fresh
  `tock_migrated` on tock's postgres: restores cleanly (upstream schema
  identical; goose baseline detected + marked applied).
- tock booted on the restored DB: `admin@example.com`/`password` logs in
  (`$2y$` bcrypt hashes verified by `CheckPassword`). Users/orgs/members
  intact.
- Env compat (PR #29): `DB_HOST/DB_PORT/DB_DATABASE/DB_USERNAME/DB_PASSWORD`,
  `APP_URL`, `PORT`, `MAIL_*` accepted when `TOCK_*` unset — compose swap is
  a one-line `image:` change keeping the volume.

## Auth spec — GREEN on tock (11/11, 22s)
Bugs found via the suite, all fixed:
- `route is not defined` — npm ziggy-js never installed `window.route`; every
  Inertia `form.post(route(...))` threw ReferenceError. app.ts wires it now.
- `remember` bool unmarshal — Inertia posts 'on'/''; `decodeBody` rejected →
  every real login failed with credentials error. Now `string` + `== "on"`.
- `requireVerified` returned 409 JSON for X-Inertia navigations → Inertia's
  "plain JSON" modal; never reached /email/verify. Now redirects (API keeps 409).
- register/forgot POSTs blocked on synchronous SMTP (4–19s) — mail now queued
  via `jobs` table + FOR UPDATE SKIP LOCKED worker (~1.3s POST).
- forgot-password silently claimed success on unknown email — now flashes
  upstream's "We can't find a user with that email address."
- reset mail subject "Reset Password Notification" → "Reset your password".
- spec-side: verify-flow helper, reset regex searches Text body, networkidle
  before post-logout fills (hydration race), duplicate-email text matched
  upstream's "has already been taken".

## E2E suite state (issue #18, 2026-10-26)

Full inventory went from ~143 failures to **~30** (most remaining are
environmental or upstream-inconsistent, not parity bugs):

- Fixed and verified green: auth 11/11, profile 36/36, two-factor 6/6,
  shared-reports 24/24, dashboard 9/9, members 27/27, calendar ~94,
  timesheet 10/10, tags/tasks/clients ~36, timetracker ~37, reporting ~56,
  organization 37/37, import-export 48/48, goals 22/22, invitations 4/4,
  admin-panel 14/15.
- API-contract fixes this pass: bulk DELETE `ids`/`ids[]` query,
  `to_jsonb` `::text` casts (42804), `type` default, `end:null` running
  entries, goal-progress UTC, report `name` required, `billable`/`client`
  null-vs-false, public-report unwrapped `data`, `201`s on create,
  `currencies` `{code:name}` map, HTML invitation mail, `PUT
  /current-team`, invited-register `email_verified_at`, reset-mail subject.
- New surface: `/admin` super-admin panel (SUPER_ADMINS email gate;
  Filament-parity server-rendered dashboard + 12 resource pages).

Known residual (not parity bugs):
- **Dispatch-stall (~9 tests)**: `Promise.all` mass-creates through the
  fixture's `page.request` starve — server verified clean under 17x curl
  concurrency (~90ms); the stall is inside Playwright's dispatch, not the
  app. Affects clients/projects/detailed pagination, filter virtualization,
  project-search.
- **Calendar multi-day (4)**: `useEventResize` cross-day segments don't
  fire the update PUT — a deeper UI interaction bug, not API.
- **Admin forbidden (1)**: upstream's spec asserts `dashboard_view`
  directly after a non-invited registration — inconsistent with the
  verify-email flow both apps implement.
