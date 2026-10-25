# Next-agent prompt — tock phase 2: parity audit, restyle, improve

Paste this into a fresh Devin session. Work dir: `/home/tdvorak/Desktop/PROG+HTML/tock`.

---

You are taking over `tock` — a complete Go rewrite of the solidtime
time-tracking app (Vue 3 + Inertia SPA retained). Phases 0–9 are DONE:
single binary, Postgres schema identical to upstream, OAuth2 API,
invoicing (PDF + UBL), audit log, all ten importers, Docker + goreleaser.

## State you inherit

- PR chain #1–#14 on `Dvorinka/tock`, stacked, unmerged. `main` is still
  the upstream PHP app. The full Go rewrite lives at branch tip
  `feat/phase9-ship`.
- First job: merge the chain in order (#1 → #14). Each merge retargets
  the next PR automatically since every base is the previous head. If
  `gh pr merge` hits review rules, merge in order locally or use
  `gh pr merge --squash --admin`. After each merge the next PR's base
  becomes `main` on GitHub — verify with `gh pr view N --repo Dvorinka/tock`.
- Alternatively: fast-forward locally (`git checkout main && git merge
  feat/phase9-ship`) after confirming the chain is linear — but keep the
  PR chain history, don't squash-destroy the record.

## Environment

- `just dev` → dev deps: pgsql :54329, pgsql_test :54330, gotenberg :3100,
  mailpit :8025/10025, minio :9000.
- `just spa` rebuilds the embedded Vue bundle → `internal/web/dist`.
- `just build` / `just serve` / `just verify` (build+test+lint = pre-push
  gate, non-negotiable).
- Seed login: `admin@example.com` / `password`.
- Env in `.envrc` (`direnv allow`). `TOCK_DB_DSN`, `TOCK_TEST_DSN`,
  `TOCK_ADDR`, `TOCK_STORAGE_DIR`, `TOCK_OAUTH_KEY_DIR`, `TOCK_APP_URL`.
- Gotcha: kill the server by PID (`lsof -ti :PORT | xargs kill`) —
  `pkill -f "tock serve"` self-matches.
- Gotcha: uuid.NewV7() first-8-chars collide within a millisecond —
  use tail bytes for test uniqueness.
- Session cookies flush only through the `sessionWriter` wrapper —
  don't write headers before it.

## Mission, in order

### 1. Stand up the upstream oracle

The PHP tree was deleted in phase 9. Restore the oracle WITHOUT touching
this repo:

```sh
git clone --depth 1 https://github.com/solidtime-io/solidtime /tmp/solidtime-oracle
cd /tmp/solidtime-oracle && docker compose up -d
```

Upstream ports (80, 5433, etc. — check its docker-compose.yml) vs tock's
dev ports. Seed it (`php artisan db:seed` or its own seeder), get it
serving on localhost.

### 2. Feature-by-feature parity audit — one feature at a time

Run BOTH apps side by side (oracle :80, tock :8080 or :18099). Use
Playwright (`mcp-playwright`) or the cua-driver to drive real browsers;
screenshot every page on both.

For EACH feature surface, compare and log findings to
`docs/parity-audit.md` (one section per feature, verdict: parity /
differs / missing / improved):

- Registration → email verify → login → password reset → 2FA → TOTP
- Dashboard (widgets, charts, running timer behavior)
- Time page: start/stop timer, edit modal, mass actions, keyboard
  shortcuts, calendar view, timesheet view, breaks
- Projects/Tasks/Clients/Tags: CRUD, colors, billable rates, archive,
  member scoping, estimates
- Members: invite flow via mailpit, roles, placeholders, merge, billable
  rate inheritance
- Reporting: filters, grouped export CSV/XLSX/PDF, shared report links
- Goals: types, weekly targets, progress calc
- Importers: run one real import on both (Toggl CSV + generic CSV minimum)
- Invoicing (tock-only feature upstream gates): recipients, line items,
  billable-time picker, PDF + UBL output vs a real e-invoice validator
- Audit log page
- API tokens, org export ZIP
- OAuth pairing flow shape (desktop app contract)
- 404s, redirects, flash messages, validation error text

Fix small diffs inline (typed errors, status codes, redirects). Log big
ones to the audit doc — do not rabbit-hole.

### 3. Restyle — tock must not look like a solidtime screenshot

Upstream frontend is Vue 3 + Tailwind. Tokens live in
`resources/css/`, `tailwind.config.js`, `resources/js/packages/ui/src/`
(component classes), plus theme init in `resources/js/utils/theme.ts`.
Restyle through tokens, NOT per-component rewrites:

- New color palette + dark/light theme (pick a distinct identity —
  solidtime is blue-accent; choose something clearly different, e.g.
  warm amber/emerald; keep contrast AA on body text)
- New accent/focus rings, button radius scale, badge styles
- Typography: replace Inter default stack if it reads too identical —
  keep legibility first
- New logo/brand marks: "tock" wordmark (simple geometric SVG ok),
  favicons in `public/favicons`, app name/title strings, mail templates
- Rename visible "solidtime" strings to "tock" (DB columns/keys/importer
  type names STAY — schema compat is sacred)
- KEEP: component structure, `data-testid` attributes (e2e relies on
  them), route names, Inertia props. Structure is API; paint is not.
- `just spa` after every styling pass; keep the bundle working.

### 4. Improve — where upstream is genuinely weaker

Only after 1–3. Candidates (verify before committing):

- Import UX: progress feedback on large ZIPs
- Invoice PDF polish + preview thumbnail on the list page
- Command palette coverage for new pages
- e2e Playwright suite: it exists (`e2e/`, `playwright/`) but has never
  run against tock in CI — get it green; that IS the parity verdict
- Anything `docs/parity-audit.md` flagged as "improved" opportunities

## Hard rules (unchanged)

- Schema stays upstream-identical. A real upstream `pg_dump` must still
  restore and run.
- No feature gates, subscriptions, license checks. Ever.
- `just verify` green before every push. Conventional commits,
  `Authored By: TDvorak <info@tdvorak.dev>` trailer.
- Feature branches + PRs to `main`, never push `main` directly.
- Update `~/agent-memory` (`projects/tock.md`) with durable state at
  each milestone — next session must survive on it alone.
- Read `PLAN.md` and `AGENTS.md` before backend decisions.
