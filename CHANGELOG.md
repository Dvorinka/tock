# Changelog

## Unreleased

- Read a solidtime environment file on switch: `APP_KEY`, `GOTENBERG_URL`,
  `DB_SSL_MODE`, `MAIL_FROM_ADDRESS`, and SMTP AUTH (`MAIL_USERNAME` /
  `MAIL_PASSWORD`). Database passwords are escaped in the DSN.
- `GET /health-check/up` for the upstream compose health path.
- README: in-place switch from the official Docker self-hosting examples,
  plus a one-file new install.
- Decision recorded in `docs/react-migration-plan.md`: the Vue SPA stays.
