# Third-Party Notices

## Sub2API

The Lite2API account-import workflow and its Sub2API export compatibility were
designed with reference to the following Sub2API source files:

- `frontend/src/components/admin/account/ImportDataModal.vue`
- `frontend/src/__tests__/integration/data-import.spec.ts`
- `frontend/src/api/admin/accounts.ts`
- `backend/internal/handler/admin/account_data.go`

Reference source: <https://github.com/Wei-Shaw/sub2api>
Reference revision: `fbfdcef8184ae4b2e224d5cfc47cf1d0e3742710`
Copyright (c) 2026 Wesley Liddick and Sub2API contributors
License: GNU Lesser General Public License v3.0 (LGPL-3.0)

Lite2API does not vendor or link Sub2API. Its import implementation is a
lightweight, independent rewrite for Lite2API's configuration model; this
notice preserves the provenance of the workflow and compatible data format.
The upstream license text is available at
<https://github.com/Wei-Shaw/sub2api/blob/fbfdcef8184ae4b2e224d5cfc47cf1d0e3742710/LICENSE>.

## CLIProxyAPI

Lite2API vendors CLIProxyAPI as an optional, isolated OAuth adapter submodule.
The production profile is pinned to tag `v6.10.9` and revision
`785b00c3127eea6aa207f1207ead8a2aa93690a3`; it is not linked into the
Lite2API binary.

Source: <https://github.com/router-for-me/CLIProxyAPI>
Copyright: CLIProxyAPI contributors
License: MIT

The upstream license text is available at
<https://github.com/router-for-me/CLIProxyAPI/blob/785b00c3127eea6aa207f1207ead8a2aa93690a3/LICENSE>.
