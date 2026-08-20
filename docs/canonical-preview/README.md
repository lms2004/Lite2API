# Lite2API canonical admin preview

This directory is generated only after the one-shot migration Runner completes all of the following against a real Lite2API process:

- `go test ./...`, JavaScript syntax validation, configuration validation, and binary build;
- deterministic OAuth quota fixtures with five accounts and multiple quota windows;
- real gateway traffic through four upstreams with distinct latency and a controlled flaky channel;
- Playwright traversal of the usage console, account inventory, all provider onboarding branches, unsaved connection testing, three-run direct channel quality testing, and account import dry-run.

The production UI itself has a single canonical source (`internal/web/app.html`, `app.css`, and `app.js`). No `native-v*` runtime layers are referenced or retained.

## Production-host browser safety

The production server is a 2-vCPU, 1-GiB host. Never launch browser captures in
parallel there. Use `make capture-admin` for ad-hoc screenshots; it runs the
standard desktop and mobile views sequentially. The Playwright acceptance flow
reuses one browser and closes it in `finally`, including assertion failures.

The host also installs `/usr/local/bin/chromium` as a safety wrapper. Headless
runs are globally serialized, concurrent launches time out after 15 seconds,
and a single browser is terminated after 180 seconds. CI runners are the right
place for stateful or parallel visual testing.
