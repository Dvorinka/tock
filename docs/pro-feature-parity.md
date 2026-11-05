# Upstream gated-feature parity

Status checked 2026-10-31 from the Vue feature predicates, Inertia shared
props, Go routes, and the project contract in `PLAN.md`. This is a source
inventory, not an end-to-end behavioral certification.

| Upstream feature area | Tock behavior | Evidence | Result |
| --- | --- | --- | --- |
| Billing, subscriptions, trials, and blocked organizations | Stripe and subscription behavior are intentionally omitted. Tock has no billing extension and does not block organizations by plan. | `internal/httpapi/inertia.go` sets `has_billing_extension` false and `billing` nil; `PLAN.md` excludes cloud billing. | Intentionally not ported |
| Billable rates, estimates, and premium report controls | Enabled for all organizations. The Vue checks allow the action when billing is absent; server permissions remain role based. | `resources/js/utils/billing.ts`; `internal/httpapi/inertia.go`; project and report handlers. | Available without a plan gate |
| Goals | CRUD and progress are implemented; the SPA receives `has_goals_extension: true`; the plan limit is unlimited. | `internal/httpapi/goals.go`; `internal/httpapi/inertia.go`; `PLAN.md` Phase 5. | Available without a plan gate |
| Invoicing | Invoice pages and API are implemented and the SPA receives `has_invoicing_extension: true`. | `resources/js/Pages/Invoices.vue`; `internal/httpapi/invoices_api.go`; `internal/httpapi/inertia.go`. | Available without a plan gate |
| Audit log | Mutation recording, API endpoints, and viewer page are implemented; access is controlled by the role permission `audits:view`. | `internal/httpapi/audit.go`; `resources/js/Pages/AuditLog.vue`; router page and API routes. | Available, role permission applies |
| Services extension | The SPA receives `has_services_extension: false`, hiding the upstream services settings entry. No Go services equivalent was found. | `internal/httpapi/inertia.go`; `resources/js/Layouts/AppLayout.vue`; `resources/js/Components/UserSettingsIcon.vue`. | Gap: behavior needs a public spec before a port |
| Runtime extension system | Not supported; the product is a single compiled binary. | `PLAN.md` architecture and explicit exclusions. | Intentionally not ported |

## Findings

- The ordinary upstream premium controls for rates, estimates, reports, and
  member invitations resolve to enabled because `has_billing_extension` is
  false. The server does not apply subscription or license checks.
- Goals and invoicing explicitly advertise themselves as available to the
  retained SPA.
- The audit log uses the normal capability matrix, not a commercial tier.
- Services is the only extension surfaced in the SPA without a tock
  implementation. Its upstream implementation is private and must not be
  copied. Keep it out of the build until its behavior can be specified from a
  public contract or independently designed.
- Cloud billing remains out of scope. Do not count it as a parity defect.

## Limits of this review

This inventory verifies wiring and implementation presence. It does not
compare every upstream gated workflow in a live oracle or certify every
feature's API and UI behavior. Continue the feature-by-feature review in
`docs/parity-audit.md` for those claims.
