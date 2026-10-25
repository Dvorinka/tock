
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
