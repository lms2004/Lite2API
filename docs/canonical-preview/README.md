# Canonical admin console

Lite2API now ships one embedded admin application. Its complete source is:

- `internal/web/app.html` — semantic structure and dialogs;
- `internal/web/app.css` — the only application stylesheet;
- `internal/web/app-core.js` — side-effect-free account and route contracts;
- `internal/web/app.js` — composition root, scoped state, session, navigation and resource loading;
- `internal/web/app-runtime.js` — request cancellation, GET coalescing and refresh lifecycle;
- `internal/web/app-metrics.js` — memoized account statistics and connection-to-route indexes;
- `internal/web/app-ui.js` — shared DOM updates, confirmations, action locking, menus, clipboard and focus behavior;
- `internal/web/app-shared.js` — presentation helpers and account selectors;
- `internal/web/app-{usage,accounts,routes,onboarding,chat,clients}.js` — feature controllers with explicit dependencies;
- `internal/web/embed.go` — deterministic assembly and gzip embedding.

There are no numbered UI generations, runtime patch layers, inline click
handlers, or global render-function overrides. `embed_test.go`,
`app-core.test.js`, `app-runtime.test.js`, and the UI quality workflow enforce
that boundary. Modules are embedded in dependency order by Go, retaining one
document, one CSP-pinned script and no frontend runtime or build dependencies.

## Refresh and rendering ownership

- Controllers receive only the state slices and services they use. Transport
  and scheduling have no DOM or feature-state dependency; domain rules remain
  in the pure core. Each feature owns its event bindings and private handlers;
  the composition root calls `bind()` and a small public API, without forwarding
  every internal function. Feature controllers never replace global render functions.
  The route controller owns drafts, server baselines, conflicts and pending saves;
  session/loading and onboarding consume its public methods rather than its state.
- The visible view owns a single refresh generation. A range/view change or
  forced post-mutation refresh cancels its predecessor. Late results cannot
  update the new view. Hidden or unauthenticated pages stop polling.
- State renders as soon as it arrives. Optional quota and trend requests do not
  hold up the working surface. Trends are requested only for usage, credential
  snapshots for usage/accounts/routes, and pool settings only for accounts.
  Client keys and adapters refresh with their corresponding views. Explicit
  refresh bypasses caches; writes invalidate those caches.
- Account statistics use a map index once per immutable server snapshot instead
  of scanning all recent requests and runtime accounts for each rendered row.
  Route references are indexed per configuration, preserving wildcard and legacy
  semantics. Number formatting also shares one formatter.
- Unchanged markup is retained. Editable routes and account rows reconcile
  keyed DOM nodes, retaining actual controls, focus, selection and open menus.
  Account counters keep updating during an edit without freezing the whole view.
  Route validation and upstream selection no longer recreate the editor on change.
  Request history renders only when expanded, with at most 30 rows per page;
  the accessible chart data table also renders on demand.
- Unchanged chart data and dimensions skip canvas repainting. Pointer selection
  uses binary search and avoids repeatedly updating the same tooltip. Quota
  disclosures retain their state by account identity.
- Quota charts filter credential identities; request charts filter connections.
  Switching between these metric families clears incompatible selections.
- Dialogs lock background scrolling and return focus to their opener. Menus
  support Escape, outside dismissal and keyboard navigation, and are positioned
  within the viewport. Motion uses short opacity/transform transitions and
  respects reduced-motion preferences.
- Destructive actions use a shared asynchronous confirmation dialog, focus
  Cancel initially, and name the exact action. Repeated key submissions share
  an action lock; revoking a newly created key also clears its displayed secret
  and generated configuration. Typing incomplete forms does not emit error toasts.
- CSP hashing uses the exact final script element bytes, including template
  whitespace, so HTML formatting cannot silently disable the application.

## Product invariants

- Overview puts trends and account quotas together in the first working area.
  Quota details are expanded per account; each view has one heading with its
  actions and refresh control. The account page provides a direct jump to API
  connections, also used after connection onboarding.
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
  Pending saves acknowledge only the submitted snapshot and retain subsequent
  local edits. Repeated submissions remain locked throughout the pending save.
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

`interaction_checks.js` additionally exercises delayed range responses,
in-progress account edits, menu dismissal and focus return, dialog motion and
rapid reopening, hidden-page polling, lazy request rendering and pagination,
quota identity filters, route search, cancellation of destructive actions,
duplicate key submissions and revocation, explicit key refresh, 200% text enlargement, and viewport
boundaries at 320, 390, 768, 1024 and 1440px.
`editor_checks.js` covers Tab navigation, validation without focus loss, repeated
upstream selection, add/reorder/delete focus, and editing while a save is in flight.
One browser is reused; signal handlers and a hard deadline close it on failure.

The production host is intentionally treated as low-memory. Ad-hoc captures
use `make capture-admin`, which visits desktop and mobile views sequentially;
never start multiple Chromium process trees there.
