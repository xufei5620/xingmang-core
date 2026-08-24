# Production source-agent runbook

This runbook deploys the outbound-only PostgreSQL projection agent without
changing Sub2API or New API source code, containers, application tables or
application files. Bridge V4 creates five fixed dynamic SECURITY DEFINER
functions per source, ten least-privilege active LOGIN roles, two
credential-free NOLOGIN compatibility holders and two NOLOGIN function owners.
LOGIN callers receive only exact function EXECUTE. Applying those reviewed
contracts is a separate cluster-superuser/DB-owner action; the agent never runs
an install or rollback contract.

## 1. One container per stream

| Source | Stream | Connector | Required schedule | Extra configuration |
| --- | --- | --- | --- | --- |
| Sub2API | `identities` | `Sub2APIIdentityDBConnector` | incremental + reconciliation | central OIDC provider key + canonical issuer |
| Sub2API | `payments` V3 | `PaymentV3DBConnector` | updated-at scan + complete reconciliation | exact CNY/config/unit evidence |
| Sub2API | `usage` V3 | `EconomicDBConnector` | ID scan + full rescan | wallet billing only, scale 1e8 |
| Sub2API | `credits` V3 | `EconomicDBConnector` | full scan every cycle | bonus/rebate domains; payment codes excluded |
| Sub2API | `balances` V3 | `BalanceDBConnector` | atomic snapshot pages | encrypted cutover/baseline required |
| New API | `identities` | `NewAPIIdentityDBConnector` | incremental + forced full scan <= 24h | central OIDC provider slug + canonical issuer |
| New API | `payments` V3 | `PaymentV3DBConnector` | full post-cutover scan every cycle | all candidates manual; failed/pending excluded |
| New API | `usage` V3 | `EconomicDBConnector` | ID scan + full rescan | consume logging must remain enabled |
| New API | `credits` V3 | `EconomicDBConnector` | full scan every cycle | check-in/redemption domains |
| New API | `balances` V3 | `BalanceDBConnector` | atomic snapshot pages | encrypted cutover/baseline required |

Each row is an independent non-root container with a distinct writable 0700
state directory, state file, encrypted spool, 32-byte spool key, mTLS client
identity and Ed25519 signing key. Never share a writable state volume between
rows. The invoice API/web containers receive none of these mounts or variables.
Only V2 identity streams have a reconcile inventory: V3 immutable facts never
emit deletion tombstones. The ten state directories plus encrypted cutover
files are one backup/restore unit.

## 2. Receiver prerequisites

Before starting a sender:

1. Create the `source_instances` row and record its UUID. `SOURCE_ID` is this
   UUID, not a display slug.
2. Map the stream's mTLS certificate to the same UUID.
3. Register its raw public Ed25519 key under the exact
   `(source UUID, stream ID, key ID)` tuple.
4. Verify identities accept V2 and economic streams accept strict V3; require
   body/header `stream_id`, the exact entity/stream matrix, scan-cycle fields,
   and manifest registration before baseline facts.
5. Verify receiver sequence, batch and event uniqueness are scoped to
   `(source UUID, stream ID)` and its ACK echoes both fields.

Do not start a sender against a receiver that infers a stream, resolves signing
keys only by source, or accepts a missing `X-Stream-ID`.

## 3. Source database prerequisites

Bridge V4 replaces all source-dependent views. For each source, pre-create six
reader roles, then have the cluster superuser that owns the current database
apply both contracts through the reviewed maintenance wrapper:

- `contracts/sub2api-source-projection-grants.postgresql.sql`
- `contracts/newapi-source-projection-grants.postgresql.sql`
- `contracts/sub2api-economic-projection-grants.postgresql.sql`
- `contracts/newapi-economic-projection-grants.postgresql.sql`

On the production migration, preserve the existing six source reader roles
before legacy reconcile by using `scripts/preserve-source-reader-roles.sh
--mode export` with a new root-only mode-0600 file. After reconcile removes the
legacy roles, restore that file, install both Bridge contracts and run all five
`check-db` commands. The file contains SCRAM verifiers: never display it, and
encrypt/archive or securely delete it immediately after the five checks pass.

Put each complete approved PostgreSQL connection string in its matching 0600
regular, non-symlink `SOURCE_DB_DSN_FILE`. Never reuse a payments DSN for an
identities stream or the reverse. The production process rejects ambient `PG*`
configuration, sets `default_transaction_read_only=on`, and proves
`SHOW transaction_read_only = on` before reading. It also forces a 15-second
statement timeout, UTC and at most two connections. LOGIN callers receive only
`invoice_bridge` schema USAGE and their exact function EXECUTE; all raw source
columns belong to the NOLOGIN bridge owner. Before scanning, `check-db`
inventories effective privileges and refuses raw SELECT, role membership,
schema-create, mutation, sequence, unexpected function execution or a
dependency-bearing/unsafe function.

For New API, `top_ups.id` is the `source_order_id` display reference. Do not
grant/read `trade_no`, and do not put `top_ups.id` into a provider-trade-number
field.

For New API identities, the caller executes only
`invoice_bridge.newapi_identities_v4(text,jsonb)`. It must fail on both raw
OAuth tables and all client secret/policy/mapping columns. The production slug
is exactly `solov-sso`; all well-known, authorization, token and user-info
endpoints must validate against the same configured HTTPS issuer.

For Sub2API payments, the caller executes only
`invoice_bridge.sub2api_payments_v4(text,jsonb)` and cannot read
`payment_orders` or `provider_snapshot`. Its `legacy_health` operation exposes
only four aggregate values. At launch,
`blocked_unknown_currency_rows` must be zero. Known non-CNY rows are counted and
excluded from the CNY-only ledger without blocking reconciliation. In
operation, any blocked-unknown row raises an alert and blocks missing/tombstone
reconciliation while exposed CNY rows continue.
Never widen the fixed-CNY allowlist (`easypay`, `alipay`, `wxpay`) without a new
source review, and never restore the removed `SUB2API_PROJECTION_CURRENCY`
setting.

### V3 cutover order (mandatory)

1. Disable invoice submission and stop all five source streams for the source.
   Verify the approved runtime and exact Bridge V4 contract hashes.
2. Verify backup/restore evidence. As cluster superuser and current database
   owner, use maintenance wrapper modes `install-source` then
   `install-economic`; bare psql is forbidden. Run the `bridge-v4` upgrade gate
   and `check-db` separately with all five DSNs. Extra grants, RLS, unexpected
   ownership/function body or schema CREATE are hard failures.
3. Create 0700 `$SOURCE_STATE_ROOT/{source}-{stream}` directories and
   `$SOURCE_CUTOVER_ROOT/{source}`. Generate distinct spool/signing keys per
   stream, one source cutover AES key and one balance-snapshot AES key.
4. Stop the matching New API/Sub2API application container while keeping its
   PostgreSQL container running. Keep it stopped, acknowledge that operator
   action, and run maintenance mode `cutover-quiescence-preflight`. It must see
   zero other client backends and zero prepared transactions. The SQL gate
   cannot prove the container stays stopped.
5. Without restarting the upstream application, run the Compose `cutover`
   profile exactly once:

   ```text
   docker compose -f deploy/docker-compose.sources.yml --profile cutover \
     run --rm sub2api-cutover-init
   docker compose -f deploy/docker-compose.sources.yml --profile cutover \
     run --rm newapi-cutover-init
   ```

   The command is read-only to the source DB. It must create `manifest.enc` and
   `baseline.enc`; a second run must fail. Immediately run `check-cutover` (the
   same profile with command override) and archive their hashes offline.
   Both commands require `ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00`,
   reject either cutover/database clock at or after that instant, and require
   the exact source contract (`sub2api-economic-v3` or
   `newapi-economic-rc25-v3`). Run these checks before applying invoice
   migration 0011, then bind the verified hashes into the signed rollback
   package; never discover an invalid pair only after the one-way migration.
   Later balance snapshots must retain this encrypted baseline: it is the only
   authority for signed `baseline_member` (`true` for original users, `false`
   for post-cutover users). Loss/corruption is fail-closed, never recaptured.
   Only after both encrypted files verify may the upstream application restart.
6. Register all five stream certificate/key tuples and source runtime in the
   receiver. The balances key ID must equal the key declared by the manifest;
   other streams keep their independent keys.
7. Run `init-state` for all ten streams. Run `init-reconcile` only for the two
   V2 identity streams; V3 must reject that command.
8. Start balances first. Its first acknowledged batch must contain only the
   cutover manifest with `scan_complete=false`; let the baseline and its empty
   final page finish. Start payments, credits and usage, then identity. Keep
   public invoice submission disabled until all four economic watermarks and
   balance reconciliation are healthy.

Rollback before public enablement means stop the four V3 streams and preserve
their state/cutover files intact; do not delete and recapture a later cutover.
After any accepted fact, rollback never means removing invoice facts or
reusing sequence zero.

## 4. Generate and mount keys

Build the disposable keygen target, generate ten independent stream key pairs, then
delete the tools image. The key IDs must exactly match the compose/trust tuple:

```text
docker build --target keygen -f agents/Dockerfile.production \
  -t invoice-source-keygen:one-time agents
install -d -m 0700 -o 65532 -g 65532 /absolute/source-keygen-work

# Repeat with the four IDs/filenames below. Do not mount or relax the parent
# secrets directory; only the disposable work directory is writable by 65532.
docker run --rm --user 65532:65532 \
  -v /absolute/source-keygen-work:/work \
  invoice-source-keygen:one-time \
  -key-id 2026-08-payments \
  -private-out /work/sub2api_payments_signing_key.pem \
  -public-out /work/sub2api_payments_signing_key.pub.b64

# sub2api identities: key ID 2026-08-identities
# newapi payments:     key ID 2026-08-payments
# newapi identities:   key ID 2026-08-identities
# Use distinct private/public files even where the display key ID is equal;
# trust resolution is scoped by (source UUID, stream ID, key ID).
install -m 0400 -o 65532 -g 65532 /absolute/source-keygen-work/*_signing_key.pem /absolute/secure/
install -m 0444 -o root -g root /absolute/source-keygen-work/*.pub.b64 /absolute/trust/
rm -rf /absolute/source-keygen-work
docker image rm invoice-source-keygen:one-time

openssl rand -base64 32 > /absolute/secure/source-spool.key
chmod 0600 /absolute/secure/source-spool.key
```

The keygen refuses overwrite and prints paths/key ID only. Back up the spool key
in the approved secret store: losing it while a pending spool exists makes exact
recovery impossible and intentionally stops the stream. Issue the mTLS
certificate separately from the approved source-agent CA.

## 5. Build and pin

Run the Go release gates, then build with digest-pinned base-image arguments:

```text
go test -race ./...
go vet ./...
govulncheck ./...

docker build -f agents/Dockerfile.production \
  --build-arg GO_IMAGE=golang:1.25.13-alpine@sha256:<approved> \
  --build-arg ALPINE_IMAGE=alpine:3.23@sha256:<approved> \
  --build-arg SOURCE_AGENT_VERSION=0.3.0 \
  -t "invoice-source-agent:$INVOICE_IMAGE_TAG" agents
```

Record and deploy the resulting image digest, not only its tag. The final image
is scratch-based, runs UID/GID 65532 and contains only CA certificates and
`source-agent-prod`; it contains no shell or key generator. The shared
`INVOICE_IMAGE_TAG` comes from the verified release manifest; the independent
`SOURCE_AGENT_VERSION` build argument is the binary/protocol version.

## 6. Initialize and start

Populate the complete variable contract from `CONFIGURATION.md` section 5.
Create each state directory as UID/GID 65532 mode 0700. Run exactly once with
the final state mount:

```text
source-agent-prod check-db
source-agent-prod init-state
# V2 identities only:
source-agent-prod init-reconcile
```

`check-db` opens the configured source DSN and exits only after the effective
database grants match the exact source/stream projection contract. Init
commands refuse overwrite. `SOURCE_RECONCILE_FILE` and threshold 3 are required
only by V2 identities; V3 uses complete rescans and stable invoice-side facts.
Then run normally:

```text
source-agent-prod run
```

There is no mock/Admin-API fallback. Missing state, wrong source/stream,
insecure files, bad key material, unsafe DNS/CIDR, non-mTLS HTTPS, non-read-only
PostgreSQL or an undecryptable spool prevents startup.

## 7. Canary and monitoring

For each stream, prove in order:

1. startup log reports the expected UUID, stream, source type, schema 2.0 for
   identities or 3.0 for economics, and
   agent version without a connection string or key;
2. first run is Sub2API reconciliation or New API full scan;
3. receiver ACK matches source, stream, batch, sequence and record count;
4. local state sequence/cursor advances and pending spool disappears;
5. a forced ACK-loss test leaves an encrypted spool and restart retries the
   exact batch ID/body;
6. New API candidates remain manual and show only `source_order_id`; an amount
   above signed `money` is rejected, one reviewer only creates a proposal, a
   distinct reviewer approves the identical tuple, and both are barred from
   issuing the resulting invoice;
7. an empty/static source still emits a signed `projection_status=healthy`
   heartbeat and advances sequence only after the exact ACK;
8. the configured reconciliation/full-scan deadline is alerted if missed;
9. `/source-agent-prod healthcheck` remains healthy and the invoice admin
   `/api/v1/admin/source-health` shows both identity streams and all eight economic streams fresh, processed,
   version-matched and projection-healthy.

Transient failures back off with jitter. Authentication, contract, oversize and
sequence conflicts stop immediately. Other repeated failures open the circuit
after `SOURCE_MAX_CONSECUTIVE_FAILURES`; the container supervisor may restart,
but alerts must not rely on restart alone.

## 8. Stop, rollback and recovery

- Stop the affected stream container only. Do not modify either upstream app.
- Preserve state, pending spool and its key together; never reset sequence to
  zero and never copy another stream's state.
- After stopping all ten agents, reject any residual `*.lock`. For each stream
  run `source-agent-prod check-state`; it is strictly offline/read-only, loads
  cursor/sequence/reconcile inventory and decrypts `pending.enc` using the
  mounted spool key. A restore is invalid unless all ten checks and both
  encrypted cutover/baseline pairs pass.
- Roll back the agent image by digest while keeping the same compatible v2
  state/spool. Run the v2 signature vector before restart.
- If a spool is present, restore the matching spool key and let the agent finish
  exact replay. Do not delete it while receiver commit status is unknown.
- A 409 requires receiver/source+stream reconciliation by an operator; blind
  retries or state-file edits are prohibited.
- Key rotation: register the new source+stream public key first, rotate the
  sender private file/key ID, retain the previous receiver key through the retry
  window, then revoke it. Rotate a spool key only when no spool exists.

## 9. Reconciliation, dependency waits and version approval

- Only a completed `full`/`reconcile` scan increments a missing-row counter.
  Pagination, query failure, process restart and incremental scans do not.
  Three consecutive complete misses are required before a signed tombstone.
- A Sub2API row whose currency is unknown makes the signed projection status
  `blocked`; visible CNY rows may refresh, but the entire scan is forbidden from
  incrementing misses or emitting tombstones. Readiness and irreversible issue
  confirmation fail closed until the projection returns `healthy`.
- Source rows may arrive before an invoice user first logs in. These events wait
  on a source-scoped HMAC dependency without consuming the eight-attempt dead
  budget or failing unrelated users/readiness. OIDC login wakes identity waits;
  a verified binding wakes that source user's payment waits.
- Funding and identity projections compare signed source time plus batch
  sequence. Older upserts are audit-only no-ops, so a late completed payment or
  binding cannot revive a refund/tombstone.
- `source_runtime_version` must equal the approved `source_instances` value on
  every batch. Stop/drain the stream (including any pending spool), update the
  source config with `expected_previous_runtime_version`, run the audited
  `bootstrap-sources` CAS, then restart. An unapproved version is rejected and
  cannot refresh heartbeat freshness.
