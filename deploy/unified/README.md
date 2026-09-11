# Unified service: local build contract, not a production deployment approval

The canonical topology is `compose.json`, with the ten isolated source streams in
`sources.json`. These are JSON-formatted Docker Compose files. There is one
`platform-api` Go process and listener. It embeds the invoice service. An invoice
initialization/readiness failure fails the common service. There is no opt-out flag.

- Platform API: `/api/v1`; invoice API: `/invoice-api/v1` (Go rewrites the internal prefix).
- Admin assets: web port 80; invoice user assets: web port 8081. Both SPA roots are `/`.
  Host TLS proxies publish each at its existing public origin; there is no iframe.
- Public gateways reject `/internal/` and `/metrics`. Source ingestion alone reaches
  `/internal/v1/source-batches` through the existing mTLS gateway and isolated network.
- Staff login is `XM_AUTH_MODE=local`; invoice login is `AUTH_MODE=session`.
  `INVOICE_STAFF_ORIGIN` must retain the historical console issuer identity; it cannot
  be guessed from a new hostname. `ADMIN_ROLE` must match the actual local staff role.
- Platform and invoice database pools/roles remain separate. `migrate` includes the
  platform and River migrations. `invoice-migrate` precedes `invoice-permissions`;
  both must succeed before the API starts. The runtime never receives the invoice owner DSN.
- PDF scanning remains networkless with its own UID and capability file. The shared
  socket's supplemental group is 10000. ClamAV remains on the internal app network.
- `platform-worker` retains River/alert/connector responsibilities and explicit egress.
  The API runtime owns invoice background work. Source streams retain independent
  source IDs, accounts, signing keys, state and upstream read-only projection networks.

`SOURCE_TRUSTED_OIDC_ISSUER` and `SOURCE_TRUSTED_OIDC_PROVIDER_KEY` in source agents
verify historical source identity facts. They do not launch or contact an IdP for
interactive login. Their evidence semantics and identity streams remain intact.

## Local validation and build

Use `python scripts/unified-service.py check` and, from a clean committed worktree,
`python scripts/unified-service.py build --tag <reviewed-tag> --output <outside-worktree-directory>`.
The build entry never starts services, loads backups, sends notifications, pushes images,
uploads files, or rolls a deployment forward. It refuses a remote Docker endpoint.
Build records prove local image construction only; they are not production release approval.
The old independent release scripts and compose topologies fail closed.

Checks require local Python 3.11+, Git, Docker Compose, PowerShell 7 and Bash.
`check` performs configuration and deployment contract checks; it does not replace
the two Go suites, frontend suites, or security release review. `build --dry-run`
prints the exact build plan without invoking a Docker build and explicitly records
whether the source is dirty. A real build requires a clean committed source tree.
The builder freezes tracked source in a tar, excludes credentials/runtime files,
and verifies each allowed file against its indexed blob before building. Images and
saved archives bind to this context, both source trees and the exact source commit.
`verify --manifest <path>` verifies only that local-build binding; it cannot turn a
local build into a production-approved release.

`python scripts/probe-unified-nginx.py --image <already-local-image-id>` creates an
isolated disposable nginx echo fixture. It verifies unchanged API paths, stripped
mock/mTLS headers, overwritten forged client-IP headers and blocked public internal
routes. It neither runs the application nor proves host/CDN transport. It cleans
only its own uniquely named container/network and uses no real keys or accounts.

## Before a separately authorized production cutover

This task has not performed any of the following. A future reviewed deployment plan must:

1. Verify exact existing platform/invoice databases, external volume names, separate
   role privileges, task queues and migration ledgers. Historical migrations are unchanged.
   Changing the Compose project must not create empty replacement volumes.
2. Prepare separate invoice owner/app DSN files for the new `invoice-postgres` DNS name.
   The platform database retains `postgres`. Do not reuse a DSN pointing at `postgres`
   for invoice, and do not mount owner credentials into the API.
3. Verify platform credential volume and source/account evidence, plus invoice field
   keyring, binding key, PDF capability, document ownership, source trust and signing
   keys. No actual credential contents were inspected for this change.
4. Reserve non-overlapping network ranges and `UNIFIED_WEB_PROXY_IP` with its exact `/32`
   in `UNIFIED_WEB_PROXY_CIDR`; reserve the existing ingest proxy `/32` outside its dynamic
   range. Validate real host/source networks and remote projection access without weakening
   allowlists. Existing CPA/reqlog mounts remain read-only and must exist before startup.
   The web runtime trusts only `INVOICE_PROXY_GATEWAY_IP/32` for forwarded client IPs.
   Host templates replace incoming X-Forwarded-For; web resolves that exact trusted
   hop and overwrites CF-Connecting-IP with the resulting remote address. Confirm
   the actual host/CDN chain independently before applying IP-based admin policy.
5. Validate both unchanged public origins, forwarded headers, session/CSRF behavior,
   independent staff/user role boundaries and SUB/NEW accounts using synthetic identities.
   Verify the host TLS proxy's invoice upstream is web port 58090, never old API port 58088.
6. Exercise both migration chains and hardened runtime roles, common readiness with
   invoice unavailable, scan/upload/download, source heartbeat/projection, invoice
   background work and platform worker recovery. A platform-only smoke is insufficient.
7. Produce a reviewed signed release policy covering the exact unified image inventory,
   vulnerability scans, SBOMs, both source trees, migration/deployment definitions and
   rollback/data compatibility. Old invoice-only signed manifests cannot authorize this topology.
8. Resolve deployment, rollback and historical recovery sequencing in a new production
   change approval. No new production deploy command is provided by this local task.

## Retired authentication and historical evidence

Keycloak compose, realm configuration, provision/bootstrap/maintenance scripts and
their active smoke gates are removed. Old independent deployment/backup generation
entries reject execution before reading environment/secrets or invoking Docker.
`invoice/deploy/backup/restore-drill.sh` still accepts an encrypted historical Keycloak
dump into a temporary PostgreSQL restore database to verify archive integrity; it does
not launch Keycloak, restore login endpoints, or reconnect that database to a service.
Signed historical manifests, handoffs, old migration bytes and their checksums remain
historical evidence. Columns named `oidc_issuer/oidc_subject` are retained because the
platform login identity migration explicitly reuses them for current account identity.
