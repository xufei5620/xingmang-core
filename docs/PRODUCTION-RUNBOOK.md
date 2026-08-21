# Production launch runbook

Target: `invoice.solov.cc` on the currently reachable `fiberstate` host.

This document is executable procedure, not authorization. Stop before every
step marked **production change approval** until the operator confirms the
exact hostname, files, CIDRs and credentials. Never patch, mount or vendor code
inside Sub2API or New API.

## 1. Required decisions and values

The following values must exist before a public launch:

- actual issuing entity name (administrator-only);
- minimum amount, initially at least CNY 200.00;
- dedicated QQ/enterprise QQ sender plus authorization code, or Gmail sender
  plus Google App Password;
- administrator public `/32` or `/128` CIDRs and a separate VPN/bastion
  break-glass CIDR;
- central OIDC provider. The supplied reference deployment is Keycloak 26.7.2
  at `auth.solov.cc`, with administration isolated at
  `auth-admin.solov.cc`; an existing standards-compliant IdP may replace it;
- TLS certificates for both Keycloak hostnames, plus one exact administrator
  `/32` or `/128` and an independent exact break-glass VPN/bastion route;
- one UUID `source_instances.id` for Sub2API and one for New API;
- New API payment-evidence procedure with at least three authorized finance
  operators (proposer, distinct approver, and independent issuer);
- age backup recipient and offline private identity holder.

Do not put any value above into Git except the non-secret CIDR/settings example
after replacing it with a host-local untracked file.

## 2. Frozen current upstream facts

Recheck immediately before launch; these observations are from 2026-08-20/21:

- FiberState Sub2API container: `sub2api-mig`, runtime `0.1.179`, commit
  `75f88be5f75c27771836b586f7de1503afa0e3bc` (rechecked 2026-08-21
  16:18 Asia/Shanghai; the container image label remains stale);
- Sub2API Compose directory: `/root/sub2api-mig`; PostgreSQL container:
  `sub2api-mig-postgres`;
- New API container: `new-api`, image `v1.0.0-rc.25`, commit
  `f116414284162ad15d8925f7bca494c109b83e93`;
- New API Compose directory: `/root/new-api`; PostgreSQL container: `postgres`;
- Sub2API OIDC is disabled; PKCE, ID-token validation and verified-email
  requirements are also currently false;
- Sub2API session binding is enabled. The invoice integration does not reuse
  its JWT, so this does not enter the invoice authentication chain;
- read-only aggregate currency audit on 2026-08-21: 2,930 payment orders, all
  provider snapshots at schema version 2 but without a currency key; all use
  provider key `easypay` with payment type `alipay`, which v0.1.179's audited
  currency function fixes to CNY. No order/user-level value was exported;
- host reverse proxy is BT/OpenResty Nginx at
  `/www/server/nginx/conf/nginx.conf`; vhosts are under
  `/www/server/panel/vhost/nginx`;
- current disk and memory capacity are ample, but capacity must still be
  rechecked before image pulls and ClamAV database initialization.

The integrity gate must show all four research trees at their recorded clean
commits before and after every release:

```powershell
pwsh -NoProfile -File .\scripts\check-upstream-integrity.ps1
```

## 3. Build and release gates

Run locally from `K:\发票\invoice-system`:

```powershell
pwsh -NoProfile -File .\scripts\verify.ps1
```

The default gate includes Go race/vet tests, frontend production builds,
dependency audit, agent tests, isolated PostgreSQL migrations and concurrency
tests, runtime-role immutability checks, Compose validation and upstream
integrity.

Before copying images to the server:

Run the fail-closed image gate. It builds API, PDF scanner, tools, web and
source-agent images, pulls every pinned runtime dependency, updates exact
Trivy 0.74.0 databases, scans images serially, generates CycloneDX 1.7 SBOMs,
and binds every report to the immutable local image ID:

```powershell
# Default: builds and scans the exact Keycloak 26.7.2 candidate.
pwsh -NoProfile -File .\scripts\release-image-gate.ps1 `
  -ReleaseName 0.1.0-rc2 `
  -ReleaseDirectory release\0.1.0-rc2 `
  -IdPMode keycloak
```

`ReleaseDirectory` must be a new or empty directory below `release/`; the
script never removes or overwrites an existing artifact directory. The default
IdP mode is deliberately `keycloak`: it must actually build the exact pinned
26.7.2 base and pass with zero unexcepted HIGH/CRITICAL findings. Even a clean
IdP image is recorded as `image_approved_pending_canary`, keeps production
launch blocked, and exits non-zero until real OIDC/MFA/RP-logout/back-channel
canaries are satisfied by the deployment runbook. Explicit `external-managed`
mode does not silently skip the IdP: its manifest records
`external_pending_canary` and remains blocked. Explicit `none` is likewise an
application-image-only blocked result.

The exact manifest-bound Keycloak image also runs the disposable
`provision-solov-realm.sh` integration: realm policy, LoA1/LoA2 executions,
roles/ACR/AMR mappers, four clients, no offline scope, fixed output, existing
realm refusal and the single root-owned invoice client-secret publication must
all pass. A result from a different local Keycloak tag or image ID is rejected.

The output includes raw HIGH/CRITICAL Trivy JSON, CycloneDX 1.7 SBOMs, build
logs, exact tool/database evidence, `release-manifest.json`, and a complete
`SHA256SUMS`. Recheck an artifact directory and its current local image tags
without rebuilding:

BuildKit provenance wrappers are disabled for the local candidate images so
an otherwise identical cached build is not assigned a fresh attestation
manifest-list ID. The gate's own machine manifest, CycloneDX SBOMs and hashes
are the retained provenance evidence.

Validation artifacts may still be generated from an unborn or dirty Git tree,
but their machine manifest keeps `productionLaunch` blocked with
`source_git_head_missing` and/or `source_worktree_dirty`. A production bundle
requires the reviewed source commit/tag to exist before the gate runs; the
gate never invents an author identity or commits files itself.

```powershell
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 `
  -ReleaseDirectory release\0.1.0-rc2
```

The verifier rejects a report/SBOM whose embedded Trivy ImageID, manifest
hash, checksum or current local image ID has drifted. Trivy runs under an
exclusive release-cache lock and every scan is serial, avoiding shared-cache
lock races. No vulnerability is ignored. PostgreSQL findings may pass only
when every finding remains confined to `usr/local/bin/gosu`/Go stdlib in the
exact reviewed digest, the extracted linux/amd64 binary has the exact reviewed
SHA-256, and pinned `govulncheck v1.7.0 -mode=binary` still proves zero called
vulnerabilities. This exception cannot be inherited by another digest, binary,
package or target.

The current Keycloak 26.7.2 image retains one visible Trivy HIGH finding only
under the exact vendor-rejection policy documented in
`docs/IMAGE-SCAN-REVIEW.md`. The gate binds the exception to the base digest,
derived image ID, CVE/package/version/status tuple, final-image pruning proof,
and isolated runtime OIDC smoke. It uses no Trivy ignore. Any drift fails.

After the artifact gate is internally consistent, run `govulncheck v1.7.0
./...` for both Go modules, create a Git commit/tag only after the working tree
has no unexpected file or secret, and retain the generated manifest and
checksums with the release. A non-zero image-gate exit is a release block, not
an artifact-generation failure when its manifest says `productionLaunch:
blocked`.

The current pinned runtime baselines are Go 1.25.13, pgx 5.9.2, x/text 0.39.0,
PostgreSQL 18.4, Keycloak 26.7.2 and ClamAV 1.4.5 LTS. The production Compose
pins the reviewed amd64 ClamAV image digest and reserves 4 GiB because engine
load and signature reload can temporarily require several GiB; refresh that
digest only through the image scan/SBOM gate above. The Keycloak Dockerfile
pins both stages to multi-arch digest
`sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec`;
an override of `KEYCLOAK_BASE_IMAGE` is a release change and requires the same
scan/review/recording gates.

## 4. Host directories and secrets

**Production change approval.** Create a new directory; do not reuse an
upstream deployment directory:

```bash
install -d -m 0700 /root/invoice-system/{secrets,config,backups}
```

Copy only this repository/release into `/root/invoice-system/app`. Required
untracked secret files are listed by `deploy/docker-compose.prod.yml` and
`deploy/docker-compose.idp.yml`.

Generate independent random database passwords and create matching one-line
DSN files. Use URL-safe/hex passwords so a DSN is not ambiguously encoded.
Generate the application field keyring without printing key material:

```bash
docker build --target api -t invoice-system-api:0.1.0 /root/invoice-system/app/backend
docker build --target tools -t invoice-system-tools:0.1.0 /root/invoice-system/app/backend
docker run --rm --user "$(id -u):$(id -g)" \
  -v /root/invoice-system/secrets:/secrets \
  --entrypoint /usr/local/bin/invoice-keygen invoice-system-tools:0.1.0 \
  --out /secrets/invoice_field_keyring.json --key-id 2026-08
```

Generate `invoice_session_binding_key` from at least 32 random bytes. Store the
QQ authorization code only through the authenticated settings page after
startup; it is encrypted by the field keyring and never returned.

Generate the private source-agent PKI in a temporary 0700 directory:

```bash
docker run --rm --user "$(id -u):$(id -g)" \
  -v /root/invoice-system/secrets:/secrets \
  --entrypoint /usr/local/bin/invoice-mtlsgen invoice-system-tools:0.1.0 \
  --out-dir /secrets/source-pki --server-name invoice-ingest.internal \
  --clients sub2api-agent,newapi-agent
```

The API target contains only `invoice-api` and the read-only migration files
required by startup verification; it has no qpdf binary or scanner server.
qpdf exists only in the separate low-UID `scanner` target. Migration/bootstrap/
key-generation and restore verification binaries exist only in the separately
scanned tools image; never deploy that image as the public API service. Record
and scan the API, scanner and tools image digests in the release manifest.

Move `source_agent_ca_key.pem` offline after issuing certificates. It must not
remain mounted to any production container. Set every secret directory to
0700, then apply the stricter per-consumer `0400`/`0444` file matrix below.

Compose `secrets.file` does not change host-file ownership or mode. Long-syntax
`uid/gid/mode` values are ignored for file-backed secrets, so prepare the host
files explicitly before any container starts:

| Consumer | Host owner | Mode | Examples |
| --- | ---: | ---: | --- |
| API and API tool containers | `10001:10001` | `0400` | field keyring, API/owner DSN, session key, OIDC client secret, break-glass CIDRs |
| API + PDF scanner shared capability | `root:10000` | `0440` | `invoice_pdf_scanner_capability`; mounted read-only, never stored in the socket volume |
| Each source-agent container | `65532:65532` | `0400` private, `0444` public cert | reader DSN, spool/signing/mTLS private keys; CA/client certificate may be public-read |
| Source state directory | `65532:65532` | directory `0700`, files `0600` | complete per-stream state, pending spool, inventory and lock metadata |
| Ingest Nginx | `root:root` | private key `0400`, cert/CA `0444` | ingest server key/certificate and source CA |
| One-time Keycloak bootstrap | `1000:1000` | `0400` | `keycloak_bootstrap_admin_password` only while using the override |
| PostgreSQL init/owner-only secrets | pinned image `postgres` UID (normally `70`, verify it) | `0400` | invoice init passwords and `keycloak_owner_db_password` |
| Keycloak DB app secret shared only by Postgres init and Keycloak | `root:root` | `0444` under the host parent directory `0700` | `keycloak_app_db_password`; owner password remains separate and never enters Keycloak |

First verify the pinned PostgreSQL UID instead of assuming it:

```bash
docker run --rm --entrypoint id \
  postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2 postgres
stat -c '%u:%g %a %n' /root/invoice-system/secrets/*
SECRETS_DIR=/root/invoice-system/secrets POSTGRES_UID=70 \
  bash deploy/preflight-secret-permissions.sh
SECRETS_DIR=/root/invoice-system/secrets POSTGRES_UID=70 \
PRODUCTION_ENV_FILE=/root/invoice-system/app/deploy/.env.production \
CHECK_CONTAINER_READABILITY=true bash deploy/preflight-secret-permissions.sh
```

Generate the scanner capability once on the deployment host without putting it
in `.env`; both containers receive the same file through Compose, and no other
service does. Recreate API and scanner together to rotate it:

```bash
umask 027
openssl rand -hex 32 >/root/invoice-system/secrets/invoice_pdf_scanner_capability
chown root:10000 /root/invoice-system/secrets/invoice_pdf_scanner_capability
chmod 0440 /root/invoice-system/secrets/invoice_pdf_scanner_capability
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  up -d --force-recreate pdf-scanner api
```

Run a container-level readability preflight. For the API image, enter with its
normal UID and require every mounted private secret to be readable but not
group/world readable. Source agents perform the equivalent fail-closed checks
during `check-db`/startup because their scratch image has no shell. Never assume
a successful `docker compose config` proves secret readability or mode.
The PostgreSQL-client `permissions` job also runs explicitly as UID/GID 10001;
with all capabilities dropped, leaving it at the image's default root user
would make the host-owned `0400` owner DSN unreadable rather than more
privileged.

## 5. Central Keycloak reference deployment

This section is optional if an existing IdP meets the same contract.

The pinned Keycloak 26.7.2 exception logic and isolated runtime smoke passed in
the superseded validation bundle
`release/0.1.0-rc1-gate-validation6/release-manifest.json`. Later source changes
correctly make that bundle stale; do not deploy it. Generate a fresh bundle
only after the full PostgreSQL verifier is green. The production realm, MFA
`acr`/`amr`, ID token, RP-initiated logout and back-channel logout canaries must
then pass on the real host. `docs/IMAGE-SCAN-REVIEW.md` is the image-exception
source of truth.

Reserve an unused Keycloak edge network. `KEYCLOAK_EDGE_GATEWAY` must be a
usable address inside `KEYCLOAK_EDGE_SUBNET`, and
`KC_PROXY_TRUSTED_ADDRESSES` must be exactly that one canonical IPv4 address
plus `/32`; Compose passes the same gateway into the startup validator, so a
mismatch, list or subnet is rejected before Keycloak starts. The `keycloak_edge`
attachment has the only positive `gw_priority`, which pins the published-port
peer/default gateway to that trusted `/32` for this multi-network container.
Every other Invoice/Keycloak bridge also has an explicit small CIDR. Docker's
automatic `/16` allocation is forbidden because it can silently cover the
proxy, ingestion and upstream projection ranges before those networks exist.
The reference values are:

```text
KEYCLOAK_HTTP_PORT=58180
KEYCLOAK_ADMIN_HTTP_PORT=58181
KEYCLOAK_DB_SUBNET=172.30.244.0/28
KEYCLOAK_EDGE_SUBNET=172.30.254.0/29
KEYCLOAK_EDGE_GATEWAY=172.30.254.1
KC_PROXY_TRUSTED_ADDRESSES=172.30.254.1/32
```

Run `scripts/preflight-production.ps1` before creation. It checks both loopback
ports, all explicitly planned Docker ranges/internal modes, and equality
between the gateway and trusted `/32`. After creation, compare the actual
gateway without changing it:

```bash
docker network inspect invoice-keycloak-prod_keycloak_edge \
  --format '{{(index .IPAM.Config 0).Gateway}}'
```

Stop if it does not exactly match the configured gateway. The two published
host ports both target Keycloak's private `8080`; their separation exists so
host Nginx can enforce different route and source policies. They must remain
bound to `127.0.0.1` only.

**Production change approval.** The base `deploy/docker-compose.idp.yml` has no
bootstrap administrator environment variable or secret. For first initialization
only, start it with `deploy/docker-compose.idp.bootstrap.yml`:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml up -d keycloak-postgres
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml \
  -f deploy/docker-compose.idp.bootstrap.yml up -d keycloak
```

After the loopback listener is healthy, provision the immutable initial realm
contract exactly once. The password is read with `password@FILE`; bearer tokens
and response bodies live only under root-owned `/dev/shm`. Stdout is one fixed
JSON line, and the only published client secret is `invoice-web` at UID/GID
10001 mode `0400`:

```bash
KEYCLOAK_BOOTSTRAP_PASSWORD_FILE=/root/invoice-system/secrets/keycloak_bootstrap_admin_password \
INVOICE_OIDC_CLIENT_SECRET_FILE=/root/invoice-system/secrets/invoice_oidc_client_secret \
  bash deploy/keycloak/provision-solov-realm.sh
```

Expected output is exactly
`{"status":"ok","realm":"solov","clients":4,"desktop_enabled":false}`.
Re-running or finding an existing output secret is an error; inspect a partial
realm rather than deleting or merging it automatically. Sub2API and New API
client secrets remain inside the restricted Keycloak administration flow and
are never printed by this provisioner.

Before using the bootstrap account, create DNS/TLS for both hostnames and
install the split edge policy. Render
`auth-admin.solov.cc.allow.conf.example` into a host-local, root-owned file by
replacing both TEST-NET routes with the exact normal administrator and
independent break-glass egress addresses. Every IPv4 entry must be `/32`, every
IPv6 entry `/128`, and `deny all;` must remain last. Never allow a LAN, Docker,
Cloudflare or ISP subnet. If Cloudflare proxies either hostname, the global
Cloudflare real-IP configuration must pass the authoritative CIDR check in the
preflight; otherwise the allowlist sees the proxy rather than the operator.

Validate the rendered file before installation:

```bash
bash deploy/validate-keycloak-admin-allowlist.sh \
  /root/invoice-system/config/auth-admin.solov.cc.allow.conf
```

Install:

```text
deploy/nginx/keycloak-proxy-headers.conf
  -> /www/server/panel/vhost/nginx/proxy/keycloak-proxy-headers.conf
rendered admin allowlist
  -> /www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf
deploy/nginx/auth.solov.cc.conf.template
  -> /www/server/panel/vhost/nginx/auth.solov.cc.conf
deploy/nginx/auth-admin.solov.cc.conf.template
  -> /www/server/panel/vhost/nginx/auth-admin.solov.cc.conf
```

Create the `access` and `proxy` directories first if absent. Keep the rendered
allowlist `root:root 0600`; the Nginx master reads it during configuration load.
The shared proxy-header include and vhosts may be `root:root 0644`.

Run `/www/server/nginx/sbin/nginx -t` and reload only after it succeeds. From
an allowed address, `https://auth-admin.solov.cc/admin/` must load/redirect,
while `https://auth.solov.cc/admin/` must return 404. From an unlisted address,
the admin hostname and `https://auth.solov.cc/realms/master/` must return 403.
Public discovery under `https://auth.solov.cc/realms/solov/` must still work.
Do not expose Keycloak port `9000`.

The optimized image uses a 2 GiB memory limit. Immediately create a different,
named permanent administrator, enroll and test TOTP/LoA2, and confirm it can
administer the realm in a fresh browser. Then delete (preferred) or disable the
temporary bootstrap account. Stop Keycloak, delete the bootstrap password file,
remove its username from the host environment, and restart with the base file
only:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml \
  -f deploy/docker-compose.idp.bootstrap.yml stop keycloak
rm -f -- /root/invoice-system/secrets/keycloak_bootstrap_admin_password
unset KEYCLOAK_BOOTSTRAP_ADMIN_USERNAME
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml up -d --force-recreate keycloak
```

Inspect the recreated container and prove neither
`KC_BOOTSTRAP_ADMIN_USERNAME` nor `keycloak_bootstrap_admin_password` is present.
Verify the deleted account cannot log in and the permanent LoA2 administrator
still can. Never use the bootstrap override for an ordinary restart.

The PostgreSQL container is initialized with owner-only
`keycloak_owner_db_password`; `010-keycloak-app-role.sh` creates
`keycloak_app` as `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION
NOBYPASSRLS` and transfers only the Keycloak database/schema ownership. The
running Keycloak container mounts only `keycloak_app_db_password`. Confirm with
`\du+ keycloak_app` and container inspection that it has no owner secret or
cluster-superuser credential before exposing the IdP.

Create realm `solov` with:

- HTTPS required externally, email verification enabled and brute-force
  protection enabled;
- self-registration only during the migration window;
- TOTP required for finance administrators; recovery codes stored offline;
- realm roles `invoice-user` and `invoice-admin` (admin is never a default
  role);
- `acr` and `amr` protocol mappers in ID and access tokens;
- an ACR-to-LoA mapping `urn:solov:loa:2 -> 2`; the level-2 authentication
  flow must require password plus OTP;
- a top-level string-array `roles` claim mapper;
- short access/ID token lifetime (about 5 minutes), normal SSO idle/max limits,
  and no offline access scope for these clients.

Before enabling required email verification, configure realm SMTP and use the
Keycloak **Test connection** action. Use a dedicated QQ/enterprise mailbox with
an authorization code, or Gmail with a Google App Password, different from the
invoice notification mailbox; do not put it in Compose or a plaintext environment variable. Enter it only in
the restricted Keycloak admin console, enable Keycloak admin events, document
the authorized operators and rotate it through the same console. Register a
canary account, receive and consume the verification message, then confirm its
ID token contains `email_verified=true`. If SMTP testing or this canary fails,
keep registration/email verification rollout disabled: invoice profile saving
correctly fails closed without a verified address.

Create distinct clients and secrets:

| Client | Type | Exact redirect |
| --- | --- | --- |
| `invoice-web` | confidential, code + PKCE | `https://invoice.solov.cc/api/v1/auth/callback` |
| `sub2api` | confidential, code + PKCE | `https://api.solov.cc/api/v1/auth/oauth/oidc/callback` |
| `newapi` | confidential generic OAuth/OIDC | `https://xm.solov.cc/oauth/solov-sso` |
| `invoice-desktop` | public, code + PKCE | the registered desktop loopback/deep-link callback only |

For `invoice-web`, also configure:

- valid post-logout redirect: exactly `https://invoice.solov.cc/`;
- back-channel logout URL: exactly
  `https://invoice.solov.cc/api/v1/auth/backchannel-logout`;
- Backchannel Logout Session Required = ON and Front Channel Logout = OFF.

The invoice API derives RP-initiated logout only from the discovery
`end_session_endpoint`. Startup fails unless discovery advertises back-channel
logout. Logout tokens are accepted only as a bounded form POST and must pass
signature, exact issuer/audience, `iat`/`exp`, `jti`, empty logout event and
no-`nonce` checks. The first valid `jti` atomically revokes by `sid` (or by
issuer+subject when `sid` is absent), appends audit and creates an immutable
replay record; later deliveries are idempotent.

Do not share client secrets. Add exact web origins only where the IdP requires
them. The invoice backend validates issuer, discovery endpoints, JWKS,
signature algorithm, audience, authorized party, nonce, ACR and AMR.
Every client above uses `https://auth.solov.cc/realms/solov` as its issuer.
Never place `auth-admin.solov.cc` in an issuer, discovery URL, redirect URI,
allowed endpoint host or application web-origin list. Raw authorization query
strings are not written to either Keycloak edge access log; Keycloak user/admin
events provide the audit source.

After DNS/TLS and the provider metadata are live, but before starting the
invoice API, run the provider contract gate:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools \
  run --rm --no-deps oidc-preflight
```

The command performs discovery and JWKS reads only. It intentionally receives
no client secret and no database credential. A passing JSON report must show
`status=ok`, `issuer_https=true`, all five endpoint checks true,
`endpoint_address_policy=true`,
`allowed_signing_algorithms=["RS256"]`, and both back-channel logout fields
true. It never prints the issuer, endpoint URLs or authorization material.

Any non-zero exit blocks API startup. In particular, do not waive missing
`userinfo_endpoint`, `end_session_endpoint`,
`backchannel_logout_supported`, or
`backchannel_logout_session_supported`; do not add an endpoint host merely to
make an unexpected provider URL pass. Re-run this gate after every IdP upgrade,
hostname/certificate change, signing-policy change, or discovery metadata
change. The provider must also continue to pass the real login, MFA step-up,
RP-initiated logout and signed back-channel logout canary flows; discovery-only
success is necessary but not sufficient for launch.

For the normal public `auth.solov.cc` path, keep
`OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS` empty. If an approved private IdP is used,
list each exact canonical RFC1918/ULA address in that variable and record the
DNS/address review in the deployment change. The client pins each connection
to its validated DNS answer; it never permits loopback, link-local or metadata
addresses as exceptions.

## 6. Invoice database, migrations and initial settings

**Production change approval.** Verify every explicit network in
`deploy/.env.production` (`INVOICE_EDGE/DB/APP`, ClamAV/OIDC egress,
proxy/ingest, both projection networks, and Keycloak DB/edge) against every
existing Docker network. Never let Compose auto-allocate one of these bridges.
Set `INVOICE_PROXY_GATEWAY_IP` to one usable address inside the proxy subnet
and set `TRUSTED_PROXY_CIDRS` to exactly that address plus `/32`. Compose pins
the bridge gateway to the same value and assigns `invoice_proxy` the only
positive `gw_priority`; static verification compares all three, and the API
rejects subnets, multiple trusted proxies and every non-host prefix.

Run in this exact order:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d postgres clamav pdf-scanner

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm migrate

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm permissions

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm bootstrap-settings

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm bootstrap-sources
```

ClamAV has no published port. It joins the internal API network plus a dedicated
egress-only bridge so FreshClam can update the persistent signature volume. The
API mounts that volume read-only and rejects every upload when `daily.*` or the
last FreshClam update check is older than `CLAMAV_MAX_SIGNATURE_AGE` (default
48h), even if clamd still answers PING. The upload chain then streams the file
over an authenticated Unix socket to the pinned qpdf 12.3.2 structural scanner.
The scanner has `network_mode: none`, no API/DB/document mounts or application
secrets, and fixed 256 MiB/0.5 CPU/32 PID/two-scan limits; its only credential
is the root-owned read-only scanner capability. Encrypted PDFs, JavaScript/actions,
launch/remote URI behavior, forms/XFA, embedded files and rich media are
rejected before encrypted promotion.

The bootstrap command refuses to overwrite an existing setting row. All later
changes use OIDC/MFA/admin-IP-protected typed APIs. The application runtime role
can append but cannot update/delete audit records and cannot mutate migration
checksums.

## 7. Source-instance provisioning and read-only agents

**Production change approval.** Create two `source_instances` rows with UUIDs
and provision receiver streams `payments`, `identities`, `usage`, `credits`,
and `balances`. The UUID itself is
the source agent `SOURCE_ID`; slugs such as `sub2api-primary` are invalid for
the production receiver database foreign key.

Create two database-only internal bridges and attach only the existing database
containers. Do not put a source agent on either upstream application's normal
network. The reviewed helper reads only the four named values from the Compose
env file, verifies any pre-existing network before reuse, and refuses a wrong
alias:

```bash
PRODUCTION_ENV_FILE=deploy/.env.production \
  bash deploy/provision-projection-networks.sh
```

The ten source DSN secret files must use only
`sub2api-projection-db:5432` or `newapi-projection-db:5432`. Confirm both
networks have `Internal=true` and exactly the expected database plus five source
agents. If a named network already exists, inspect it and reuse it only when
its subnet, internal flag and members match; do not silently accept a different
object.

Review, then execute the V2 identity and V3 economic templates as the upstream
database owner. Production has ten active LOGIN roles/DSN secret files: one
identity plus four V3 economic streams per source. The two legacy V2 payment
compatibility holders (`invoice_sub2api_payments_reader` and
`invoice_newapi_payments_reader`) must exist as `NOLOGIN`, connection-limit-0
roles with no password; the templates enforce that state and no container uses
them.

- `contracts/sub2api-source-projection-grants.postgresql.sql`;
- `contracts/newapi-source-projection-grants.postgresql.sql`;
- `contracts/sub2api-economic-projection-grants.postgresql.sql`;
- `contracts/newapi-economic-projection-grants.postgresql.sql`.

The read roles must have `default_transaction_read_only=on`, a short statement
timeout, no role inheritance from the application owner and no write/schema
privilege. Validate using attempted `INSERT`, `UPDATE`, `DELETE`, `CREATE` and
access to password/session/client-secret columns; every attempt must fail. The
agent repeats this as a fail-closed startup inventory: its effective SELECT
columns must equal the reviewed template exactly, with no role membership or
sequence privilege.

The Sub2API payments template creates
`invoice_sub2api_payment_projection_v1`; its companion health view exposes only
four aggregate counts. Neither patches Sub2API source or tables. Before
starting the agent, prove no row is blocked:

```sql
SELECT total_rows,exposed_cny_rows,unsupported_known_non_cny_rows,
       blocked_unknown_currency_rows
FROM public.invoice_sub2api_payment_projection_health_v1;
```

Any non-zero `blocked_unknown_currency_rows` blocks launch pending explicit
finance/DBA review. Known non-CNY rows are intentionally excluded from V1 but
do not block reconciliation. After launch, blocked-unknown evidence raises a
source-health alert and suspends missing-row tombstones while exposed CNY rows
continue syncing. The only reviewed
missing-snapshot CNY derivation is for v0.1.179 provider keys `easypay`,
`alipay`, and `wxpay`; Stripe/Airwallex with explicit non-CNY are known but
unsupported, while missing/contradictory/invalid evidence remains blocked.
Do not grant the reader direct `provider_snapshot` or `payment_orders` access.
PostgreSQL TEMP is granted through PUBLIC by default;
the templates fail until the source database owner has removed that effective
privilege for these isolated readers.

The New API template likewise creates only
`invoice_newapi_oidc_provider_contract_v1` and
`invoice_newapi_oidc_binding_projection_v1`. Before starting identities sync,
verify exactly one `solov-sso` provider row has `contract_ok=true`; the reader
must fail on both raw OAuth tables and client-secret/policy/mapping fields. The
agent independently validates all four endpoints against the configured exact
HTTPS issuer on every scan.

Each production stream uses its own source-agent container and state/spool
files. Required variables are defined in `docs/CONFIGURATION.md` section 5.
The only cross-system path is outbound HTTPS with mTLS plus Ed25519 signatures.
The agent never receives an invoice PostgreSQL credential.

- Sub2API payments V3: post-cutover `(updated_at,id)` scans with exact wallet
  unit/config/currency proof;
- Sub2API usage/credits/balances: wallet events, non-cash sources and atomic
  encrypted balance snapshots;
- negative `admin_balance`/`admin_concurrency` redeem records are not positive
  credit facts and therefore do not poison the credits contract. A
  non-positive used `balance` credit still fails closed; unexplained
  administrator deductions remain visible to the signed balance checkpoint
  and freeze eligibility instead of creating invoiceable cash;
- Sub2API identities: only the configured central OIDC provider/issuer;
- New API payments: every row remains `pending_manual`; every publishable cycle
  is a full post-cutover scan because there is no `updated_at`;
- New API usage/credits/balances: consume logs, mutable-code full rescans and
  atomic balance snapshots; logging/configuration drift blocks watermarks;
- New API identities: only the two reviewed security-barrier views. The provider
  contract must prove well-known, authorization, token and user-info endpoints
  belong to the exact central issuer and slug `solov-sso`; the reader cannot
  select either raw OAuth table or any client secret.

ACK loss must replay the byte-identical encrypted-spool batch. Never delete an
agent state/spool file to "fix" a cursor; use the documented recovery check.

Build the disposable `keygen` Docker target, generate one distinct Ed25519 key
per source/stream, delete that tools image, and copy
only each `.pub.b64` value into `source-trust.json`:

```bash
docker build --target keygen -f agents/Dockerfile.production \
  -t invoice-source-keygen:one-time agents
install -d -m 0700 -o 65532 -g 65532 /root/invoice-system/source-keygen-work
docker run --rm --user "65532:65532" \
  -v /root/invoice-system/source-keygen-work:/work \
  invoice-source-keygen:one-time \
  -key-id 2026-08-payments \
  -private-out /work/sub2api_payments_signing_key.pem \
  -public-out /work/sub2api_payments_signing_key.pub.b64
# Repeat for the other nine source/stream tuples, then review and promote:
install -d -m 0755 -o root -g root /root/invoice-system/config/source-public-keys
install -m 0400 -o 65532 -g 65532 /root/invoice-system/source-keygen-work/*_signing_key.pem /root/invoice-system/secrets/
install -m 0444 -o root -g root /root/invoice-system/source-keygen-work/*.pub.b64 /root/invoice-system/config/source-public-keys/
rm -rf /root/invoice-system/source-keygen-work
docker image rm invoice-source-keygen:one-time
```

Create ten empty state directories and
`$SOURCE_STATE_ROOT/cutover/{sub2api,newapi}` mode 0700, owned by UID/GID 65532.
Generate distinct spool/signing keys per stream plus one cutover and one balance
snapshot AES key per source. Do not
start or initialize the agents yet: even `docker compose run` must attach the
external ingestion network, which is created with the API in section 9.

Every signed batch must match the audited `source_instances.runtime_version`
and carry `projection_status=healthy`. A source upgrade is a controlled CAS:
stop/drain its five agents (including pending spools), set
`expected_previous_runtime_version` in the source bootstrap file, rerun
`bootstrap-sources`, then restart and canary. The API requires fresh payments
and four economic plus identity heartbeats and zero queued/dead events before readiness, user
submission or final manual issue confirmation. OIDC/account dependency waits
remain visible but do not make unrelated users unhealthy. They use exact HMAC
wakeups plus a 12-hour fallback; alert when the parked count approaches
100,000 rather than shortening that interval or deleting paid-user evidence.

## 8. Configure upstream OIDC without source changes

**Production change approval.** Take an upstream database/config backup first.

### Sub2API

Use its administrator settings page. Set:

- issuer/discovery to the `solov` realm;
- backend redirect to
  `https://api.solov.cc/api/v1/auth/oauth/oidc/callback`;
- frontend redirect to `/auth/oidc/callback`;
- scopes `openid profile email`;
- signing algorithms `RS256`;
- PKCE = true;
- validate ID token = true;
- require verified email = true;
- enable OIDC only after a canary admin and user both bind successfully.

Existing users must log in with their existing account and explicitly bind the
new IdP. Never merge by email. Preserve password login during migration.

Add only the user custom menu:

```text
开票中心 -> https://invoice.solov.cc/embed/sub2api
```

The Nginx entry disables access logging and returns a clean 303, so the Sub2API
JWT automatically appended to the iframe URL is neither consumed nor retained.
Do not add the administrator page as an iframe; administrators use the
top-level `https://invoice.solov.cc/admin` URL.

### New API

Prefer a custom OAuth provider with stable slug `solov-sso`, not the older
single `users.oidc_id` path. Configure discovery/endpoints from the same IdP,
map `sub`, `preferred_username`/`name` and `email`, and require
`email_verified=true` in its access policy.

Existing users first log in with their original credential and bind the
provider. Disable open OAuth registration until the migration policy is
approved. Never auto-merge duplicate-email accounts.

If using the Chats iframe feature, configure only:

```text
https://invoice.solov.cc/embed/newapi
```

Never use `{key}` or append a New API model/API token.

## 9. Public invoice service and Nginx

Start the API process and internal mTLS ingress after migrations, permissions,
settings, ten agent state directories and both encrypted cutover pairs are ready. The ingest proxy waits
for the API process, not its readiness: readiness itself requires the first
ten signed heartbeats, so a health dependency would deadlock cold start.

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d api web ingest-proxy

# First installation only: prove all ten DB roles.
all_sources=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
for service in "${all_sources[@]}"; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm "$service" check-db
done

# Capture each source exactly once in one RR/RO transaction. A rerun must fail.
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm sub2api-cutover-init
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm newapi-cutover-init
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm sub2api-cutover-init check-cutover
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm newapi-cutover-init check-cutover

# Initialize every independent cursor/sequence. Only V2 identities have a
# deletion-reconciliation state file.
for service in "${all_sources[@]}"; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm "$service" init-state
done
for service in sub2api-identities newapi-identities; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm "$service" init-reconcile
done

# Register trust first, then let manifest-only balances sequence 1 commit.
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --wait --wait-timeout 300 \
  sub2api-balances newapi-balances
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --wait --wait-timeout 300 \
  sub2api-payments sub2api-usage sub2api-credits newapi-payments newapi-usage newapi-credits
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --wait --wait-timeout 300 \
  sub2api-identities newapi-identities

# Now wait for the source heartbeats, event drain, ClamAV/scanner and API health.
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d --wait --wait-timeout 300 \
  api web ingest-proxy

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml ps
```

All ten source services must remain `running` without a restart-count increase;
the API must become healthy only after all five streams for both enabled sources
are fresh. Keep the public vhost/menu disabled if the five-minute wait expires.

Install these files in the BT Nginx locations shown in their templates:

- `invoice-http-context.conf` in the `http {}` context;
- `invoice-common-headers.conf` as
  `/www/server/panel/vhost/nginx/proxy/invoice-common-headers.conf` and
  `invoice-security-headers.conf` as
  `/www/server/panel/vhost/nginx/proxy/invoice-security-headers.conf` (the BT
  vhost-root `*.conf` glob is the `http {}` context and must not load this
  server-only snippet globally);
- `invoice.solov.cc.conf.template` as the enabled vhost.

Issue TLS, run `/www/server/nginx/sbin/nginx -t`, then reload. The vhost:

- publishes no database, ClamAV or ingestion port;
- overwrites forwarded/mock/mTLS headers;
- blocks `/internal/` publicly;
- disables logging on token-bearing embed and OIDC paths;
- streams the exact PDF upload and download routes without host-disk request or
  response buffering;
- disallows administrator framing;
- routes only `/api/` to the API and all other application paths to the web
  container.

`INVOICE_INGEST_PROXY_IP` must be inside `INVOICE_INGEST_SUBNET`, and
`INVOICE_INGEST_PROXY_CIDR` must be the exact `/32` for that address. Do not
expand it to the bridge subnet: the API trusts mTLS assertion headers only from
this one container address, while each source agent independently pins the
same destination `/32`. Keep the fixed proxy address outside
`INVOICE_INGEST_DYNAMIC_RANGE` so the API or an agent cannot receive it before
the proxy starts; the supplied `/28` layout reserves `.14` for this purpose.

## 10. Canary acceptance

Use one finance admin, one Sub2API user and one New API user.

1. Login, logout and login again; confirm cookie flags and CSRF rejection.
   Logout must first revoke the local opaque session, then navigate the top
   window through Keycloak's discovered end-session endpoint and return only
   to `https://invoice.solov.cc/`. Because the service deliberately does not
   persist an ID token, Keycloak may show one explicit logout-confirmation
   page; zero-click IdP logout is not a V1 requirement. Terminate a canary
   session in Keycloak and
   prove the signed back-channel callback immediately invalidates its invoice
   session; replay the same logout token and confirm one immutable replay row
   and one audit event only.
2. Perform administrator LoA2/OTP step-up from an allowed IP. Confirm denial
   from another IP and successful break-glass only through the approved VPN.
   Also confirm public `/admin` is 404 for every source, the admin hostname is
   403 outside the exact allowlist, the allowed Admin Console completes its
   `master`-realm login, and public `solov` discovery/login remains available.
3. Bind both upstream accounts explicitly through the central IdP.
4. Confirm source agent identity events produce the two connected-source rows.
5. Confirm completed Sub2API balance orders use exact CNY `pay_amount`; test a
   recharge multiplier and subscription conversion. For refunds, confirm the
   cap is `pay_amount-round_CNY(pay_amount*refund_amount/amount)`, including
   full, partial and half-cent cases.
6. Confirm New API `success` appears only in the finance payment-candidate
   queue; an amount above signed `money` fails, one admin leaves it proposed,
   the same admin cannot self-approve, a second admin must enter the identical
   amount/evidence, and neither reviewer can issue the invoice.
   Also confirm an administrator cannot review/adjust/issue/upload or resolve a
   refund case belonging to their own invoice identity.
7. On a verified New API test lot, record a partial manual refund with evidence.
   Confirm unissued reservations are rejected/released, issued exposure enters
   `refund_attention`, the old dual approval is invalidated, source rescan does
   not restore the cap, and only a fresh two-person review of the lower net
   amount can unfreeze it. Repeat with remaining cap `0.00` for full freeze.
8. Save personal and enterprise invoice profiles. A forged browser
   `email_verified=true` must still fail.
9. Submit CNY 199.99 (reject), CNY 200.00 (accept), partial allocation and two
   same-source orders; reject cross-source mixing and duplicate use.
10. Review, begin manual issue, confirm issue, upload a clean static PDF, verify
   ClamAV rejection with EICAR, and verify qpdf rejection of encrypted,
   JavaScript, Launch, external URI and embedded-attachment samples in a
   non-production request. Make the signature volume stale in a canary stack
   and prove upload fails closed even while clamd still responds. Inspect the
   scanner container and prove `network_mode: none`, only the socket volume and
   scanner capability are mounted, and no API/database/document secret or
   ciphertext volume is present; stopping it must make readiness and upload
   fail closed.
11. Confirm email contains no attachment/bearer URL and the authenticated user
    can download only their own encrypted-at-rest PDF.
12. Lose an ingestion ACK, restart the agent and prove byte-identical replay.
13. Apply a Sub2API refund: unissued reservation is invalidated; an issued
    request creates an open refund case; resolve it with red-letter evidence.
14. Run the signed encrypted backup and isolated restore drill. Tamper with a
    copy of the manifest and confirm restore exits before the first `age`
    decrypt; also confirm a symlink/FIFO/PAX archive fixture is rejected.
15. Confirm no secret, OIDC code, upstream JWT, tax ID or email appears in
    Nginx/application logs.

## 11. Backup and restore

Install `age` and OpenSSH, and keep the private age identity offline. Backup
authenticity uses a different Ed25519 signing key and the fixed
`solov-invoice-backup-v1` namespace. Generate it on an offline encrypted volume
(not the production host and never under `BACKUP_DIR`); the dedicated key may
be unencrypted only because the volume itself is encrypted and is mounted for
the approved backup window alone:

```bash
umask 077
ssh-keygen -q -t ed25519 -N '' \
  -C invoice-backup-2026 -f /offline/invoice-backup-signing-2026
printf 'invoice-backup namespaces="solov-invoice-backup-v1" %s\n' \
  "$(cat /offline/invoice-backup-signing-2026.pub)" \
  >/offline/backup-allowed-signers
chmod 0400 /offline/invoice-backup-signing-2026
chmod 0444 /offline/backup-allowed-signers
```

Copy only `backup-allowed-signers` to the production configuration directory
and review it offline. Every non-comment line must use principal
`invoice-backup`, the exact namespace and `ssh-ed25519`; broad namespaces,
wildcard principals and RSA/ECDSA keys are rejected. Temporarily mount the
encrypted signing volume read-only for the command below, then unmount it
immediately after the script self-verifies and publishes the signature.

An API crash after encrypted promotion but before database attach can leave an
unreferenced object. Never remove it manually. In the maintenance window, stop
API/ingest and all ten agents, run the tools-image GC in dry-run mode, review
the random object keys/ages, then execute only with an audited reason. It
rechecks the database immediately before each deletion, accepts only old
`issued/*.pdf.enc` files and fsyncs the directory:

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml stop api ingest-proxy
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml stop \
  sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances \
  newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm document-gc \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --document-root /data/documents --minimum-age 24h
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm document-gc \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --document-root /data/documents --minimum-age 24h --execute \
  --maintenance-confirmed --reason 'scheduled pre-backup orphan cleanup'
```

The backup command below owns the final write freeze and waits for the API and
all source-agent health checks before returning them to service:

```bash
BACKUP_DIR=/root/invoice-system/backups \
AGE_RECIPIENT_FILE=/root/invoice-system/config/backup-recipients.txt \
SOURCE_STATE_ROOT=/root/invoice-system/source-state \
SOURCE_CUTOVER_ROOT=/root/invoice-system/source-state/cutover \
BACKUP_SIGNING_KEY_FILE=/mnt/offline-signing/invoice-backup-signing-2026 \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
PRODUCTION_ENV_FILE=/root/invoice-system/app/deploy/.env.production \
BACKUP_QUIESCE_CONFIRMED=YES \
BACKUP_LOCAL_KEYCLOAK=true \
  bash deploy/backup/backup.sh
```

The script first stops API/ingest and all ten agents, then captures PostgreSQL,
the encrypted document volume, every complete source state directory (including
pending spool, reconciliation inventory and lock metadata), receiver/document
indexes and optional local Keycloak data within one write-freeze window. Failure
cleanup attempts to restart all quiesced services. It intentionally does not
copy the field keyring, age private identity, ten spool keys, two cutover keys
or two balance-snapshot keys; back those up separately/offline and keep each
key paired with its state archive.
The checksum manifest is not a valid backup package until the script has made
an OpenSSH Ed25519 `.sha256.sig`, verified it against the reviewed signer file,
and atomically published both. Unsigned/orphaned components are incomplete and
must never enter retention as a successful backup.

Run `deploy/backup/restore-drill.sh` against every release backup with the
matching manifest and signature, source UUIDs, offline age identity and field
keyring. Signature verification and exact component-name validation occur
before any checksum or `age` decryption. The drill's streaming archive parser
allows only bounded directories/regular files and rejects duplicate/traversal
paths, links, devices, FIFO/socket entries, PAX/xattr metadata and resource
overruns before extraction. It then restores PostgreSQL, compares migration,
receiver-state and document indexes byte-for-byte, validates all ten source
state envelopes, and runs `invoice-backup-verify` to authenticate/decrypt a
bounded sample of document objects and compare plaintext size/SHA-256 to the
restored database. A backup is invalid until this passes.

```bash
DATABASE_BACKUP=/root/invoice-system/backups/invoice-TS.postgres.dump.age \
DOCUMENT_BACKUP=/root/invoice-system/backups/invoice-TS.documents.tar.age \
SOURCE_STATE_BACKUP=/root/invoice-system/backups/invoice-TS.source-state.tar.age \
METADATA_BACKUP=/root/invoice-system/backups/invoice-TS.metadata.tar.age \
KEYCLOAK_BACKUP=/root/invoice-system/backups/invoice-TS.keycloak.dump.age \
BACKUP_MANIFEST=/root/invoice-system/backups/invoice-TS.sha256 \
BACKUP_SIGNATURE=/root/invoice-system/backups/invoice-TS.sha256.sig \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
AGE_IDENTITY_FILE=/offline/backup-age-identity.txt \
FIELD_KEYRING_FILE=/offline/invoice_field_keyring.json \
SOURCE_SPOOL_KEY_ROOT=/offline/source-spool-keys \
SUB2API_SOURCE_ID="$SUB2API_SOURCE_ID" NEWAPI_SOURCE_ID="$NEWAPI_SOURCE_ID" \
SUB2API_RUNTIME_VERSION=0.1.179 NEWAPI_RUNTIME_VERSION=v1.0.0-rc.25 \
SUB2API_BALANCES_SIGNING_KEY_ID=2026-08-balances \
NEWAPI_BALANCES_SIGNING_KEY_ID=2026-08-balances \
INVOICE_TOOLS_IMAGE=invoice-system-tools:0.1.0 \
SOURCE_AGENT_IMAGE=invoice-source-agent:0.3.0 \
  bash deploy/backup/restore-drill.sh
```

### 11.1 OIDC back-channel logout replay retention

`oidc_backchannel_logout_events` is security state, not an ordinary transient
job table. Keep it in every PostgreSQL backup and run retention only after the
new signed/encrypted backup and its isolated restore drill above have both
succeeded. Retain that preceding backup under the normal backup-retention
policy; never restore only this table into a live database because its replay
records and immutable audit trail are one security history.

The maintenance tool is owner-only even if another role is accidentally
granted `DELETE`; the `invoice_app` runtime role remains limited to
`SELECT/INSERT`. It defaults to a read-only preview, uses the PostgreSQL clock,
holds a dedicated advisory lock, applies bounded statement/lock timeouts and
deletes in batches. A row is eligible only when **both** `received_at` and
`token_expires_at` are strictly older than the retention cutoff. The compiled
minimum is 180 days (`4320h`) and cannot be lowered, so a logout token still in
any replay window cannot be removed by this command. Every delete batch and
the completion marker are inserted into `audit_events` in the same transaction;
an audit failure rolls that batch back.

Quarterly, first preview with the default 365-day retention. Review the count
and cutoff without copying hashes or row contents out of PostgreSQL. For
execution, open an approved maintenance window, stop the API (which is the only
writer of these events), repeat the preview, then provide all three explicit
write controls: `--execute`, `--maintenance-confirmed` and a bounded reason.
The source agents do not write this table, but stop `ingest-proxy` as well so
they spool safely while the API is down:

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml stop api ingest-proxy

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500 --execute --maintenance-confirmed \
  --reason 'quarterly OIDC logout replay retention after verified backup'

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml up -d api ingest-proxy
```

Record the emitted `request_id`, starting eligible count, deleted count, batch
count and cutoff in the maintenance ticket. Confirm the matching
`auth.backchannel_logout.retention.batch` and
`auth.backchannel_logout.retention.complete` audit rows before closing the
window. A busy-lock, timeout, owner mismatch or audit error is a failed
maintenance run; investigate it instead of adding grants, lowering retention
or bypassing the tool with manual SQL.

Omit `KEYCLOAK_BACKUP` only when the matching signed manifest was created with
`BACKUP_LOCAL_KEYCLOAK=false`; the restore rejects a missing or extra component.
To rotate signing keys, first append the new namespace-bound Ed25519 public key
to the reviewed allowed-signers file, run and restore-test one backup with the
new private key, and retain the old public line until every backup signed by it
has expired. Removing an old line early makes those retained backups
intentionally unverifiable. Never reuse the age identity as the signing key or
change the namespace/principal during a rotation.

For a real recovery, keep ingress/API/agents stopped. Restore the matched
database, document archive, all ten source directories and both cutover pairs first; restore the
matching field/spool keys and ownership (`65532`, directories `0700`, state
files `0600`); run the drill and migration verify; only then start the API and
receiver, followed by the ten agents. Never start an agent against restored DB
state while its cursor/pending spool is from a different snapshot.

## 12. Rollback

Before first public traffic, rollback is simply the previous image digest and
database backup. After traffic is accepted:

- never point DNS at an older writable database;
- stop user/source ingress before restoring a database;
- preserve current database/documents/audit as incident evidence;
- application images may be rolled back only if their declared migration
  compatibility includes the current schema;
- otherwise restore the matched database + document backup together into a new
  isolated stack, validate, then switch the vhost;
- after all ten source agents are stopped, disconnect the two upstream
  database containers from their invoice projection networks and remove those
  empty networks; never disconnect either database from its original upstream
  network;
- before disconnecting, run the matching reviewed rollback contract as the
  source database owner:
  `contracts/sub2api-projection-rollback.postgresql.sql` or
  `contracts/newapi-projection-rollback.postgresql.sql`. Each transaction first
  forces all six source-specific roles to NOLOGIN, refuses any active reader
  session, drops only named auxiliary views with `RESTRICT` (never `CASCADE`),
  drops their owned grants/roles, and restores the recorded deployment-before
  `PUBLIC TEMPORARY` baseline. A missing role/view or dependency aborts the
  whole rollback instead of accepting a partial source boundary;
- OIDC configuration rollback re-enables prior login methods but must not
  delete IdP bindings or silently merge accounts.

## 13. Final go/no-go

Go only when every canary item passes, backup restore is proven, no P0/P1
security issue remains, the finance/legal issuer values are approved, and the
operator has recorded exact image/source/database/config versions. The chosen
OIDC provider itself must also have an approved immutable-image scan or a
reviewed managed-service assurance record; the rejected Keycloak/ZITADEL
images in `docs/IMAGE-SCAN-REVIEW.md` are explicit NO-GO inputs. Otherwise keep
the public menu disabled and the source agents stopped.
