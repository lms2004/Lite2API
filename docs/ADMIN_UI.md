# Admin UI architecture

The admin console is a dependency-free, server-embedded application. See
`canonical-preview/README.md` for its source boundary, product invariants, and
local acceptance harness.

When changing the UI:

1. Put reusable account and route rules in `internal/web/app-core.js` and cover
   them with `app-core.test.js`.
2. Keep markup in `app.html`, styling in `app.css`, and browser integration in
   `app.js`; do not add versioned overlays or monkey patches.
3. Preserve unknown operational states as unknown. Never turn absent quota,
   health, or model data into a successful zero value.
4. Treat route editing as a draft. Never discard incompatible targets merely
   because another model or effort is selected.
5. Run `go test ./...`, the Node domain tests, and the sequential browser
   acceptance flow before release.
