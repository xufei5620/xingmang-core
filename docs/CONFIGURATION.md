# Secure configuration guide

The local development defaults are intentionally non-production:

```text
APP_ENV=development
SOURCE_MODE=mock
AUTH_MODE=mock
SMTP_HOST=mailpit
ADMIN_BOOTSTRAP_IP_ALLOWLIST=127.0.0.1/32,::1/128
```

No production hostname, Admin API Key, upstream database password, JWT secret or
SMTP credential belongs in `.env`. Production secrets must be delivered through
root-controlled file mounts; the SMTP App Password is submitted once through
the protected admin command and retained only as field-keyring ciphertext.

## Current implementation status

The repository contains both the loopback mock stack and a separate production
runtime. Production OIDC/MFA/RBAC, PostgreSQL settings/audit/snapshots,
break-glass enforcement, write-only SMTP rotation, ClamAV/qpdf/encrypted
documents, durable source state and mTLS/signing are implemented and covered by
the local gates. `APP_ENV=production` rejects either mock mode.

The live deployment is still gated on real DNS/TLS/IdP/SMTP/source credentials,
reviewed upstream database grants, image/vulnerability scans, an encrypted
restore drill and canary acceptance. V1 is explicitly single-replica; it does
not claim two-phase CIDR activation or cross-replica policy acknowledgement.

## 1. Local development

Copy `deploy/.env.example` to `deploy/.env`, then start the infrastructure:

```powershell
docker compose --env-file deploy/.env -f deploy/docker-compose.dev.yml up -d
```

Default services bind to localhost only:

- PostgreSQL: `127.0.0.1:55432`
- Redis: `127.0.0.1:56379`
- Mailpit SMTP/UI: `127.0.0.1:58025` / `127.0.0.1:58080`
- Mock source: `127.0.0.1:58081`

The development bridge permits host access so local Go/Node tooling can reach
these loopback bindings. It is not the production topology. Production must
use separate internal and edge networks and must not publish PostgreSQL,
Redis, Mailpit, source-agent, or object-storage ports.

The mock source returns the versioned example contract. It has no route to a
production source.

## 2. OIDC

Production uses one central OIDC issuer and distinct clients:

| Client | Type | Requirements |
| --- | --- | --- |
| Invoice web | confidential | authorization code, PKCE S256, exact redirect URI |
| Invoice admin | confidential | authorization code, PKCE S256, MFA/ACR policy |
| Desktop | public | system browser, authorization code, PKCE S256; no client secret |

Validate issuer, audience, signature, expiry, nonce and state. Pin allowed
signing algorithms. Use `(issuer, subject)` as the identity key; email is display
metadata only. Redirect URIs must be exact HTTPS values with no wildcard.

Recommended variables:

```text
OIDC_ISSUER_URL=https://auth.example.invalid/realms/invoice
OIDC_CLIENT_ID=invoice-web
OIDC_CLIENT_SECRET_FILE=/run/secrets/invoice_oidc_client_secret
OIDC_DESKTOP_CLIENT_ID=invoice-desktop
OIDC_REQUIRED_ADMIN_ACR=urn:invoice:mfa
OIDC_REQUIRED_ADMIN_AMR=otp
OIDC_LOGOUT_TOKEN_MAX_AGE=10m
OIDC_ALLOWED_ENDPOINT_HOSTS=auth.example.invalid
OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS=
OIDC_ALLOWED_SIGNING_ALGS=RS256
OIDC_MAX_HTTP_RESPONSE_BYTES=1048576
OIDC_PREFLIGHT_TIMEOUT=30s
```

The web/admin UI shares one confidential invoice relying-party client but
administrator authorization is still enforced by role + ACR + AMR + IP at the
invoice API. Configure exact post-logout URI `https://invoice.solov.cc/` and
exact back-channel logout URL
`https://invoice.solov.cc/api/v1/auth/backchannel-logout`; require the `sid`
claim and disable front-channel logout. The API refuses a provider that does
not advertise a safe end-session endpoint and back-channel logout support.

### Provider contract preflight

Before starting or upgrading the production API, run the tools-only
`invoice-oidc-preflight` command (the production Compose service is named
`oidc-preflight`). It uses the same strict discovery/JWKS implementation as
the runtime and fails closed unless all of the following are true:

- the configured issuer is an exact HTTPS URL with no userinfo, query or
  fragment;
- every authorization, token, userinfo, JWKS and end-session endpoint is HTTPS,
  query-free and on an exact `OIDC_ALLOWED_ENDPOINT_HOSTS` entry;
- every endpoint hostname is resolved once per connection and every returned
  address must be publicly routable, unless an exact canonical RFC1918/ULA
  address is listed in `OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS`; loopback,
  link-local, metadata, shared, benchmark and documentation ranges cannot be
  exempted;
- discovery advertises authorization code flow, PKCE S256, the configured token
  authentication method and at least one explicitly allowed ID-token signature
  algorithm (production default: only `RS256`);
- the JWKS is bounded, duplicate-key-free, contains no private key material and
  has a usable public signing key whose type/curve matches the allowed
  algorithm (RSA keys must be at least 2048 bits);
- both `backchannel_logout_supported` and
  `backchannel_logout_session_supported` are `true`.

Redirects, proxy-environment routing, oversized responses, duplicate JSON keys,
issuer mismatch, DNS rebinding to a non-public address and endpoint-host SSRF
are rejected. Success output contains
only Boolean evidence, the negotiated algorithm list and a SHA-256
configuration fingerprint; it never contains raw issuer/endpoint URLs, query
strings, tokens or a client secret. The CLI accepts
`OIDC_CLIENT_SECRET_FILE` for environment parity and reports only whether the
path was configured, but discovery-only mode deliberately never reads the file
or performs a token exchange. The supplied Compose service therefore mounts no
OIDC secret.

`RequireBackchannelLogout` is hard-enabled in the preflight command and remains
mandatory in production API startup; the tool has no switch to weaken it.

Keep `OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS` empty for a public or hosted IdP. A
self-hosted private IdP may list only the exact private addresses returned by
its approved DNS record; do not use a subnet and remove obsolete addresses
immediately. The API runtime and preflight tool consume the same address list.

For the supplied Keycloak deployment, the client-facing issuer remains
`https://auth.solov.cc/realms/solov`. `auth-admin.solov.cc` is an
operator-only administration hostname; it must never be configured as an
application issuer, redirect URI, discovery host or browser origin. Keycloak is
started with fixed frontend/admin URLs and one exact trusted Docker gateway:

```text
KC_HOSTNAME=https://auth.solov.cc
KC_HOSTNAME_ADMIN=https://auth-admin.solov.cc
KC_PROXY_HEADERS=xforwarded
KC_PROXY_TRUSTED_ADDRESSES=172.30.254.1/32
```

The production entrypoint rejects a subnet, multiple entries, IPv6, an
uncanonical address or a missing `/32`, and compares it to the Compose network
gateway before Keycloak starts. The host Nginx publishes only the
documented public Keycloak paths on `auth.solov.cc`, always rejects `/admin`,
and sends the administration hostname to a separate loopback port protected by
an exact administrator and break-glass `/32` or `/128` allowlist. Keycloak's
`hostname-admin` setting only generates URLs; the Nginx path/host policy is the
actual network enforcement boundary.

Local `AUTH_MODE=mock` must be refused when `APP_ENV=production`.

### Issuer visibility

The raw OIDC issuer URL, discovery URL, client IDs, claim mappings and client
secret status are security configuration, not user-profile data. Public
settings, iframe bootstrap responses, user APIs, HTML source and telemetry must
not expose them. User-facing pages display a configured provider label such as
`SoloV 统一登录`; they do not display the issuer URL.

Only an administrator with `security.settings.read` may view OIDC status, and
even that response returns a provider label, configured/not-configured flags,
the last successful discovery time and a configuration fingerprint. It does
not return the raw issuer URL or any client secret. Diagnostics that genuinely
need the issuer run on the server and write only a redacted hostname/hash to
the audit log.

The production API follows this contract. Local mock mode remains isolated and
does not accept production OIDC credentials.

### Platform-password login (CR-0004)

Ordinary users do not go through OIDC at all. The user-facing login page asks
for one account + password with no platform picker (XM-INV-AUTOLOGIN); the
backend auto-detects which platform the account belongs to instead: an
email-shaped identifier tries Sub2API first, then falls back to New API only
on a definitive invalid-credentials result (a New API username can itself
happen to look like an email address); a non-email identifier tries New API
only, since Sub2API accounts are always keyed by email. An upstream outage on
either platform never triggers a fallback attempt -- it fails closed as
"login service unavailable" immediately, so one platform's downtime can never
be misreported as a wrong password for a working one. The two upstream
attempts an auto-detected login can make still share a single rate-limit
bucket (keyed by client IP + identifier), not one bucket per platform tried.
The backend forwards whichever credentials it tries to the platform's real
login endpoint over HTTPS (`internal/auth/sub2api_login.go`,
`internal/auth/newapi_login.go`), never persists or logs the password, and
resolves the verified `(platform, platform_user_id)` pair to a local identity
exactly like an OIDC `(issuer, subject)` pair. An explicit `platform` field is
still accepted on both `platform-login` endpoints and skips auto-detection
entirely (kept for ops/test callers; the login page itself never sends one
anymore, and a 2FA follow-up call does not need to either -- the backend
remembers which platform issued the temp_token). Administrator login is
unaffected and still uses the OIDC block above. This mode requires no Keycloak
configuration to run.

```
SUB2API_LOGIN_BASE_URL=https://api.solov.cc   # exact HTTPS origin, host-pinned
NEWAPI_LOGIN_BASE_URL=https://xm.solov.cc     # exact HTTPS origin, host-pinned
PLATFORM_LOGIN_TIMEOUT=10s                    # 1s..1m
PLATFORM_LOGIN_MAX_ATTEMPTS=8                 # consecutive-failure lockout threshold, per IP+account
PLATFORM_LOGIN_LOCKOUT_WINDOW=15m             # 1m..24h
```

All five have production-matching defaults, so they only need to be set to
override them (a staging platform origin, a shorter lockout window, etc.).

**Operational dependency:** both upstream platforms must keep their Turnstile
challenge disabled (`GET /api/v1/settings/public` on Sub2API, `GET
/api/status` on New API) for this server-to-server forwarder to authenticate
at all — there is no browser to solve a challenge in. If either platform
enables Turnstile, every login attempt on that platform fails; on Sub2API it
is misreported as "wrong password" (a 400 from Sub2API's Turnstile check is
indistinguishable, at the HTTP layer, from a bad-credentials 400). This is not
monitored automatically yet — see the XM-INV-LOGIN handoff doc for the
suggested preflight/health-check follow-up.

Both platforms fold "wrong password" and "unknown account" into one
indistinguishable response by design (verified against
`K:/sub2api-src`/`K:/newapi-src` as of 2026-08-31); the invoice-system side
preserves that and never surfaces which case occurred, matching CR-0004's
"failure must not leak whether the account exists" requirement.

### Embedded platform scope (CR-0003)

A platform embeds the invoice center per-account via `?ui_mode=embedded`. As
of XM-INV-PLATFORM-SCOPE, a platform-password session's own login platform is
authoritative for scoping that embedded view: the backend already enforces
it server-side on every `/api/v1/user/*` endpoint (funding lots, source
accounts, eligibility summary, invoice requests, submit, cancel), so a
Sub2API session can never see or operate on New API data and vice versa,
regardless of the embed URL.

**Operators no longer need to append `&platform=sub2api`/`&platform=newapi`
to the embed link.** The URL param still exists as a fallback, but it now
only matters for a session with no platform of its own -- an OIDC
administrator embedding on a platform's behalf, or the brief window before
the session finishes loading. For a platform-password session the param is
redundant (the session already determines the scope) and is ignored in favor
of it.

## 3. SMTP

Local development uses Mailpit without credentials. Production requires TLS,
server certificate validation, a verified sender domain and a write-only
credential encrypted by the application's field keyring.

V1 production supports a dedicated QQ/enterprise QQ account
(`smtp.qq.com`/`smtp.exmail.qq.com`) or Gmail (`smtp.gmail.com`). QQ uses its
SMTP authorization code; Gmail requires 2-Step Verification plus a Google App
Password. Never use the normal mailbox password. The credential is write-only,
encrypted by the field keyring in the dedicated settings-secret row, and never
belongs in a plain environment value, normal settings row or source control.

Production SMTP is configured from the administrator console. The
authorization code/App Password is a write-only secret:

- it is accepted once over an MFA step-up protected request;
- the browser clears the field immediately after submission;
- API responses return only configured state and non-secret sender metadata;
- it is never returned, masked, partially revealed or copied into audit bodies,
  validation errors, traces, support bundles or database snapshots;
- submitting an empty secret keeps the current value; replacing it is an
  explicit rotate action;
- the secret is encrypted with the versioned field keyring and stored in the
  dedicated `admin_setting_secrets` row; the browser and read APIs never receive
  it back.

The allowlist is exactly `smtp.qq.com`, `smtp.exmail.qq.com`, and
`smtp.gmail.com`, always port 587 with required STARTTLS and certificate/hostname
validation. Arbitrary hosts, IP literals and port 465 are rejected. Implicit TLS
is a future option and remains disabled until a dedicated transport and test
exist. Plain SMTP and certificate-validation disablement are prohibited.

SPF, DKIM and DMARC must be configured before public delivery. Email is a
notification only: do not attach invoices or place a permanent bearer download
URL in the message. Delivery uses an idempotent outbox and a verified recipient.

The administrator `Send test email` action is rate-limited and MFA step-up
protected. Production must set `SMTP_TEST_RECIPIENT` to a dedicated, controlled
mailbox that differs from the configured sender (for example,
`invoice-test@example.com`). The actual deployment value belongs only in the
root-managed production environment. The API returns only a masked form to the administrator
UI and the request body must be exactly `{}`. It cannot accept an arbitrary
recipient, attachment or template body, so the service cannot become an SMTP
relay. Audit stores only the bounded operation outcome; raw SMTP dialogue,
recipient address and authorization material are never persisted in audit.
The administrator identity must still carry a verified email as an additional
account-integrity gate, even though that address is not used as the recipient.

Local mock mode may use Mailpit. Production has no `SMTP_PASSWORD` or
`SMTP_PASSWORD_FILE` precedence: introducing a second secret source would make
rotation ambiguous and is deliberately rejected by the deployment contract.
Rotate or clear the credential only through the typed MFA/IP-protected settings
command, then run the restricted test action.

Keep three mailbox roles separate where practical: the administrator identity,
the dedicated SMTP sender and the fixed independent test recipient. Sharing the
administrator and sender mailbox is supported for V1 but increases the impact
of a mailbox compromise and should be removed in a later hardening phase.

## 4. Administrator source-IP policy

`ADMIN_BOOTSTRAP_IP_ALLOWLIST` is a development/mock convenience used by the
current local milestone. It accepts CIDRs but is permitted only when
`APP_ENV=development`; it must be rejected or ignored in production. It is not
the production dynamic policy or break-glass source.

The application must derive client IP only through an explicitly configured
trusted-proxy chain. It must reject forwarding headers from an untrusted direct
peer. The public origin should be firewalled to the approved CDN/reverse proxy,
and the proxy must replace rather than append attacker-controlled client-IP
headers.

```text
ADMIN_BOOTSTRAP_IP_ALLOWLIST=127.0.0.1/32,::1/128
INVOICE_PROXY_GATEWAY_IP=10.20.0.1
TRUSTED_PROXY_CIDRS=10.20.0.1/32
TRUSTED_CLIENT_IP_HEADERS=CF-Connecting-IP
```

Production accepts exactly one trusted proxy host (`/32` or `/128`), never a
bridge subnet. `TRUSTED_PROXY_CIDRS` must equal the explicitly configured
`invoice_proxy` gateway address rendered by Compose. The production Compose
attachment also gives `invoice_proxy` the only positive `gw_priority`; this
keeps the published-port TCP peer and the trusted `/32` aligned even though the
API joins several isolated networks.

Fixed IP is an additional control, not authentication. Administrator routes
still require OIDC, MFA, RBAC, CSRF protection, secure cookies and audit logging.
If IP parsing or proxy configuration is unavailable, administrator access fails
closed.

### Deployment break-glass CIDRs

`ADMIN_BREAK_GLASS_CIDRS_FILE` is a deployment-controlled, narrow emergency
allowlist. It is loaded from a root/operator-managed secret file and cannot be
read, changed or removed through the administrator UI or settings API. It must
contain only known VPN/bastion egress addresses, normally IPv4 `/32` or IPv6
`/128`; world-open ranges are rejected.

`ADMIN_BREAK_GLASS_CIDRS_FILE` is the production break-glass configuration
name. Raw CIDRs from this file never enter the settings database, settings
snapshots or settings APIs.

Break-glass is not an authentication bypass. Requests from these CIDRs still
require OIDC, fresh administrator MFA, active role, CSRF protection and the
ordinary audit path. Its only purpose is to recover from an accidental dynamic
allowlist lockout. The file is loaded at production startup; changing it
requires a controlled container restart.

The effective network policy is:

```text
authenticated administrator
AND MFA/step-up satisfied
AND (active dynamic CIDR OR deployment break-glass CIDR)
```

### Dynamic CIDR update rules

The PostgreSQL-backed normal allowlist is edited through one typed administrator
route. The request requires fresh MFA/IP authorization, CSRF and the current
settings revision. The server:

1. normalizes/deduplicates at most 16 entries;
2. rejects unspecified, multicast, link-local and overly broad ranges (minimum
   IPv4 `/24`, IPv6 `/64`);
3. requires the new list to retain the current client IP unless the request is
   already using deployment break-glass;
4. commits the database CAS and only then atomically replaces the in-process
   policy.

V1 runs one API replica. There is no replica acknowledgement/cache protocol;
adding replicas requires a separately implemented propagation mechanism before
this settings route may remain enabled.

Deployment control:

```text
ADMIN_BREAK_GLASS_CIDRS_FILE=/run/secrets/admin_break_glass_cidrs
```

## 5. Source synchronization

Development:

```text
SOURCE_MODE=mock
MOCK_SOURCE_BASE_URL=http://mock-source:8080
SOURCE_INGESTION_ENABLED=false
```

Production projection agent:

```text
SOURCE_MODE=db_projection
SOURCE_TYPE=sub2api
SOURCE_ID=10000000-0000-4000-8000-000000000001
SOURCE_RUNTIME_VERSION=<exact deployed Sub2API version>
SOURCE_SCHEMA_VERSION=3.0
SOURCE_DB_DSN_FILE=/run/secrets/source_projection_dsn
SOURCE_DB_DIALECT=postgres
SOURCE_TRUSTED_OIDC_PROVIDER_KEY=central-oidc
SOURCE_TRUSTED_OIDC_ISSUER=https://id.example.invalid/realms/central
SOURCE_STATE_FILE=/var/lib/invoice-source-agent/sub2api-payments.json
SOURCE_STATE_STREAM=payments
SOURCE_SPOOL_FILE=/var/lib/invoice-source-agent/sub2api-payments.pending.enc
SOURCE_SPOOL_KEY_FILE=/run/secrets/source_spool_key
SOURCE_POLL_INTERVAL=1m
SOURCE_ECONOMIC_SAFETY_DELAY=5m
SOURCE_RECONCILE_INTERVAL=6h
NEWAPI_FULL_SCAN_INTERVAL=1h
SOURCE_MAX_BACKOFF=1m
SOURCE_MAX_CONSECUTIVE_FAILURES=10
SOURCE_LOCAL_HEALTH_MAX_AGE=5m
SOURCE_MAX_PAGES_PER_CYCLE=1000
SOURCE_SCAN_LIMIT=100
INGESTION_ORIGIN=https://invoice.example.invalid
INGESTION_ALLOWED_HOSTS=invoice.example.invalid
INGESTION_ALLOWED_PORTS=443
INGESTION_ALLOWED_CIDRS=203.0.113.10/32
INGESTION_ALLOWED_METHODS=POST
INGESTION_HTTP_TIMEOUT=20s
SOURCE_MTLS_CERT_FILE=/run/secrets/source_mtls_cert
SOURCE_MTLS_KEY_FILE=/run/secrets/source_mtls_key
SOURCE_MTLS_CA_FILE=/run/secrets/ingestion_ca
SOURCE_MTLS_SERVER_NAME=invoice.example.invalid
SOURCE_MTLS_RELOAD_ON_HANDSHAKE=true
SOURCE_SIGNING_KEY_FILE=/run/secrets/source_signing_key
SOURCE_SIGNING_KEY_ID=source-signing-2026-01
ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00
SOURCE_CUTOVER_MANIFEST_FILE=/cutover/manifest.enc
SOURCE_CUTOVER_KEY_FILE=/run/secrets/sub2api_cutover_key
# balances stream only:
SOURCE_BALANCE_BASELINE_FILE=/cutover/baseline.enc
SOURCE_BALANCE_SNAPSHOT_FILE=/state/balance-current.enc
SOURCE_BALANCE_SNAPSHOT_KEY_FILE=/run/secrets/sub2api_balance_snapshot_key
```

`SOURCE_SCHEMA_VERSION=2.0` is used only by `identities`; only those streams set
`SOURCE_RECONCILE_FILE` and initialize deletion reconciliation. Production V3
uses exactly `payments|usage|credits|balances`. Set host
`SOURCE_STATE_ROOT` to the parent of ten independent state directories and set
`SOURCE_CUTOVER_ROOT=$SOURCE_STATE_ROOT/cutover`; the latter contains
`sub2api/{manifest.enc,baseline.enc}` and
`newapi/{manifest.enc,baseline.enc}`.

`SOURCE_SPOOL_KEY_FILE` contains only standard base64 for exactly 32 random
bytes and is mounted mode 0600. For example, generate it once with
`openssl rand -base64 32`; do not rotate/remove it while a pending spool exists.
The encrypted spool contains the byte-exact v2/v3 body and its bound before/after
cursor, allowing safe recovery when the receiver committed but its ACK was
lost.

`SOURCE_ID` is the pre-registered UUID primary key from the invoice system's
`source_instances` table, not an upstream display name. The mTLS certificate
mapping, signed header, v2 body and receiver row must all resolve to this exact
UUID.

The DSN, spool key, Ed25519 private key and mTLS private key must be regular,
non-symlink files readable by container UID 65532 and no broader than 0600 on
Linux. The state directory must also be owned/writable by UID 65532; do not make
the read-only root filesystem writable to accommodate it. Its Linux mode must
be 0700; broader group/other traversal is rejected at startup.

Generate each stream's Ed25519 pair with the disposable Dockerfile `keygen`
target described in `SOURCE-AGENT-RUNBOOK.md`; delete that tools image after
all ten pairs are stored. The production/default image contains no keygen.

```text
docker build --target keygen -f agents/Dockerfile.production \
  -t invoice-source-keygen:one-time agents
```

The command refuses relative/aliased/existing outputs, writes PKCS8 private PEM
mode 0600 and raw 32-byte Ed25519 public key as standard base64 mode 0644. Its
stdout prints only key ID and paths, never either key. Mount the private PEM at
`SOURCE_SIGNING_KEY_FILE`. Configure the receiver trust entry with the exact
`(SOURCE_ID, SOURCE_STATE_STREAM, SOURCE_SIGNING_KEY_ID)` tuple and the public
base64 file; a public key registered for one stream must not authorize another.
For rotation, register the new public tuple first, then rotate the sender; keep
the previous receiver key through the maximum clock/retry window.

`INGESTION_ORIGIN` is an origin only. The agent appends the fixed
`/internal/v1/source-batches` path. The host, port and every resolved address
must be independently allowlisted. World-open CIDRs, redirects, URL userinfo,
paths, query strings, fragments and disabled TLS verification are rejected.

New API uses `SOURCE_TYPE=newapi` and no projection currency. Its top-ups are
always `pending_manual` candidates; its `identities` stream additionally
requires exact slug `solov-sso` and the HTTPS issuer. The identities reader has
only `invoice_bridge.newapi_identities_v4(text,jsonb)` EXECUTE; every scan requires
`contract_ok=true` and validates well-known, authorization, token and user-info
endpoints against that issuer.
Sub2API payments always call the fixed, reviewed
`invoice_bridge.sub2api_payments_v4(text,jsonb)` function. It extracts each order's
currency from `provider_snapshot.currency` without exposing the JSON object;
only v0.1.179's audited fixed-CNY providers (`easypay`, `alipay`, `wxpay`) may
derive CNY when snapshot currency is absent. Configurable/unknown unresolved
rows are excluded and counted by its aggregate-only `legacy_health` operation;
valid non-CNY is separately
counted as unsupported and does not block reconciliation, while unknown or
contradictory evidence does. Fixed deployment currency and direct
`payment_orders` reads are intentionally unsupported.

The invoice API independently requires fresh accepted `payments`, `usage`,
`credits`, `balances`, and `identities` batches. Production uses independent
freshness budgets: `SOURCE_ECONOMIC_HEARTBEAT_MAX_STALENESS=5m` for the latest
accepted non-identity batch, `SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS=15m` for
the proven economic scan horizon, and `SOURCE_IDENTITIES_MAX_STALENESS=15m` for
identity heartbeats. The source horizon uses
`SOURCE_ECONOMIC_SAFETY_DELAY=5m` and `SOURCE_POLL_INTERVAL=1m`. The API refuses
an economic watermark budget smaller than the safety delay, the receiver's
five-minute maximum clock skew, and two poll intervals (12 minutes under the
reviewed defaults), so a nominally healthy quiet stream cannot be configured
to fail readiness by construction. All durations remain bounded between their
documented launcher limits.
`projection_status=blocked`, an unapproved runtime version, queued/dead source
events or a missed heartbeat fails readiness, user submission and final manual
issue confirmation. Recoverable OIDC/account dependency waits remain visible
to administrators but do not block unrelated users or global readiness.

The library also contains a separately tested Admin API fallback connector. It
requires an Admin Key secret file and an immutable exact origin, and it has only
compiled GET paths. `cmd/source-agent-prod` deliberately rejects
`SOURCE_MODE=admin_api`; enabling a future fallback launcher requires a separate
security/deployment approval and must never silently replace DB projection.

```text
SOURCE_MODE=admin_api
SOURCE_UPSTREAM_ORIGIN=https://sub2api.internal.example
SOURCE_UPSTREAM_ALLOWED_HOSTS=sub2api.internal.example
SOURCE_UPSTREAM_ALLOWED_PORTS=443
SOURCE_UPSTREAM_ALLOWED_CIDRS=10.20.30.40/32
SOURCE_UPSTREAM_ALLOWED_METHODS=GET
SUB2API_ADMIN_KEY_FILE=/run/secrets/sub2api_admin_key
SOURCE_UPSTREAM_MTLS_CERT_FILE=/run/secrets/upstream_client_cert
SOURCE_UPSTREAM_MTLS_KEY_FILE=/run/secrets/upstream_client_key
SOURCE_UPSTREAM_MTLS_CA_FILE=/run/secrets/upstream_ca
SOURCE_UPSTREAM_MTLS_RELOAD_ON_HANDSHAKE=true
```

For New API API fallback, the only credential is a dedicated administrator PAT
loaded from `NEWAPI_ADMIN_PAT_FILE` and sent as a bearer credential. A browser
session token is not an operational credential. API credentials are retried at
most once after 401/403 using a fresh snapshot so rotation does not require
echoing or logging either key.

The production entrypoint is `cmd/source-agent-prod`; `cmd/source-agent` remains
an offline v1 mock verifier. Build the minimal non-root image with:

```bash
docker build -f agents/Dockerfile.production \
  --build-arg SOURCE_AGENT_VERSION=0.3.0 \
  -t "invoice-source-agent:$INVOICE_IMAGE_TAG" agents
```

The Dockerfile exposes `GO_IMAGE` and `ALPINE_IMAGE` build arguments. Release
CI must override both tags with approved multi-architecture `@sha256:` digests
and record the resulting agent-image digest; a mutable registry tag alone is
not a production pin.

Initialize each stream's dedicated volume once, then run it normally:

```bash
docker run --rm ... "invoice-source-agent:$INVOICE_IMAGE_TAG" init-state
docker run --rm ... "invoice-source-agent:$INVOICE_IMAGE_TAG" run
```

Initialization refuses overwrite; normal startup only loads the existing
source+stream-bound state and fails closed when the volume/file is missing. The
state and encrypted pending spool use cross-process lock, revision/old-value
CAS, fsync, atomic replace and 0600 permissions on Linux. A Windows deployment
additionally requires an operator-proved restrictive volume ACL because Go file
modes do not expose its DACL. The agent must never receive an invoice PostgreSQL
DSN for sender state. Direct `SOURCE_DB_DSN`, signing-key, mTLS-private-key and
spool-key environment values are rejected; secrets are file mounts only.
Ambient `PG*` connection variables are also rejected so pgx cannot silently
supplement or redirect the DSN-file contract through process environment or
`.pgpass` configuration. Put the complete approved source projection connection
contract in `SOURCE_DB_DSN_FILE`.

Deploy ten independent containers when both sources are enabled: each source
has `payments`, `identities`, `usage`, `credits`, and `balances` streams. Each
has a distinct state file, spool file, spool key, mTLS identity
and signing key; do not share a writable state volume. New API forces a full
scan on `NEWAPI_FULL_SCAN_INTERVAL` (which cannot exceed 24 hours). Sub2API
uses incrementals between `SOURCE_RECONCILE_INTERVAL` scans. In-memory stores
are test-only, and the production launcher never falls back to mock data or
Admin API mode.

**Restart does not force an extra cycle (XM-INV-AGENT-RESTART-GRACE, agent
0.3.1+).** The reconcile/full-scan schedule is persisted in the same state
file as the cursor, so a container recreation (`deploy/roll-forward.sh`)
resumes the schedule instead of starting a full ScanReconcile/ScanFull the
moment the process comes back up. A state file written by an older agent
binary has no schedule recorded and forces one cycle on the first restart
after upgrade -- expected and one-time, not a bug. Independently, a periodic
Sub2API usage `ScanReconcile` cycle itself no longer rewinds all the way back
to the cutover manifest: it resumes from where the previous completed
reconcile left off, so `SOURCE_RECONCILE_INTERVAL` reconciles verify only the
data ingested since the last reconcile, not the entire history since cutover.
The very first reconcile ever (or a legacy in-flight cycle abandoned on
upgrade, logged as `legacy_reconcile_cycle_abandoned` in the OnCycle warnings)
still starts from the live watermark, which for a fresh cutover is the
cutover manifest itself. The credits stream and New API's mandatory full scans
are unaffected -- see agents/sourceagent/economics_db.go's `prepareCursor` doc
comments for exactly which branch each stream/mode takes.

During a long Sub2API reconcile (restart-forced on first upgrade, or simply a
large backlog), the economic watermark for that stream stays where it was
until the cycle completes, which would otherwise report `ECONOMIC_WATERMARK_STALE`
and fail `/readyz` and the funding-lot freshness gate for the whole duration.
As long as the agent's `source_economic_scan_cycles` row for that stream keeps
its `updated_at` moving -- proving the rescan is still making progress, not
stalled -- the API downgrades that finding to the non-fatal
`ECONOMIC_RESCAN_ACTIVE` reason instead (visible in the admin source-health
report) and keeps the stream ready. See docs/PRODUCTION-RUNBOOK.md section 9's
readiness note for the exact activity-window budget.

`SOURCE_SCAN_LIMIT` may be at most 166 for the Sub2API payments stream because
one source order can emit the base order plus one cumulative gateway-refund
record. The conservative 166-row ceiling keeps a batch far below 500 even if a
future reviewed contract adds another record. Other streams may use up to 500,
but 100 is the conservative default.

Before publishing the agent image, run from `agents/` with Go 1.25.13:

```text
go test -race ./...
go vet ./...
govulncheck ./...
```

The checked module floor is `github.com/jackc/pgx/v5 v5.9.2`; indirect
`golang.org/x/text` must remain at a version without GO-2026-5970 (currently
v0.39.0 or later). Build with `-mod=readonly` so an image build cannot silently
rewrite the reviewed dependency graph.

The invoice web/API process does not receive these variables or mount these
secret files.

## 6. Iframe and cookies

```text
EMBED_ALLOWED_PARENT_ORIGINS=https://api.example.invalid
SESSION_COOKIE_NAME=__Host-invoice_session
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_HTTP_ONLY=true
SESSION_COOKIE_SAME_SITE=Lax
BOOTSTRAP_QUERY_LOGGING=false
```

Allowed parent origins are exact origins, not wildcard domains. The bootstrap
route must redirect to a clean URL and must not initialize analytics/error
tracking while the upstream token is in the request.

## 7. File storage and scanning

```text
DOCUMENT_MAX_BYTES=20971520
DOCUMENT_ALLOWED_TYPES=application/pdf
DOCUMENT_STORAGE_PUBLIC=false
MALWARE_SCAN_REQUIRED=true
DOCUMENT_ROOT=/data/documents
DOCUMENT_QUARANTINE_ROOT=/quarantine
CLAMAV_ADDRESS=clamav:3310
CLAMAV_DATABASE_ROOT=/clamav-db
CLAMAV_MAX_SIGNATURE_AGE=48h
PDF_SCANNER_SOCKET=/scanner/qpdf.sock
PDF_SCANNER_CAPABILITY_FILE=/run/secrets/invoice_pdf_scanner_capability
```

Production startup and every individual upload fail if clamd is unavailable,
the read-only signature volume lacks `main.*` or `daily.*`, or the actual
`daily.*` database file is older than `CLAMAV_MAX_SIGNATURE_AGE`. The
deployment healthcheck deliberately ignores `freshclam.dat`: it is updater and
rate-limit state whose mtime may remain unchanged after a successful no-op
check, so it cannot prove signature freshness. Production accepts only a
positive integer duration with one `h`, `m` or `s` suffix (for example `48h`).
The ClamAV container has a dedicated
egress-only network for FreshClam and no published port. After ClamAV, the
pinned qpdf 12.3.2 parser runs in the separate `scanner` image over an
authenticated Unix socket. The scanner container has `network_mode: none`, no
database/document mounts, only the single-purpose read-only capability, a
socket volume and bounded tmpfs, plus fixed memory/CPU/PID/concurrency/time
limits. It performs strict validation with bounded pages, JSON bytes/nodes and
depth. V1 rejects encrypted PDFs, JavaScript, Launch/open
actions, remote URI/GoTo behavior, form submission/AcroForm/XFA, embedded files,
portfolios and rich media, as well as OFD, images, archives and all non-PDF
formats. Only then is plaintext encrypted into the private document volume.

## 8. Mandatory production assertions

At startup, production must reject:

- `SOURCE_MODE=mock` or `AUTH_MODE=mock`;
- secrets supplied as plain environment values where a `_FILE` option exists;
- missing mTLS/signing material in source-agent mode;
- wildcard OIDC redirect/issuer/origin values;
- disabled TLS verification;
- `ADMIN_BOOTSTRAP_IP_ALLOWLIST` as a production authorization source;
- an empty or world-open administrator IP allowlist;
- a missing/invalid `ELIGIBILITY_START_AT`, any value other than
  `2026-09-01T00:00:00+08:00` (the same instant as
  `2026-08-31T16:00:00Z`), or any mismatch between that deployment value and
  the immutable database policy row;
- public document storage, stale ClamAV signatures, disabled malware scanning,
  an unavailable/wrong-version isolated qpdf scanner, unsafe/missing scanner
  capability, or a non-tmpfs quarantine;
- an upstream Admin API URL exposed to a browser or invoice web process.

## 9. Administrator self-service settings model

Administrator settings are split by sensitivity. A single generic key/value
settings endpoint is prohibited.

| Class | Examples | UI behavior | Storage/change rule |
| --- | --- | --- | --- |
| Business | minimum invoice amount, fixed service item, request instructions | minimum editable; `技术服务` read-only | versioned DB value, optimistic update, database CHECK, audit before/after hash |
| Delivery | sender display name, verified sender address, notification toggle | readable; test action available | DB metadata; sender changes require verification |
| Write-only secret | QQ authorization code or Google App Password | empty input keeps current value; never echo | field-keyring ciphertext in dedicated secret row; MFA/IP rotate action |
| Security policy | dynamic administrator CIDRs | dedicated security page; new list retains current IP | MFA, optimistic revision, atomic single-replica refresh |
| Immutable financial policy | eligibility start, policy version, payment-and-usage rule | read-only in user/admin UI | fixed by migration; UPDATE/DELETE/TRUNCATE rejected; a change requires a new audited migration and ledger review |
| Deployment locked | break-glass CIDRs, trusted proxies, field keys, cookie security, mTLS files, production mode | status only or completely hidden | operator-managed file/environment; no settings API write path |
| Issuer snapshot | administrator-only issuing entity name and fixed invoice item | update current settings | settings revision plus immutable encrypted snapshot when issuance is confirmed |

Every settings mutation uses an explicit typed command, CSRF protection and an
optimistic revision under fresh administrator MFA/IP authorization.
Secret-bearing request bodies are omitted from audit capture. Production uses
the PostgreSQL repository; the memory repository is compiled only into local
mock mode.

The production API exposes these typed settings routes:

```text
GET  /api/v1/user/invoice-policy
GET  /api/v1/admin/settings
PUT  /api/v1/admin/settings/invoice
PUT  /api/v1/admin/settings/smtp
PUT  /api/v1/admin/settings/admin-access
POST /api/v1/admin/settings/smtp/test
```

The user policy response contains `minimum_request_minor`, the fixed
`service_item`, `eligibility_start_at`, `eligibility_policy_version`,
`eligibility_timezone=Asia/Shanghai`, and
`eligibility_rule=payment_and_usage_at_or_after`. These are read-only public
business rules, not a settings write surface. It never contains issuer names,
issuer revision identifiers, SMTP metadata, CIDRs or secrets.

V1 eligibility is inclusive at the exact boundary: both the real payment
completion time and the authoritative wallet usage occurrence time must be
greater than or equal to `2026-09-01 00:00:00 Asia/Shanghai`. Pre-policy cash
remains in a noninvoiceable pool and is consumed before post-policy cash.
Subscription purchases remain auditable but noninvoiceable because the current
signed source contract cannot prove an unambiguous subscription-purchase to
actual-usage link.

## 10. Issuing entity and historical snapshots

The current issuing-entity display name is part of the versioned administrator
settings row. The item is fixed to `技术服务`; V1 does not expose a generic tax
policy or arbitrary issuer-record editor.

At submission, the request stores immutable user invoice-profile and allocation
snapshots. At manual-issue confirmation, it additionally stores the active
settings revision and an encrypted normalized issuer snapshot. Later settings changes must not
rewrite previous requests, audits, PDFs, email records or red-letter cases.

User APIs expose no issuer field at all. Authorized administrator history uses
the request's settings revision; raw OIDC issuer configuration is never copied
into invoice history.

Snapshot corrections use a new request/document revision and an explicit
void/red-letter workflow; direct updates to an issued snapshot are prohibited.
The PostgreSQL immutability trigger prevents direct changes to the issued
request/profile/allocation/issuer snapshot. Memory-backed local settings remain
development-only and are not tax/audit evidence.
