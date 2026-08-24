# Invoice System security architecture

Status: V1 baseline
Boundary: independent integration; no Sub2API or New API source modification

## Implementation and deployment status

The repository contains two deliberately separate runtimes: a mock-only local
development stack and a fail-closed production stack. The production path now
implements OIDC code+PKCE, persistent sessions, administrator LoA2/MFA and IP
policy, durable PostgreSQL settings/audit/history, file-backed versioned field
encryption, signed mTLS source agents, encrypted PDF storage and verified-admin
SMTP testing. It refuses mock auth, stale sources/signatures, unknown database
migrations and missing production secrets.

This is implementation evidence, not a claim that the live service is already
launched. DNS/TLS, IdP/upstream configuration, source DB grants, real secrets,
image scans, encrypted restore drill and canary still require an approved
production change. V1 deploys one API replica; dynamic CIDR propagation across
multiple replicas is intentionally outside this release.

## 1. Security decision

The production data path should be implemented in this order of preference:

1. Source-local read-only database projection plus an outbound-only source
   agent that pushes signed batches over mTLS.
2. Source-local outbound-only source agent holding an upstream administrator
   credential and calling a fixed set of read-only API operations.
3. A dedicated administrator-key sidecar on the source host, exposed only to
   the invoice backend through a private authenticated channel.

The invoice web/API process must never hold an upstream administrator key or an
upstream database credential. It must never be a generic HTTP proxy.

Production use of direct database access from the invoice host, direct public
Admin API access, or browser-side upstream access is prohibited.

## 2. Trust zones

```text
Browser / desktop client
        |
        | OIDC session, CSRF protection
        v
Invoice web/API  ----> private invoice PostgreSQL
        |
        | typed internal operations only
        v
Invoice ingestion boundary
        ^
        | mTLS + payload signature + anti-replay
        |
Source-local agent (no inbound listener)
        |
        +----> read-only projection (preferred)
        |
        +----> fixed upstream GET operations (fallback)
```

Every source instance has a distinct identity and certificate; each
`payments|identities|usage|credits|balances` stream has its own signing key,
cursor, sequence, state file and
encrypted pending spool, database login and column grant. A source certificate
can only write events for the
source ID encoded in its certificate SAN. Source and stream fields in a request
body are never authoritative: the receiver matches certificate, signed
`X-Source-ID`, signed `X-Stream-ID` and body fields exactly. Sender
cursor/sequence state lives only on a dedicated source-agent volume. The agent
never receives an invoice PostgreSQL DSN; its only invoice-bound data path is
outbound mTLS HTTPS to the fixed ingestion operation. Missing local state or an
undecryptable/tampered pending spool fails closed instead of restarting sequence
zero or rebuilding a conflicting batch.

The four V3 economic streams bind to one source-database-clocked encrypted
cutover manifest and a separately encrypted balance baseline. These files are
read-only after a create-once `REPEATABLE READ READ ONLY` capture; changing the
runtime/configuration/unit or losing either file blocks all new entitlement.
Only `identities` keeps deletion-reconcile state. V3 economic facts are
immutable invoice-side evidence and never accept a source tombstone.

The API accepts mTLS assertion headers only from the ingest proxy's dedicated
container `/32`, never from the whole shared ingestion bridge. The proxy owns a
fixed address and Docker DNS alias; source agents also restrict that alias to
the same `/32`. Direct source-agent-to-API requests therefore cannot forge the
proxy's certificate-verification headers.

Source agents also do not join either upstream application's normal Docker
network. Each upstream database is attached to a dedicated internal projection
bridge with a database-only alias; the two matching agents join that bridge and
the internal ingestion bridge only. Their production containers therefore have
no route to the upstream web/API container or the public internet.

## 3. Upstream integrity boundary

- Do not patch, vendor, mount, generate into, or commit to either upstream
  source tree.
- Do not run invoice migrations in either upstream database.
- Database projections and roles are separately approved deployment objects;
  they are not installed by the default development Compose stack.
- Upstream custom menus, OIDC, Nginx, DNS and database grants require a separate
  production change approval.
- Development defaults to `SOURCE_MODE=mock` and has no production endpoint or
  production credential.

## 4. Credential containment

### Source Bridge V4 mode

The LOGIN source agent receives no base-table privilege. It has only
`invoice_bridge` USAGE and EXECUTE on its exact fixed dynamic SECURITY DEFINER
function. A NOLOGIN/NOINHERIT owner receives exact reviewed source-column
SELECT. Function bodies use bound JSONB values, `search_path=pg_catalog` and
zero persistent relation `pg_depend`. Sub2API payments use the payments bridge
because the base table has no currency column; the function extracts only
`provider_snapshot.currency`, with a narrowly audited fixed-CNY fallback only
for EasyPay/Alipay/WeChat Pay. Unresolved rows are excluded and reported only as
aggregate counts. Valid non-CNY rows are an unsupported-but-known bucket and do
not block reconciliation; only missing, contradictory or invalid currency
evidence pauses missing/tombstone processing. Each caller
must be `NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`,
`NOREPLICATION`, `NOBYPASSRLS`, read-only by default, limited to two
connections, and constrained by short statement and idle-transaction timeouts.
There is no direct-column fallback. The production gate verifies exact function
body hashes, owner effective columns, no caller raw access/ownership/schema
CREATE, RLS disabled and zero upstream dependencies; any mismatch fails closed.
Payments and identities readers are separate; their DSNs are never shared.

### Admin API fallback

Sub2API's Admin API Key is global and unscoped. It has no native expiry, source
IP restriction, or invoice-only permission. It maps to an administrator and
can reach mutation routes. Therefore it may only exist in the source agent or
an isolated sidecar and must be loaded from a secret file, not an image,
environment dump, log or browser response.

The fallback client has immutable scheme, host and port; permits GET only;
does not follow redirects; validates numeric identifiers; bounds response
sizes; and implements named operations rather than accepting paths or URLs.

Allowed Sub2API operations are limited to:

```text
GET /api/v1/admin/payment/orders
GET /api/v1/admin/payment/orders/{numeric_order_id}
```

The following are explicitly denied:

```text
/api/v1/admin/usage
/api/v1/admin/users/{id}/api-keys
/api/v1/admin/settings
/api/v1/admin/system
all POST, PUT, PATCH and DELETE requests
all redirects and caller-supplied upstream URLs
```

`/api/v1/admin/usage` is not used because the current response surface can
hydrate and return user API-key data. Consumption is not an invoice entitlement
source.

New API fallback is limited to `GET /api/user/topup` with a dedicated
administrator PAT. Its `success` rows remain `payment_candidate/pending_manual`;
the connector has no operation that can turn them into verified entitlement.
The source DTO lacks currency, refund/chargeback evidence, `updated_at`, and a
way to distinguish provider settlement from administrator completion.

Central identity projection never reads `users` or email. Sub2API calls only a
row-filtered identities bridge pinned to the exact central issuer; its LOGIN
role has no access to raw `auth_identities`, so email/social provider subjects
never cross the database boundary. New API calls its identities bridge pinned
to `solov-sso`. All four
non-secret endpoints (well-known, authorization, token and user-info) must match
the configured exact HTTPS issuer. Raw OAuth tables, client credentials,
scopes, field mappings and access policies are outside the grant. Only the configured exact HTTPS central issuer
can produce a binding; without a user-table read its `user_status` is `unknown`.

## 5. Authorization and IDOR controls

- External user IDs are loaded from the server-side account binding. A browser
  or desktop client cannot select an upstream user ID.
- Every order detail response must satisfy
  `response.user_id == expected_external_user_id` before it is stored or
  returned.
- Invoice allocations use `(source_instance_id, external_order_id)` as their
  immutable external key.
- Bindings are unique on both `(source_instance_id, external_user_id)` and the
  central `(issuer, subject)` identity.
- Email equality never establishes an account binding.
- Manual binding overrides require elevated authorization, a reason and an
  immutable audit event.

## 6. Financial fail-closed rules

Amounts are decoded as decimal strings and converted to integer minor units.
Floating-point arithmetic is not used in the invoice ledger.

Sub2API eligibility requires:

```text
status = COMPLETED
completed_at is present
pay_amount > 0
refund_amount = 0
currency = CNY
order user matches the bound user
local available amount is sufficient
```

Any 401, 403, 423, 404, timeout, schema error, unknown status, missing field,
currency mismatch or non-zero refund amount prevents new reservation and
issuance. Previously captured evidence is quarantined rather than deleted.

Invoice entitlement is actual gateway money, not credited balance or plan
units. Sub2API refund evidence is converted with the exact cumulative formula
`round_CNY(pay_amount * refund_amount / amount)`; full refund equals
`pay_amount`. `amount-pay_amount` is never treated as a discount/adjustment.

New API `money` is a signed but unverified candidate ceiling. The database
rejects any reviewed amount above it. Verification is a two-person state
machine: one administrator proposes an amount plus evidence hash, another
distinct administrator approves the identical tuple, and both are barred from
beginning or confirming manual issuance funded by that candidate. Reject or
freeze invalidates the active proposal rather than leaving stale evidence
approvable.

New API refunds/chargebacks are currently manual upstream. The administration
API therefore accepts only a lower remaining cap plus bounded encrypted
evidence and reason. It reuses the atomic refund workflow (reservation release,
issued red-letter case), lowers a database-enforced future review ceiling, and
deletes the active approval. Repeated unsigned source candidates cannot raise
that ceiling; restoring a positive net amount requires two new distinct
reviewers, and those reviewers remain barred from issuance.

The source read and invoice database transaction cannot be atomic. Orders are
therefore re-read at submission, immediately before issuance, and continuously
after issuance. A post-issuance refund creates a red-letter case; it never
silently reopens invoice capacity.

## 7. Invoice workflow state machine

```text
DRAFT
  -> SUBMITTED
  -> UNDER_REVIEW
  -> APPROVED_PENDING_ISSUE
  -> DOCUMENT_QUARANTINED
  -> ISSUED

SUBMITTED / UNDER_REVIEW -> NEEDS_CHANGES -> SUBMITTED
SUBMITTED / UNDER_REVIEW -> REJECTED | USER_CANCELLED
ISSUED -> REFUND_ATTENTION -> RED_LETTER_REQUIRED -> RED_LETTER_COMPLETED
```

- `DRAFT` does not reserve money.
- `SUBMITTED` reserves money in one invoice-database transaction.
- Reject/cancel releases the reservation exactly once.
- An administrator cannot increase the amount; amount changes return the
  request to the user.
- State transitions use optimistic version checks and server-side role checks.
- Email delivery state is independent of the legal invoice state.
- Every transition records actor, reason, before/after state and correlation ID.

## 8. Embedded user and administrator surfaces

The Sub2API iframe entry is treated as an untrusted credential-bearing entry:

- the bootstrap endpoint loads no analytics or third-party scripts;
- query strings are excluded from edge, proxy and application logs;
- the upstream token is used only in request memory and immediately exchanged
  for an invoice session;
- the endpoint returns a 303 redirect to a clean URL;
- responses set `Referrer-Policy: no-referrer`;
- invoice cookies use `__Host-`, `Secure`, `HttpOnly`, `Path=/` and an explicit
  SameSite policy;
- state changes require a CSRF token and an exact allowed `Origin`;
- `postMessage` accepts only an exact configured parent origin and a versioned
  message schema;
- a session for account A cannot be silently replaced by a token for account B.

The invoice service must return a narrow `frame-ancestors` CSP containing only
the approved Sub2API/New API origins. Menu visibility is not authorization.
Administrator pages require invoice-side OIDC, MFA, RBAC and the trusted-source
IP policy. Sensitive administration should open as a top-level page rather than
inside an iframe.

Keycloak administration is separately isolated. `auth.solov.cc` exposes only
the public realm/resource/discovery paths and returns 404 for `/admin`; the
`master` realm is reachable only from the exact administrator/break-glass
allowlist. `auth-admin.solov.cc` proxies a distinct loopback host port and
applies that same allowlist before any Keycloak route. Both proxies overwrite
forwarding headers, and Keycloak accepts them only from the single canonical
Docker bridge gateway `/32`. The administration hostname is never an OIDC
issuer or application redirect.

OIDC discovery, JWKS and token traffic uses no environment proxy, follows no
redirect, enforces exact HTTPS endpoint hosts and pins each connection to one
validated DNS answer. Public providers may resolve only to publicly routable
addresses. A private provider requires individually approved canonical
RFC1918/ULA addresses; loopback, link-local, metadata, shared, benchmark and
documentation ranges remain forbidden. The separate tools-only provider
preflight uses the same transport and metadata validator as the API runtime.

The raw OIDC issuer/configuration is not part of the browser contract. Public
and user-facing APIs expose only a provider display label. Security diagnostics
return configured state and fingerprints, not issuer URLs or secrets.

## 9. Document and email controls

Uploaded PDF documents enter quarantine first. Extension and Content-Type are
insufficient: validate PDF magic bytes, enforce a strict size limit, scan for
malware, and reject encrypted files, embedded attachments, JavaScript, launch
actions, and external automatic loading. V1 rejects OFD, images, archives, and
all non-PDF formats. ClamAV signatures are mounted read-only into the API;
FreshClam/daily staleness fails both readiness and the upload operation itself.
The pinned qpdf parser runs in a separate low-UID container with no network,
database, document-ciphertext volume or application secrets. The API sends a
bounded plaintext stream over a Unix socket authenticated by a deployment
capability mounted read-only into both containers; that capability never lives
in the qpdf-writable socket volume. The scanner uses a private bounded tmpfs,
limits concurrent qpdf processes, time, pages, output bytes, object count and
depth, and returns only fixed result codes before encrypted promotion. Scanner
unavailability fails both readiness and the upload itself.
The public Nginx disables request-body buffering only on the exact document
upload route, so plaintext streams directly into the API's size-bounded tmpfs
quarantine instead of its host-disk client-body temp directory. Files are never
extracted into a web root. The exact authenticated download route also disables
proxy response buffering, so decrypted bytes cannot spill into an edge temp
file.

Documents use random object keys in private storage, server-side encryption,
SHA-256 integrity metadata and immutable versions. Download requires current
authorization on the ordinary user API; no object key or bearer download token
is exposed. Responses force attachment and set `nosniff` plus a restrictive CSP.

Email sends a notification with `/records?request_id=...`, not a bearer link or
document attachment; authentication is still required before download.
The recipient must be verified. Templates are fixed, user input is escaped, and
an idempotent outbox handles retries. Logs contain only a masked recipient,
template ID, message ID and delivery status.

SMTP authorization material is write-only. It is accepted only through a
step-up protected rotate command, AES-GCM encrypted with the field keyring in a
dedicated secret row, and never returned or included in audit bodies. Test email
is restricted to the current administrator's verified address and cannot carry
an attachment or arbitrary content.

The production mailer supports only STARTTLS with hostname/certificate
validation and an exact host allowlist: `smtp.qq.com`, `smtp.exmail.qq.com`, or
`smtp.gmail.com`, all on port 587. QQ uses an authorization code; Gmail uses a
Google App Password. Arbitrary hosts/IPs and implicit TLS port 465 are rejected.

## 10. Administrator configuration boundary

The administrator console may self-manage the issuer display name, minimum
amount, fixed-item policy, SMTP metadata/write-only credential and a dynamic
CIDR allowlist. It cannot manage trusted proxy chains, field-encryption roots,
source credentials, mTLS files, secure-cookie requirements, production mode or
deployment break-glass CIDRs.

Every settings mutation requires OIDC administrator role, fresh MFA step-up,
source-IP policy, CSRF and an optimistic revision. SMTP secrets are accepted by
a dedicated write-only command and stored only as field-keyring ciphertext.
CIDR changes must retain the caller's current IP unless break-glass is in use;
the in-process policy changes only after the database CAS succeeds.

Deployment break-glass CIDRs are narrow operator-managed VPN/bastion addresses.
They are additive only for lockout recovery and still require OIDC, MFA, RBAC
and the ordinary audit path. They are not returned by settings APIs.

The production source name is `ADMIN_BREAK_GLASS_CIDRS_FILE`. Its raw contents
never enter the settings store or any settings API. The separate
`ADMIN_BOOTSTRAP_IP_ALLOWLIST` is accepted only by local mock mode. Production
loads and enforces break-glass CIDRs at startup. Normal administrator CIDRs are
stored with optimistic revision and refreshed atomically in the single API
replica; scaling to multiple replicas requires a separate propagation design.

The legal invoice issuer is represented by immutable revisions. Submission
captures the user's profile and selected amounts; issuance captures the legal
issuer revision and normalized issuer snapshot. Changing current settings never
mutates historical requests, documents, delivery records or red-letter cases.
Lists/logs mask issuer legal identifiers, and OIDC issuer data is never copied
into invoice history.

The production PostgreSQL path durably records the settings revision and an
encrypted immutable issuer snapshot at manual-issue confirmation. The separate
memory implementation remains development-only.

## 11. Source-agent container controls

- no published or inbound port;
- non-root user, read-only root filesystem, dropped Linux capabilities and
  `no-new-privileges`;
- a dedicated internal source network;
- egress restricted to local projection/API and the configured ingestion host;
- separate mTLS and payload-signing keys;
- offline overwrite-refusing Ed25519 key generation; private PKCS8 is mounted
  only into its sender stream and raw public base64 only into receiver trust;
- encrypted bounded retry spool;
- byte-exact retry after ACK loss/restart, with spool deletion only after the
  matching sequence and stored source cursor have advanced;
- no shell, package manager or runtime secret dump in the production image;
- structured logs with credential and PII redaction;
- circuit breaker on contract/version drift and authentication failure.

## 12. Production gates

Production synchronization remains disabled until all of the following pass:

- projection grants or API allowlist are independently reviewed;
- arbitrary method/path, SSRF, redirect and cross-user order tests fail closed;
- mTLS identity, signature, sequence, replay and certificate revocation tests
  pass;
- pagination overlap, duplicate batch and out-of-order delivery tests pass;
- submit/issue/refund race tests pass;
- credential and query-string log scans are clean;
- file quarantine, malware rejection and private download tests pass;
- OIDC/MFA/RBAC/fixed-IP administrator tests pass;
- dynamic CIDR CAS/current-IP retention and deployment break-glass recovery
  tests pass in the single-replica topology;
- SMTP secret non-disclosure and restricted QQ/Gmail test-email tests pass;
- settings revision and immutable encrypted issue-snapshot tests pass;
- mock-to-contract and pinned upstream-version contract tests pass.

OIDC back-channel logout replay records use append-only runtime permissions:
the API can `SELECT/INSERT` but cannot update, delete or truncate them. Their
retention path is a separate tools-image command requiring the direct database
and table owner, a minimum 180-day window, an explicit maintenance confirmation
and an audited reason. Eligibility requires both receipt and token expiry to
precede the same cutoff. Deletion is bounded, mutually locked and transactionally
coupled to an immutable audit event, so audit failure preserves the replay row.
