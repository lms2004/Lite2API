# Canonical admin console

Lite2API now ships one embedded admin application. Its complete source is:

- `internal/web/app.html` — semantic structure and dialogs;
- `internal/web/app.css` — the only application stylesheet;
- `internal/web/app-core.js` — side-effect-free account and route contracts;
- `internal/web/app.js` — API integration, state, rendering, and interactions;
- `internal/web/embed.go` — deterministic assembly and gzip embedding.

There are no numbered UI generations, runtime patch layers, inline click
handlers, or global render-function overrides. `embed_test.go`,
`app-core.test.js`, and the UI quality workflow enforce that boundary.

## Product invariants

- The console is optimized for a single operator. Common OAuth providers are
  direct actions on the account page; the provider/method wizard remains only
  for uncommon credential types and adapters. A successful OAuth account joins
  the existing credential pool without a redundant result dialog.
- Saving an enabled new or materially changed API connection automatically
  tests the exact current URL, credential, proxy, headers, model declaration,
  and model map first. A changed secret invalidates the previous test, but the
  operator does not need a separate "test" click.
- Every chat-capable API connection exposes a direct channel conversation.
  It bypasses model routes and fallback, carries successful turns as context,
  and reports the actual upstream model, latency, token usage, request ID, and
  raw response without contaminating route-health samples.
- An empty model declaration means **unknown**, not "supports every model".
  Explicit `"*"` remains a deliberate wildcard.
- Capability routes and direct-model routes are different modes. Incompatible
  fallback targets remain visible with validation errors; the UI never removes
  them silently.
- New routes are validated, saved, and hot-loaded by their single primary
  action. Existing route edits remain drafts with impact summaries, discard
  support, server-change conflict detection, and save-time validation.
  Logical-route serialization strips stale target-model fields; direct routes
  never persist a route-level reasoning setting.
- Existing legacy `accounts` and `all_accounts` routes retain the same fields
  and routing semantics when other routes are saved. Moving one to explicit
  targets is a separate, warned draft conversion rather than an implicit
  migration.
- CLIProxyAPI credential-pool priority is higher-first. Lite2API connection
  priority is lower-first and is only used by the route priority strategy. The
  UI labels both directions where they are edited.

## Acceptance

`.github/workflows/admin-canonical-product-qa.yml` starts deterministic local
upstreams and a credential-pool fixture, builds a real Lite2API binary, seeds
real traffic, and drives one sequential Playwright browser through usage,
quality testing, direct channel chat, common-account actions, every uncommon onboarding
branch, automatic connection test/save, immediate route activation, route
validation/save, import dry-run, and mobile layouts. Screenshots and
diagnostics remain ephemeral on the isolated runner; the workflow reports
pass/fail without uploading management evidence.

The production host is intentionally treated as low-memory. Ad-hoc captures
use `make capture-admin`, which visits desktop and mobile views sequentially;
never start multiple Chromium process trees there.
