# Source-agent synchronization protocol

## V3 economic eligibility contract (production source of truth)

Each Sub2API/New API source owns five isolated processes: `identities` uses
schema 2.0; `payments`, `usage`, `credits`, and `balances` use schema 3.0.
Every stream has a distinct PostgreSQL LOGIN role, state file, encrypted
pending spool, Ed25519 key and receiver sequence. The four economic streams
share only a read-only encrypted cutover manifest.

Before V3 starts, `source-agent-prod cutover-init` opens one PostgreSQL
`REPEATABLE READ READ ONLY` transaction. It captures the database clock, all
user balances and all economic table/domain ceilings in that same snapshot,
then writes a create-only AES-256-GCM manifest and separately encrypted balance
baseline. A rerun refuses to overwrite either file. Everything in the baseline
is `LEGACY_NON_INVOICEABLE`. The first balances batch is manifest-only and has
`scan_complete=false`; subsequent pages deliver the baseline, including an
empty final page when the source has no users.

Every balance checkpoint also carries signed `baseline_member`. Cutover rows
must be `true`. Reconciliation never infers this flag from current user state:
the balances agent authenticates/decrypts the immutable original baseline and
checks the external user ID against that set. A user created after cutover is
therefore `false`; a missing, corrupt or mismatched baseline stops the stream.
For a signed `false` member, the invoice ledger starts that account at zero on
the global cutover and replays every signed post-cutover economic fact. The
first later balance is a reconciliation checkpoint, not a new legacy baseline;
this preserves post-policy payment and usage that arrived before OIDC binding.

The signed `cutover_manifest` hash is SHA-256 over its strict canonical payload
with `manifest_hash` blank. It covers the projection contract, configuration
hash, unit code, four source ceilings, baseline snapshot hash and row count.
The declared signing key must equal the already verified balances-stream
request key. Other streams use their own independently trusted keys and bind to
the manifest/configuration hashes rather than reusing that key.

V3 transport completeness is separate from immutable economic facts. Every
batch carries a stable `scan_cycle_id`, fixed scan ceiling, published stream
watermark/cursor and `scan_complete`. Intermediate pages do not publish the
ceiling. Only the final page may advance the source-wide watermark after all
prior pages and events commit; an empty final page advances an actually empty
stream. The payload excludes changing scan/watermark values, so full rescans
produce the same payload hash and event ID. Balances use the change-snapshot
rule below instead of replaying every account at each source observation time.

Economic cycles capture their source-side horizon as
`transaction_timestamp()-SOURCE_ECONOMIC_SAFETY_DELAY`. Sub2API payments keep
the ID of the final classified row visible at that horizon as the row ceiling,
but publish the horizon itself as `scan_ceiling_at`/watermark. Page reads remain
bounded by the exact `(horizon,row_id)` tuple. Consequently, a quiet payment
stream proves it was checked through the current delayed horizon instead of
re-publishing the timestamp of its last historical order.

The scan-cycle identity also binds the committed starting cursor revision. An
ACK retry at the same revision therefore keeps the same cycle ID, while the
next empty cycle receives a new ID even when its source ceiling is unchanged;
it cannot append a new sequence to an already finalized cycle.

Balances batches additionally carry signed top-level `scan_snapshot_id` and
integer `scan_snapshot_row_count` (0..2,000,000). Every page in that cycle uses
the same values from the encrypted durable snapshot; an empty snapshot still
sends explicit numeric zero. Other streams omit both fields. The receiver can
therefore refuse publication until the exact snapshot event count is present,
instead of trusting a short final page.

The balances connector still captures the **entire** source balance projection
inside one repeatable-read, read-only transaction on every cycle. After the
cutover baseline, however, the signed event snapshot contains only accounts
that are new or whose service units/negative flag actually changed since the
last acknowledged full capture. `scan_snapshot_row_count` is the exact number
of records in this signed change snapshot, not the total number of source
accounts. The sender includes the preceding acknowledged event snapshot ID in
the emission-snapshot hash preimage, so `A -> B -> A` produces distinct
occurrence identities even if the database clock has equal precision. That
predecessor is sender-side encrypted state; it is not an extra checkpoint
payload field and the receiver does not independently validate the predecessor
chain. The receiver verifies the signed `scan_snapshot_id`, delta count and
ordinary signed batch sequence. A static full capture therefore publishes a
complete, healthy, signed zero-record cycle and advances the balances watermark
without creating another `balance_checkpoint` event.

The encrypted mutable balance file stores both the latest full capture and its
prepared change snapshot. It accepts the prior legacy full-snapshot format and
migrates it only when beginning the next cycle. If the process stops after
preparing that file but before creating `pending.enc`, the unchanged committed
cursor selects the exact prepared snapshot on restart; the source is not read
again. Once the exact ACK commits the cursor, that full capture becomes the
comparison base for the following cycle. A previously present account missing
from a later full capture fails closed because V3 balances have no deletion
tombstone. This delta rule is balances-only: payments, usage and credits retain
their immutable fact rules and are never coalesced.

On the receiver, omission from a published balance delta means unchanged only
for an already bound account whose catch-up is complete. If new payment,
credit, or usage facts exist after its last checked boundary, their receiver
visibility must be covered by a published balances cycle. Funding visibility
is proven through the exact source event, payload revision, scan-cycle mapping,
and published payments cycle; an unmapped eligible funding lot fails closed.
When that balances cycle has no real checkpoint for the account, the receiver
persists a separate immutable carry-forward proof rather than fabricating a
source checkpoint. The proof binds the cycle snapshot/count, final sequence and
batch body hash, and the latest real signed actual ordered by
`(as_of,source_sequence,id)`. Real checkpoints and carry proofs are mutually
exclusive for one account/cycle and are evaluated together in
`(as_of,source_sequence,real-before-carry,id)` order. This lets a positive
classification from one evidence item participate in the next conservation
check. Parked checkpoints for unrelated identities in the same published cycle
do not block a bound account; that account's advisory lock and completed
catch-up state provide the isolation boundary.

| Stream | Allowed entities |
|---|---|
| `payments` | `payment_order`, `payment_candidate`, `payment_adjustment`, `subscription_purchase` |
| `usage` | `usage_event` |
| `credits` | `credit_event` |
| `balances` | `cutover_manifest`, `balance_checkpoint` |

V3 never emits tombstones. Signed invoice-side facts survive normal upstream
log cleanup. A stable ID that reappears with a different payload conflicts and
freezes reconciliation. A fact deleted before capture is handled by the next
balance checkpoint: unknown positive balance is non-cash credit; unknown
negative balance freezes that account. Refunds freeze the whole funding lot in
V1.

Units are explicit canonical non-negative integer strings:

- Sub2API uses `SUB2_BALANCE_1E8`. Wallet usage is PostgreSQL
  `round(actual_cost,8)*1e8`, matching `users.balance numeric(20,8)`. Negative
  balances are sent as zero plus `balance_negative=true`, freezing that account.
  Contract `sub2api-economic-v4` hashes normalized numeric multiplier and fee
  values, not mutable settings timestamps. Missing, duplicate or non-numeric
  settings, a multiplier other than one, or a fee outside `0..100` with at most
  two decimal places remain unhealthy; a real valid fee change
  changes the hash.
- New API uses native integer `NEWAPI_QUOTA`. rc.25 wallet derivation is healthy
  only with `QuotaPerUnit=500000`, `Price=1`, and every `TopupGroupRatio=1`.
  Contract `newapi-economic-rc25-v4` normalizes equivalent numeric text and
  represents every valid all-one ratio object with one semantic constant, so
  group names, JSON ordering and whitespace do not create false drift.
  Unknown providers remain `pending_manual` without wallet units.

Sub2API cash wallet units additionally require a positive per-order `amount`
and `pay_amount`, an order `fee_rate` in `0..100` with at most two decimal
places, and the exact CNY invariant
`pay_amount = amount + ceil(amount * fee_rate) / 100`. This reproduces
Sub2API's two-decimal fee `RoundUp` while remaining invariant under harmless
settings timestamp rewrites; an order credited under a non-unit multiplier
does not satisfy the formula. Payment fulfilment redeem codes are excluded from
bonus credits. New API candidates
require `status=success` and `complete_time`; failures/pending rows are not
projected. Mutable New API payments and redeem-code domains are fully rescanned
each publishable cycle, so a pre-cutover ID completed/redeemed after cutover is
captured. Every scan and every V3 `check-db` rechecks the live contract and
current configuration hash against the create-only cutover manifest; drift blocks
without advancing the watermark.

Bridge V4 SECURITY DEFINER functions expose only numeric IDs, event times, service units,
non-secret method/provider identifiers and health/configuration hashes. They do
not expose email, password, log content, model/token/IP/request metadata, raw
trade references, redeem keys, provider payloads or secret settings. Equal
timestamps from incomparable tables use different `causal_domain` values; the
receiver freezes `AMBIGUOUS_EVENT_ORDER` instead of inventing precedence.

The source agent is an outbound-only, read-only sidecar. It reads a minimal
economic/identity projection from Sub2API or New API and sends signed batches to
the independent invoice system. It has no source mutation method and must not
share a process, database role, secret mount, or network identity with invoice
web/API.

Deployment and recovery steps are in
[`SOURCE-AGENT-RUNBOOK.md`](SOURCE-AGENT-RUNBOOK.md).

The immutable compatibility schema is
[`source-agent-batch.v1.schema.json`](../contracts/source-agent-batch.v1.schema.json).
New payment-candidate, adjustment, and identity-unknown semantics use
[`source-agent-batch.v2.schema.json`](../contracts/source-agent-batch.v2.schema.json).
An agent must not put a v2 entity into a `schema_version=1.0` envelope. Every
v2 envelope has a registered UUID `source_instance_id`, source type
Schema 2.0 uses source `sub2api|newapi`, stream `payments|identities`, mode `db_projection`, and
mandatory `projection_status=healthy|blocked`. V1 has no stream/health field and
is retained only for the offline mock fixture. `cmd/source-agent-prod`
constructs v2 only.

## 1. Verified source contracts

The connector implementation was checked against these source snapshots on
2026-08-20:

- Sub2API `v0.1.179`, commit `75f88be...`: `payment_orders` still contains
  `refund_amount`, `refund_at`, `completed_at`, `updated_at`; the admin list and
  numeric-detail GET DTO still returns the minimal financial fields plus other
  fields that this agent deliberately ignores. Admin list ordering remains
  `created_at DESC` with offset pagination. Admin authentication still accepts
  `x-api-key` or an administrator bearer JWT.
- New API `v1.0.0-rc.25`, commit `f116414...`: `top_ups` contains no
  `updated_at`, currency, refund/chargeback field, or provider-settlement proof.
  `GET /api/user/topup` remains AdminAuth bearer authentication and returns
  `success/data/{page,page_size,total,items}` ordered by `id DESC`.

The first production read-only observation for this milestone found Sub2API
runtime `0.1.178`; the launch recheck found its OTA-updated runtime `0.1.179`
at the exact audited commit above, with the required payment columns unchanged.
New API remains rc25 with the same top-up limitation. Neither observation
authorized source writes and no source row or credential was copied into this
repository.

## 2. Production source priority

### 2.1 Preferred: dependency-free Source Bridge V4

`Sub2APIDBConnector` has EXECUTE only on the independently reviewed function:

```text
invoice_bridge.sub2api_payments_v4(text,jsonb):
id, user_id, status, order_type, amount, pay_amount, refund_amount,
currency, completed_at, refund_at, created_at, updated_at, payment_type, provider_key
```

Sub2API v0.1.179 has no `payment_orders.currency` column. The actual per-order
gateway currency is `provider_snapshot.currency`. Its source
`paymentProviderConfigCurrency` permits configured currency only for Stripe and
Airwallex; EasyPay, Alipay and WeChat Pay are fixed to default CNY. The reviewed
payments bridge therefore prefers a valid matching v2 snapshot currency,
and derives CNY only for those three exact fixed-provider keys when their v2
snapshot omitted/invalidated currency. Stripe, Airwallex, provider mismatch and
unknown-provider rows without valid currency are excluded. The page operation
exposes CNY only. `legacy_health` returns aggregate total, exposed-CNY,
known-non-CNY and blocked-unknown counts. There is no fixed-currency
environment variable, and the reader never receives direct `provider_snapshot`
access.

Known non-CNY rows are intentionally unsupported by V1 and excluded without
blocking reconciliation; if an order previously exposed as CNY becomes
explicitly non-CNY, normal repeated-miss tombstoning withdraws its entitlement.
If aggregate health reports blocked-unknown currency rows, CNY rows continue
to synchronize but that reconciliation cycle is marked blocked: it does not
increment missing counters or emit tombstones. This prevents an excluded row
from being misclassified as deleted. Only IDs previously exposed by the
payments bridge participate in later repeated-miss tombstone inventory.

`NewAPIDBConnector` executes only
`invoice_bridge.newapi_payments_v4(text,jsonb)`, whose returned page contains:

```text
top_ups:
id, user_id, amount, money, payment_method, payment_provider,
create_time, complete_time, status
```

It does not select `trade_no`. The numeric `top_ups.id` crosses the boundary as
`external_order_id` and is the user/admin `source_order_id` display reference;
it is never promoted into the provider `trade_no` column. Neither connector
joins or reads `users`.

Review-only PostgreSQL grant templates are provided separately:

- [`sub2api-source-projection-grants.postgresql.sql`](../contracts/sub2api-source-projection-grants.postgresql.sql)
- [`newapi-source-projection-grants.postgresql.sql`](../contracts/newapi-source-projection-grants.postgresql.sql)

They are not migrations and the agent never executes them. The cluster
superuser that owns the current database must apply both source and economic
contracts through the reviewed `ON_ERROR_STOP` maintenance wrapper. The dedicated login
must be transaction-read-only, have a short statement timeout and no source-app
role membership, and must not be mounted into invoice web/API. The production
launcher requires zero effective raw SELECT and rejects extra function grants,
mutation/sequence privileges, schema CREATE, elevated role attributes or any
inherited role. The database gate separately proves exact bridge-owner columns,
function body hashes, RLS disabled and zero persistent relation dependencies.

Each login must also have effective CONNECT, exactly `CONNECTION LIMIT 2`, no
effective TEMPORARY privilege and `NOINHERIT`. The launcher proves these facts
before every start. PostgreSQL grants TEMP through PUBLIC by default, so the DBA
must remove that inherited grant without weakening the upstream application's
own explicit access.

Payments and identities are different trust streams and therefore use different
login roles and DSN secret files. Sharing one source-wide reader would give
each process columns it does not need and is rejected by the exact-grant gate.

### 2.2 Central-IdP identity projection without users/email

Only the explicitly configured central OIDC provider can produce an
`identity_binding`. Email is never a join key or automatic binding proof.

Sub2API reads these fields only from
`invoice_bridge.sub2api_identities_v4(text,jsonb)` using `(updated_at,id)` keyset
pagination. The function, not merely Go filtering, pins `provider_type='oidc'`, the
exact provider key/HTTPS issuer and a non-empty provider subject. It accepts either
the dedicated `verified_at` timestamp or the exact JSON boolean
`metadata.email_verified=true` written by Sub2API's verified-email OIDC flow.
For the latter case, the projected verification time is the immutable identity
`created_at` (the local binding-persistence time, not the IdP's original email
verification time). String values such as `"true"`, false/missing/non-object
metadata and a missing creation time fail closed. Among invoice bridge roles,
only the NOLOGIN bridge owner gains `SELECT(metadata)`; the LOGIN reader receives
EXECUTE only, and the function never returns metadata, email or other claims:

```text
id, user_id, provider_type, provider_key, provider_subject,
verified_at, issuer, created_at, updated_at
```

New API reads only `invoice_bridge.newapi_identities_v4(text,jsonb)` operations:

```text
page:
id, user_id, provider_id, provider_user_id, created_at, provider_slug

provider_contract (trust check only):
slug, enabled, well_known, authorization_endpoint, token_endpoint,
user_info_endpoint, contract_ok
```

The reviewed function is pinned to slug `solov-sso`; the connector requires all four
non-secret endpoints to be present, HTTPS, query/userinfo/fragment-free, on the
configured exact issuer and under its path. The binding view joins by provider
ID only when `contract_ok=true`. The role has no raw-table grant and never
receives `client_id`, `client_secret`, scopes/field mappings, access policy, or
any user column.
Because user status cannot be learned without reading `users`, v2 emits
`user_status=unknown`; consumers must not infer that the source user is active.

### 2.3 Explicit fallback: read-only admin API

The HTTP connectors are fallback only. Their exact origins are immutable and
their credentials live only in the sidecar.

Sub2API uses GET only:

```text
GET /api/v1/admin/payment/orders?page=N&page_size=100
GET /api/v1/admin/payment/orders/{numeric_id}   # reserved/issued refund watch
```

The list is offset-paginated and can duplicate/omit rows during concurrent
changes. Every run therefore starts at page one, deduplicates by source order
ID, and eventually scans every page. Up to 100 reserved/issued numeric IDs may
also be re-read directly in the same scan. A missing first-page item is never
proof that money is unavailable.

New API uses GET only:

```text
GET /api/user/topup?page=N&page_size=100
```

There is no safe read-only detail/refund endpoint. Passing watch IDs fails
closed. The bearer credential must be a dedicated least-privilege administrator
PAT; browser sessions are not a production integration mechanism.

## 3. Cursor and reconciliation rules

Sub2API database scans use:

```text
WHERE (updated_at, id) > (:last_updated_at, :last_id)
ORDER BY updated_at ASC, id ASC
LIMIT <= 500
```

New API database incrementals use `id > :last_id ORDER BY id ASC`, which finds
new rows but cannot see a later status change. A complete `id ASC` reconciliation
scan is therefore mandatory on a bounded schedule. New API OAuth bindings have
the same limitation. Sub2API also needs periodic full reconciliation for hard
deletes and the residual risk of equal-timestamp updates.

`SyncCoordinator` persists a cursor only after `Publisher` receives a matching
batch acknowledgement. Before the first network attempt, production atomically
writes the exact raw body, body hash, starting cursor and target cursor to an
AES-256-GCM pending spool. An ACK loss or process restart therefore resends the
same batch ID and byte-identical body (only the detached signature timestamp is
refreshed). Sequence state advances only after the ACK matches source, stream,
batch, sequence and record count; the target cursor then advances, and only
afterward is the pending spool deleted. Crash recovery also recognizes the
locally ACK-proven sequence and completes its stored cursor without skipping to
a newly observed cursor. On every restart the coordinator must inspect and
replay that spool before calling `Connector.Scan`; otherwise a stateful balance
connector could replace its durable snapshot before the pending target cursor
is committed.

The balances durable change state also closes the smaller pre-spool crash
window: replacing `balance-current.enc` records the committed base snapshot ID
alongside the newly captured full state and exact change snapshot. A restart
whose cursor still names that base reuses the prepared change snapshot rather
than treating it as acknowledged or capturing past it.

Event IDs remain deterministic from source ID, entity, external ID, operation
and canonical payload hash. Adding `stream_id` does not change established
event IDs. Production uses durable compare-and-swap implementations of
`CursorStore` and `SequenceStore`; the provided default is `FileStateStore` on
a dedicated agent state volume:
it binds source+stream, uses a cross-process lock, compares revision and old
value, writes a 0600 same-directory temporary file, fsyncs it, atomically
replaces the state and fsyncs the directory (Windows uses write-through atomic
replacement and requires a separately verified restrictive volume ACL). The
state file must be explicitly initialized once; missing state/volume fails
closed. In-memory implementations are for tests only. The agent must never hold
an invoice PostgreSQL DSN for sender state.
Payment and identity projections use distinct streams, state files, encrypted
spools and processes/containers. They may share a source instance ID, but the
receiver and sender always scope sequence, predecessor hash, batch replay and
event replay to the exact `(source_instance_id, stream_id)` pair. Their
incompatible keysets are never packed into one cursor.

Hard deletion is never inferred from one missing scan. A durable reconciliation
store requires three consecutive misses across completed scans before emitting
a tombstone. Incremental, partial and failed scans cannot increment misses. The
seen journal is fsynced after ACK, cursor recovery survives restart, and a
blocked projection preserves prior counters without advancing them. Tombstones
quarantine local eligibility/binding; they never erase issued legal evidence.

Every accepted batch persists source runtime/agent versions, projection health
and receiver time. A signed empty batch is the heartbeat for a static source;
a duplicate replay does not refresh freshness. The receiver rejects a runtime
version that differs from the approved `source_instances` value. Both streams
must be fresh and have no queued/dead event before submit or final manual issue.

An identity/payment may precede the invoice user's first central-OIDC login.
That event enters `waiting_dependency` under a source-scoped keyed HMAC, does
not consume the dead-letter budget and does not block unrelated users or global
readiness. Login wakes identity waits; a verified binding wakes only the same
source+external-user payments. A 12-hour fallback covers a missed wakeup
without rewriting every parked row at five-minute cadence. The admin health
view exposes the waiting count; alert well before 100,000 and investigate
migration coverage. Waiting evidence is not automatically deleted because it
may be needed when that paid user later binds.
Funding and binding writes compare signed source time plus batch sequence, so
older completed/bind events are audited no-ops after a refund or tombstone.

## 4. Financial semantics

Sub2API's three source amounts are not interchangeable. For balance orders,
`amount` is credited balance after `BalanceRechargeMultiplier`; for subscription
orders it is the plan price. `pay_amount` is actual gateway money after fee and,
when configured, subscription USD-to-CNY conversion. `refund_amount` is
cumulative refund in the source `amount` unit. The agent calculates the exact
gateway-money refund as:

```text
gateway_refund = round_currency(pay_amount * refund_amount / amount)
```

A full source refund returns exactly `pay_amount`; a partial refund is rounded
half-up to the per-order currency precision using rational arithmetic. The
normalized `payment_order` carries both source refund and
`gateway_refund_amount`. A positive cumulative gateway refund also emits only
`payment_adjustment/refund` with
`basis=absolute_cumulative_gateway_refund`. A difference between `amount` and
`pay_amount` is never an adjustment: it may be a recharge multiplier,
subscription conversion or fee. Consumers compare source version/hash and
never apply the same cumulative adjustment twice.

Sub2API auto-eligibility remains fail-closed and requires at least:

```text
status=COMPLETED
completed_at present
pay_amount > 0
refund_amount = 0
supported currency
current external-user ownership/binding
not already consumed by the local ledger
```

New API always emits `payment_candidate`, including when source status is
`success`. Its `verification_state` is fixed to `pending_manual`. It never emits
`payment_order`, never invents a currency, and never grants automatic invoice
entitlement, because the current source contract cannot distinguish provider
settlement from administrator completion and has no refund evidence.
The signed `money` value is retained only as the candidate upper bound. Manual
review can approve a smaller positive CNY amount but can never exceed that
bound. One administrator proposes an exact amount/evidence hash and a distinct
administrator must approve the same tuple; neither reviewer may begin or
confirm issuance for a request funded by that lot. Reject/freeze atomically
invalidates a pending proposal, so later approval must restart with a new first
reviewer; the superseded review rows remain immutable audit evidence.

Because New API has no refund field, an MFA/IP-restricted administrator may
record an evidence-backed manual remaining cap for an already verified lot.
The cap may only decrease. The same funding-reduction transaction rejects and
releases unissued reservations, moves issued exposure to `refund_attention`,
and opens a red-letter case. It also persists a non-increasing review ceiling
and invalidates the old approval, so source rescans cannot restore money and a
non-zero net amount requires a fresh two-person review at or below that ceiling.

## 5. Signed transport, sequencing, and replay

Production agents initiate HTTPS to the ingestion exact origin. TLS 1.2 or
later, server verification, and a client certificate are required. The mTLS
certificate SAN identifies one source instance. The body is signed separately
with the active Ed25519 key:

```http
POST /internal/v1/source-batches
Content-Type: application/json
X-Source-ID: 10000000-0000-4000-8000-000000000001
X-Stream-ID: payments
X-Batch-ID: <uuid>
X-Sequence: 42
X-Sent-At: 2026-08-20T12:00:03Z
X-Content-SHA256: <64 lowercase hex>
X-Signature-Key-ID: source-signing-2026-01
X-Signature: <base64 Ed25519 signature>
```

Signature input is UTF-8, LF-separated, with no trailing LF:

```text
POST
/internal/v1/source-batches
X-Source-ID
X-Stream-ID
X-Batch-ID
X-Sequence
X-Sent-At
X-Content-SHA256
```

The receiver verifies clock skew, exact body hash, the exact
source+stream+key-ID public-key set,
signature, certificate/source match, the exact body/header stream, sequence,
predecessor body hash, batch ID, event ID and payload hash before committing.
An identical batch retry is an idempotent duplicate; a batch/event ID reused
with different bytes is rejected as a security incident. The sender advances
sequence and source cursor only after a matching ACK. Stream substitution
invalidates the Ed25519 signature.

Cross-language receiver implementations must pass the fixed
[`source-agent-signature.v2.json`](../contracts/examples/source-agent-signature.v2.json)
test vector against its referenced byte-exact batch before deployment.

Signing and upstream credentials are request-scoped snapshots. Rotation swaps
the active snapshot while in-flight requests retain their copy. A read-only API
request retries 401/403 once with a fresh snapshot; there is no rapid retry loop.
Secret values are never formatted, returned, logged, placed in URLs, or stored
in cursor/batch state.

## 6. URL, DNS, TLS and response safety

`RestrictedHTTPClient` enforces:

- exact allowlisted scheme, hostname and port;
- HTTPS by default; plaintext HTTP only by explicit development/internal
  configuration;
- at least one non-world-open destination CIDR;
- all DNS answers must be in an allowed CIDR, checked at construction, before
  each request and again at dial time;
- no userinfo, origin path/query/fragment, redirects, proxy environment,
  attacker-controlled absolute URL, percent/dot path segment, or arbitrary
  method;
- TLS 1.2 minimum, normal certificate verification and optional mTLS files;
- bounded response bodies and strict acknowledgement JSON.

Host allowlisting and CIDR allowlisting are both required. Allowing a hostname
without validating resolved IPs is not sufficient SSRF protection.

## 7. Acknowledgements and operations

```json
{
  "accepted": true,
  "source_instance_id": "10000000-0000-4000-8000-000000000001",
  "stream_id": "payments",
  "batch_id": "018f4ec7-08d0-7b72-a2d4-1df742eec6c8",
  "sequence": 42,
  "accepted_records": 3,
  "duplicate": false
}
```

| Status | Meaning | Agent action |
| --- | --- | --- |
| 200 | accepted or identical duplicate | CAS sequence/cursor; delete encrypted spool |
| 400/422 | contract/invariant failure | quarantine; do not skip |
| 401/403 | mTLS/signature/source failure | open circuit and alert |
| 409 | sequence/hash-chain conflict | stop source; operator reconciliation |
| 413 | too large | stop; lower reviewed page limit and replay the same pending sequence under operator control |
| 429 | backpressure | respect `Retry-After`, retry with jitter |
| 5xx | transient receiver failure | bounded exponential retry from encrypted spool |

Do not log bodies, credentials, raw trade numbers, emails, identity subjects,
issuer URLs, query strings, invoice tax IDs, or source connection strings.

## 8. Production launcher behavior

`cmd/source-agent-prod` is the only production entrypoint. Its explicit
commands are:

```text
source-agent-prod init-state
source-agent-prod init-reconcile
source-agent-prod cutover-init
source-agent-prod check-cutover
source-agent-prod check-db-static
source-agent-prod check-db
source-agent-prod check-state
source-agent-prod inspect-pending
source-agent-prod healthcheck
source-agent-prod run
source-agent-prod version
```

The init commands create but never overwrite source+stream cursor or reconcile
state. `check-db-static` is the pre-cutover connection/ACL/function-SHA and
source-boundary check; it does not read a manifest. Full `check-db` repeats that
gate and requires every V3 economic stream to match the live semantic contract
and configuration hash recorded by the encrypted create-only manifest.
`check-state` is offline/read-only, rejects stale locks, validates all
reconcile files and decrypts a pending spool with the real mounted key.
`inspect-pending` uses the same production configuration, state envelope and
authenticated encrypted-spool reader, but emits exactly one versioned JSON
object containing only source/stream, batch/hash/count/scan metadata, hashed
cursor positions, durable sequence state and a reachable crash-window
classification. It never emits records, payloads, source cursors, DSNs, key
material or tokens, never opens the source database or ingestion transport, and
never creates a lock or writes state. It fails closed unless the state directory
is lock-free and the pending batch, cursor transition and hash/sequence chain
are mutually consistent.
`healthcheck` adds a bounded local heartbeat-age check. `run`
requires an existing state file, a source PostgreSQL DSN secret file, a
base64-encoded 32-byte spool-key file, Ed25519 PKCS8 signing key, mTLS files and
an exact ingestion origin/CIDR allowlist. It rejects mock/admin-API modes,
direct secret environment values, non-PostgreSQL dialects and missing files;
there is no mock fallback.

`cmd/source-keygen` is the offline provisioning command for directly compatible
sender/receiver trust material. It writes a 0600 Ed25519 PKCS8 private PEM and a
0644 standard-base64 raw public-key file, refuses overwrite, and never prints
key bytes. Receiver public keys are indexed by source+stream+key ID.

Each legacy/V2 process owns exactly one `payments` or `identities` stream; V3
processes own exactly one of `payments|usage|credits|balances`. It sets and
proves `transaction_read_only=on`, a statement timeout and a two-connection
maximum before scanning; it pins UTC and `public,pg_catalog` search path.
Sub2API performs keyset incrementals plus scheduled
reconciliation. New API starts with a full scan after every restart and forces
a complete scan at least every configured interval (maximum 24 hours), with
incrementals between full scans. Very large scans yield after the configured
page budget but retain the same scan mode/cursor until completion. Transient
failures use bounded exponential
backoff plus cryptographic jitter; contract/auth/sequence/oversize failures stop
fail-closed, and repeated otherwise-transient failures open a bounded circuit
instead of retrying forever. SIGTERM/SIGINT cancels database and HTTP work and
exits cleanly.

## 9. Production gates still requiring external proof

Code and local tests cannot prove the following deployment facts:

- the approved LOGIN source roles have only exact bridge-function EXECUTE, the
  NOLOGIN owner has exact reviewed columns, and neither has inherited mutation,
  ownership or schema-CREATE privilege;
- the reviewed Bridge V4 function hashes/ACLs match, have zero upstream
  `pg_depend`, every read source table has RLS disabled, and the fixed-CNY
  provider allowlist still matches the audited upstream source;
- central IdP provider key/slug and issuer values are the approved production
  values;
- ingestion mTLS SAN mapping, CA chain, current/previous signing public keyset,
  revocation and clock synchronization work across replicas;
- the dedicated agent state volume is persistent, restrictive, initialized and
  monitored; the spool key is backed up and mounted 0600, and receiver-side
  source+stream event uniqueness is deployed;
- New API full reconciliation is scheduled and alerted; Sub2API Admin API
  fallback deployments additionally schedule reserved/issued refund watches;
- end-to-end source reads and ACKs succeed with real least-privilege credentials.

Until those are verified, the production DB launcher is implemented and locally
tested but live wiring remains blocked. Lack of access must never be replaced by
guessed DTO fields, credentials, issuer, currency, or refund semantics.
