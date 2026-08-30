# Deployment Version Manifest Template

Keep the completed manifest in protected host configuration management. Do not
commit public IPs, private hostnames, certificate details or operational
credentials to the application repository.

Record at least:

| Item | Evidence |
|---|---|
| Deployment timestamp | UTC ISO-8601 |
| Lite2API Git commit | Full 40-character commit |
| Lite2API binary | SHA-256 and installer rollback snapshot |
| CLIProxyAPI upstream | Fixed commit plus ordered patch SHA-256 values |
| CLIProxyAPI binary | SHA-256 and installer rollback snapshot |
| Container images | Full OCI names with `sha256:` digests |
| Configuration | Redacted configuration SHA-256 |
| Nginx allowlist | Redacted network list hash and `nginx -t` result |
| Backup | Encrypted off-host snapshot ID and restore-drill date |
| Health evidence | Local health, model auth, management auth and public 401 |

Generate evidence after every deployment with the installers and
`deploy/server-ops/check-services.sh`; never paste secret-bearing environment or
credential files into the manifest.
