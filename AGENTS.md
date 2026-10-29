# Repository agent guidance

## Business contract and delivery

- Use `docs/BUSINESS_CAPABILITIES.md` as the business target. Its Mermaid
  containers express ownership and its dashed edges express support; they do
  not define call order, processes, or a required module layout.
- Apply the eight scenarios in `docs/BUSINESS_USE_CASES.md` to source, client,
  invocation, access, channel changes, repair, upgrade, and restore work. A
  historical snapshot's failed test stays failed; later source revisions need
  their own scope and evidence rather than rewriting that history.
- The program owns authentication, authorization, resource limits, capability
  filtering, bounded recovery, complete delivery, and observation retention.
  An Agent running a check or generating client configuration does not make
  that responsibility a runtime feature.
- For each change, trace the affected request/configuration path and retain
  concrete input, expected behavior, observed result, and test/source references.
  Distinguish implementation, isolated HTTP verification, and actual client /
  provider acceptance. Reuse evidence only for its unchanged scope.
- A model catalog is a declaration. Set capability evidence to `verified` only
  after checking that exact source/model/interface/feature combination, with
  `source` and `observed_at`. Unknown capability/quota is not unsupported/zero.
  Never upgrade catalog discovery or OAuth refresh success into call success.
- Prefer the product's `/admin/api/accounts/verify` checks to manually writing
  proof fields. Verify exact source/model/operation/effort/credential, then read
  whether evidence was saved. Tool support requires the actual two-way exchange;
  ordinary stream and tool checks do not prove their combination or image use.
- Preserve reviewed configuration revisions through UI drafts and previews.
  Check service-provided change impact; a stale operation must not silently
  overwrite a newer configuration or expose implicit sources outside an alias.
- A logical call succeeds only after the protocol result is complete and
  delivered. Test failure branches that affect this promise: malformed 2xx,
  missing stream terminal events, cancellation, safe rejection/fallback, and
  uncertain submission. Never replay uncertain submissions or join sources
  after response delivery starts.
- Keep client model/feature promises through every fallback. Reject conflicting
  requirements explicitly. Soft sticky routing is not proof that provider-side
  continuation state is portable; validate any required session behavior with
  the actual client and adapter before declaring it supported.
- After configuration/permission changes, check subsequent requests and active
  resource leases. A save response or process startup alone is insufficient.
  Recovery acceptance requires a completed call; permanently invalid OAuth
  grants require reauthorization, not restoration of old tokens.
- Preserve unrelated work. Split tasks only when the parallel benefit exceeds
  coordination cost; give each deliverable one owner and persist evidence and
  unresolved limits. Add independent review for substantial protocol/security
  changes, then recheck only the affected scope.
- Run stateful acceptance with temporary config, keys and logs. Keep credentials,
  raw prompts/responses, and environment contents out of committed evidence.
  Legacy configured API keys are unrestricted compatibility credentials; use
  managed client keys when per-client model/RPM/concurrency limits are needed.
- Release acceptance uses `deploy/verify_service.py` and reviewed scenarios,
  including access denials and affected sources. Liveness alone only establishes
  bootstrap. Restore encrypted material into a new private directory, check real
  client/key/source behavior, and retain failed or incomplete recovery results.
  Never present fake-age contract tests as encryption or disaster recovery proof.

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
