
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
