# Vue to React

Decision: stay on the upstream Vue 3 + Inertia SPA. Do not start a React
port.

## Why

`AGENTS.md` says port the product, do not redesign it. `resources/js` is
the solidtime client: Inertia pages, `packages/ui`, and Ziggy route names.
The Go server speaks that protocol from `internal/httpapi/inertia.go`.
Replacing the client means reimplementing every page the Playwright suite
already covers, then keeping up with upstream Vue changes by hand.

A second framework in the same bundle costs more than it saves. React is
the default for a new TDvorak app. This repository is not a new app.

## Inventory

| Surface | Role | Move? |
| --- | --- | --- |
| `resources/js/Pages` | Inertia pages | No |
| `resources/js/packages/ui` | Shared Vue components | No |
| `resources/js/utils`, `Layouts` | Client behavior the e2e suite locks | No |
| `internal/httpapi/inertia.go`, `internal/httpapi/pages.go` | Server props for those pages | Stays the contract |
| `e2e/` | Browser oracle for the Vue UI | Stays |

## If one page is ever replaced

Do it only because that page is being thrown away for a product reason.
One route, the same Inertia props, no second framework loaded for the
rest of the app. Until that reason exists, leave the client alone.
