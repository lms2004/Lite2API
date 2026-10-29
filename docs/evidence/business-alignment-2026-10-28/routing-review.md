# Lite2API capability and routing work

Owner: /root/routing. Repository: /root/Lite2API. Target: user attachment `已粘贴的文本.txt` (2026-10-28).

## Confirmed gaps and repairs

- Selection originally considered operation/model/configured effort only. Added per-request stream/tools/image input requirements. Explicit negative feature facts exclude both first and fallback candidates before submission. Unknown facts allow an attempt and remain unknown; success does not manufacture a verified fact.
- Added `ChannelCapability.evidence` and `.features` with bounded status/source/observed_at facts. Feature keys scope the interface (`openai.chat:tools`, `openai.responses:stream`, `anthropic.messages:image_input`). Statuses: declared/verified/unsupported/unknown. Legacy empty evidence means operator declaration, never a verified call. `verified` requires source and RFC3339 observation time. Model catalog discoveries are stamped `declared`, `model_catalog`, fetch observation time. Discovery preserves independently verified/negative feature facts. Config snapshots deep-copy maps; main Agent updated admin clone and source-change comparisons.
- Required features are read from full RawMessage fields with a byte scanner, so image detection is not lost after the existing 1 MiB observability cap. The scanner skips arbitrarily large JSON strings without copying them.
- Dynamic client reasoning now participates in selecting the actual upstream model; route `auto` no longer causes a high-effort request to choose a low-only upstream. Fixed alias effort conflicts are explicitly rejected. OpenAI Responses uses native `reasoning.effort`, preserves other reasoning fields, and omits the nonnative top-level field. Explicit `none` is forwarded, not deleted. Gateway callpoints integrated by completion Agent.
- Anthropic legacy `reasoning_effort` is rejected with 400 instead of silently discarded; its native `thinking` is preserved. Non-auto configured OpenAI effort targets are ineligible on Messages rather than pretending to implement a translation.
- Known cooling/unavailable candidates return ErrKnownUnavailable, distinct from full capacity and no eligible source. No healthy source is still a failed logical call.
- Legacy account/all_accounts routes also check their resolved upstream model against account declarations. The former test allowing alias `all` to be sent unchanged to an account advertising only `m` was corrected to explicitly map upstream_model=m.
- Runtime source URLs use the redaction helper owned by permissions Agent.

## Owned files

- internal/config/config.go, capability_evidence.go, capability_evidence_test.go
- internal/gateway/runtime.go, runtime_test.go, capabilitysync.go
- internal/gateway/request_requirements.go, request_requirements_test.go, request_rewrite_operation.go
- internal/gateway/claude_compat_test.go

## Verification evidence

Focused isolated tests (using loopback httptest servers; no production mutation) passed:

```
GOTOOLCHAIN=local go test ./internal/config ./internal/gateway -run 'Test(CapabilityEvidence|Requirements|SelectionExcludes|SelectionDistinguishes|DynamicReasoning|GatewayFeature|GatewayResponsesReasoning|DiscoveryPreserves|ResponsesReasoning|Anthropic|MergeSynced|Reconcile|SyncDiscovered|EmptyRoute|OrderedTarget|Scheduler|TargetRoute)' -count=1
ok github.com/lms2004/lite2api/internal/config 0.020s
ok github.com/lms2004/lite2api/internal/gateway 0.274s
```

Business fixtures cover: large image prompts, tool/stream requirements; explicit unsupported first source skipped; unsupported fallback never called after primary quota rejection; unknown remains unknown; correct high-effort upstream choice; native Responses high/none plus conflict rejected before submission; native Anthropic thinking and unsupported legacy field; discovery preserving verified feature evidence; cooldown vs no-support vs capacity; existing scheduler/reload/capacity regressions.

After that focused pass, one safety-only adjustment rejects configured `none` effort on Anthropic too, because there is no implemented mapping to its native thinking parameter. Root Agent will run the final full race suite after integration.

## Boundaries

No external account, real model request, service deployment, credential modification or browser run was performed. These are source + isolated HTTP-path tests. Live client/version/model combinations still require scoped operational acceptance; manually recording a verified fact is not itself proof. Whole target capability is not claimed merely from these schema fields. Existing shared capacity across reloads already meets the inspected limit-preservation requirement and was retained.

## Supplemental one-pass session constraint review

Confirmed by source only (no new runtime test; no production request):

1. Lite2API `gateway.go:1435` (`sessionKey`) includes `previous_response_id` as only a scheduler seed; no response-ID ownership registry exists. `runtime.go:717` can skip the first candidate when cooling/full, and the ordinary gateway attempt loop can change target after safe rejection. Sticky is preference, not required ownership.
2. CLIProxy HTTP Responses uses `ExecuteWithAuthManager` / `ExecuteStreamWithAuthManager` directly (`sdk/api/handlers/openai/openai_responses_handlers.go:446,488`) and does not reconstruct prior input history. Its richer transcript reconstruction is in the distinct WebSocket handler.
3. `third_party/cliproxyapi/internal/runtime/executor/codex_websockets_executor.go:1552,1585` explicitly routes non-WebSocket downstream to the legacy HTTP executor. That executor deletes `previous_response_id` in both non-stream and stream paths (`codex_executor.go:180,428`). A syntactically completed response may therefore lose requested prior context silently even with the same auth.
4. CLIProxy `SessionAffinitySelector.Pick` changes credentials if a cached auth is unavailable (`sdk/cliproxy/auth/selector.go:506`); session affinity is likewise not a hard stateful constraint.

Minimal safe current boundary: explicitly reject nonempty `previous_response_id` on the gateway's HTTP Responses contract and direct clients to send complete input history. Treat null/empty as no continuation, reject wrong type, never silently remove it. This needs a focused no-upstream-call test. It limits unsupported incremental continuation, while preserving full-context stateless tool calls.

Actual continuation support would require bounded response-ID ownership tied to account, actual selected credential, model, upstream identity, and client access; capture terminal IDs; honor identity changes/reload; refuse missing/expired bindings and unavailable original owners; no fallback to a different owner; and no use of CLIProxy's current Codex HTTP deletion path. Adding only stable session hashing or explicit single target cannot prove this contract. This review intentionally made no code changes; root Agent owns the final boundary decision.

## Final continuation increment (supersedes blanket-rejection recommendation)

Implemented after root Agent chose to preserve reasonable direct native passthrough:

- `RequestRequirements.Continuation` is set only for a nonempty Responses `previous_response_id` string. Null/empty means no continuation; wrong JSON types and >4096 byte IDs fail before submission.
- `Scheduler.ValidateContinuation` counts structural owners `(account ID, mapped model, pinned credential)` using model/operation/request-feature constraints. Cooldown and capacity never erase a potential owner. It allows only one owner, with a stable concrete model mapping, account type openai, adapter empty or generic-openai, and no credential pin. CLIProxy and other declared adapters, multiowner routes, and logical names with multiple concrete model mappings are rejected explicitly with 400.
- A pinned gateway runtime generation retains that singleton candidate set throughout the request. Its native upstream receives `previous_response_id` unchanged, owns the history, and must validate the opaque ID. There is no crossowner fallback, synthetic history, response cache, or new binding framework. Unknown/expired IDs or stale config lineage remain the native upstream's explicit error responsibility.
- Gateway integration is at `gateway.go` immediately after route-requirement validation, owned by root Agent.
- New owned files: `internal/gateway/continuation.go`, `continuation_test.go`; parser updated in request_requirements.go.

Focused tests:
```
GOTOOLCHAIN=local go test ./internal/gateway -run 'Test(Continuation|GatewayContinuation)' -count=1
```

The isolated HTTP fixtures prove: invalid type rejects before network; known CLIProxy/third-party adapters reject without network; multiple direct owners reject even when one is cooling; a single direct native source receives the exact prior response ID and mapped model; quota rejection sends exactly once; an unavailable native owner yields 503 without replacing or resubmitting it; variable concrete model mappings reject. No live credential/model calls or production writes occurred.

## Final evidence-lifecycle regression repair

Confirmed the newly added discovery timestamps were being compared by sameChannelCapabilities, causing a same-content catalog poll to persist/rebuild; full capability serialization also placed evidence Source/ObservedAt into route fingerprints. This would retire completed-call observations without a routing change.

Fixed only capabilitysync/runtime:

- Both catalog merge paths preserve prior model_catalog evidence when model, upstream mapping, merged reasoning contract and evidence status are unchanged. ObservedAt now belongs to the observed declaration version, not every polling heartbeat. A changed declaration can acquire a new observation time.
- Route fingerprints retain model, mapping, reasoning, capability status, feature keys and feature statuses; pure evidence source/time annotations are excluded. Full configuration comparisons still see metadata edits, so they remain safe for concurrent write detection.
- `capability_evidence_lifecycle_test.go` runs actual isolated HTTP calls after initial discovery and proves repeated identical catalogs preserve runtime identity, persisted bytes and successful RouteLatest observations, for generic-openai and CLIProxy discovery. It also proves source/time edits retain fingerprints while capability status, feature status, mapped model and reasoning changes invalidate them.

```
GOTOOLCHAIN=local go test ./internal/gateway -run 'Test(UnchangedCatalog|RouteFingerprintIgnoresEvidence|DiscoveryPreserves|MergeSynced|Reconcile)' -count=1
ok github.com/lms2004/lite2api/internal/gateway 0.296s
```

git diff --check passed. Root Agent owns the final full regression after this final increment. No further scope review was performed.
