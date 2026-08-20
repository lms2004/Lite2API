# Repository agent guidance

## Browser and visual QA safety

- Never start multiple Chromium or Playwright browser processes concurrently on
  this production host. In particular, do not wrap browser commands in
  `Promise.all`, `xargs -P`, background loops, or parallel sub-agents.
- Reuse one Playwright browser for multi-page acceptance tests. For ad-hoc CLI
  screenshots, use `make capture-admin`; it captures the standard views
  sequentially.
- Every browser run must have a hard timeout and guaranteed cleanup on success,
  failure, cancellation, and disconnect.
- Keep browser validation read-only against production. Run stateful product
  workflows in CI fixtures, not against the production Lite2API process.
