# Shared Rate Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.
>
> **Approval status:** This commit is RL0 docs-only. Only RL1 may be submitted for a separate
> implementation approval after RL0 review. RL2-RL4 are a locked forecast, not authorization to
> implement, migrate, configure credentials, touch staging/production, merge, or deploy.

**Goal:** Replace per-process XM-R011 accounting with a privacy-preserving PostgreSQL 18 GCRA that
enforces one quota across all Platform API replicas and fails closed when it cannot decide.

**Architecture:** A new `internal/platform/ratelimit` domain owns canonical keys, HMAC digests,
integer GCRA, and a small `Store` interface. PostgreSQL stores only policy metadata and HMAC bucket
digests; narrow owner-held functions atomically consume/prune state, while HTTP maps decisions to
429/503 and a dedicated pool bounds database pressure. Delivery is split RL1 contract-only -> RL2
disposable database -> RL3 staging shadow -> RL4 separately approved enforcement.

**Tech Stack:** Go 1.27, chi v5.3.2, pgx v5.10.0, PostgreSQL 18, River 0.45.0, PowerShell/Bash
verification, existing `SecretProvider`/`CredentialRef`.

**Spec:** `docs/superpowers/specs/2026-08-28-shared-rate-limit-design.md`

## Global Constraints

- Base design evidence is `release/v0.1-launch@543087f`; every new slice refetches and re-audits.
- PostgreSQL runtime algorithm is exact `gcra-v1`; integer microseconds only, no float tokens.
- Logical key is exact v2 tuple: key version, environment, principal type/id, HTTP method, chi route template.
- Database receives only a 32-byte HMAC-SHA256 digest; no principal, raw path, query, IP, or secret.
- PG mode policy is authoritative in `httpapi.rate_limit_policy`; env quota values are bootstrap-only.
- `429 RATE_LIMITED` means a valid quota decision; all undecidable backend/policy/key failures are
  `503 RATE_LIMIT_BACKEND_UNAVAILABLE`, `Retry-After: 1`, and never call the handler.
- Production failure mode is always closed. There is no off/auto/fail-open/degraded-local mode.
- `/healthz` remains dependency-free; `/readyz` verifies the active rate-limit backend.
- Dedicated API pool defaults and caps at 4 connections, min 0, 100ms decision deadline, and
  `application_name=platform-api-rate-limit`.
- Default quota remains 120/minute, burst 20, idle TTL 15m, cleanup interval 10m, batch 2000.
- Denied requests update `last_seen`; cleanup must never reset an actively denied bucket.
- Migration, DBR, CredentialRef, staging database, staging enforcement, and production are separate STOPs.
- DBR1 verifier must cover schema/table/routine owner, SECURITY DEFINER search_path, PUBLIC/default ACLs,
  API consume-only and worker prune-only permissions before any staging database use.
- Existing migrations are immutable; live rollback is forward-fix/application rollback, never `down`.
- RL0/RL1 may be proposed now; RL2-RL4 and all live enforcement remain NO-GO.
- No task may read or print a real credential, use an external admin DSN, merge, or deploy production.

---

## File and Slice Map

| Slice | Files/responsibility | Runtime effect |
|---|---|---|
| RL1 | `internal/platform/ratelimit/{types,gcra,key,memory}.go` + tests | None; not wired into API |
| RL2 | next `db/migrations/*_httpapi_rate_limit.*.sql`; PostgresStore; cleanup job; disposable harness; DBR policy | Disposable PG18 only |
| RL3 | API config/key provider/small pool/ready/shadow/metrics; staging runbook/topology | Staging shadow only after four approvals |
| RL4 | staging postgres enforcement evidence; production change record | Each environment separately approved |

The candidate migration number at base `543087f` is `000014`. At RL2 start, refetch the target branch.
If another migration has claimed 14, rename both uncommitted files to the next free number before review;
never edit a released migration.

---

## RL1 — Contract, Key Privacy, and Pure Reference Model

> **RL1 is the only implementation slice currently eligible to request approval.** It creates no SQL,
> pool, CredentialRef wiring, compose variable, runtime middleware change, or external network call.

### Task 1: Define domain types and integer GCRA golden model

**Files:**
- Create: `internal/platform/ratelimit/types.go`
- Create: `internal/platform/ratelimit/gcra.go`
- Test: `internal/platform/ratelimit/gcra_test.go`

**Interfaces:**
- Produces: `Policy`, `BucketKey`, `Decision`, `Store`, `GCRAState`, `Evaluate`.
- Consumes: standard library only.

- [ ] **Step 1: Write failing validation and boundary tests**

Define table tests named:

```go
func TestPolicyValidate(t *testing.T)
func TestGCRABurstIsExact(t *testing.T)
func TestGCRARefillAndRetryAfter(t *testing.T)
func TestGCRAClockRollbackDoesNotGrantCapacity(t *testing.T)
func TestGCRARejectsIntegerOverflow(t *testing.T)
```

The exact simultaneous case is `PerMinute: 1, Burst: 20`; 20 evaluations at one timestamp allow and
the 21st denies with 60 seconds. Include `Burst: 1`, sub-second interval, very large values, backward
time, and Retry-After ceil/min-one assertions.

- [ ] **Step 2: Run RED**

```powershell
go test -p 1 ./internal/platform/ratelimit -run 'Test(Policy|GCRA)' -count=1
```

Expected: FAIL because the package/types do not exist.

- [ ] **Step 3: Implement exact public domain contract**

```go
type Policy struct {
    Revision       int64
    Algorithm      string
    KeyVersion     uint32
    PerMinute      uint32
    Burst          uint32
    IdleTTL        time.Duration
    CleanupInterval time.Duration
}

type BucketKey struct {
    Environment string
    KeyVersion  uint32
    Digest      [32]byte
}

type Decision struct {
    Allowed        bool
    RetryAfter     time.Duration
    PolicyRevision int64
    ObservedAt     time.Time
}

type Store interface {
    Consume(context.Context, BucketKey) (Decision, error)
    Ready(context.Context, string, uint32) error
}
```

`Evaluate` uses:

```text
interval_us  = ceil(60_000_000 / per_minute)
tolerance_us = (burst - 1) * interval_us
now_us       = max(observed_now_us, last_seen_us)
allowed      = now_us >= tat_us - tolerance_us
```

Use checked integer arithmetic and exact `Algorithm == "gcra-v1"`. Invalid policy/state returns a
typed error, never an allow decision.

- [ ] **Step 4: Run GREEN and commit Task 1**

```powershell
go fmt ./internal/platform/ratelimit
go test -p 1 ./internal/platform/ratelimit -run 'Test(Policy|GCRA)' -count=1
git add internal/platform/ratelimit/types.go internal/platform/ratelimit/gcra.go `
  internal/platform/ratelimit/gcra_test.go
git commit -m "feat(ratelimit): define shared GCRA contract"
```

Expected: tests PASS; commit contains no caller/runtime wiring.

### Task 2: Define canonical key v2 and HMAC boundary

**Files:**
- Create: `internal/platform/ratelimit/key.go`
- Test: `internal/platform/ratelimit/key_test.go`

**Interfaces:**
- Consumes: resolved environment, `principal.Type`, principal ID, method, route template, key version.
- Produces: `CanonicalIdentity`, `CanonicalBytes`, `HMACDigest` returning `[32]byte`.

- [ ] **Step 1: Write failing canonical/privacy tests**

```go
func TestCanonicalBytesAreLengthPrefixedAndUnambiguous(t *testing.T)
func TestCanonicalBytesSeparateEnvironmentTypeMethodAndRoute(t *testing.T)
func TestCanonicalUnknownRouteUsesFixedSentinel(t *testing.T)
func TestCanonicalRejectsMissingIdentity(t *testing.T)
func TestHMACDigestIsDeterministicAndKeyed(t *testing.T)
func TestHMACDigestDoesNotContainIdentity(t *testing.T)
```

Cover colon, NUL, Unicode, empty fields, oversized fields, actual paths with different IDs, and two
different 32-byte test keys. Assert the unknown route canonical value contains
`GET <unmatched-api-route>` and never the actual URL path.

- [ ] **Step 2: Run RED**

```powershell
go test -p 1 ./internal/platform/ratelimit -run 'Test(Canonical|HMAC)' -count=1
```

- [ ] **Step 3: Implement the canonical protocol and HMAC**

```go
type CanonicalIdentity struct {
    KeyVersion   uint32
    Environment  string
    PrincipalType principal.Type
    PrincipalID  string
    Method       string
    RouteTemplate string
}
```

Encode a protocol version byte followed by each field as `uint32 big-endian length + UTF-8 bytes`.
Uppercase method, accept only validated principal types, and substitute only the fixed sentinel for
an empty route template. `HMACDigest` accepts exactly 32 decoded key bytes for v1 and uses
`hmac.New(sha256.New, key)`; it never formats the key/digest into an error.

- [ ] **Step 4: Run GREEN and commit Task 2**

```powershell
go fmt ./internal/platform/ratelimit
go test -p 1 ./internal/platform/ratelimit -run 'Test(Canonical|HMAC)' -count=1
git add internal/platform/ratelimit/key.go internal/platform/ratelimit/key_test.go
git commit -m "feat(ratelimit): define private bucket keys"
```

### Task 3: Add an unwired MemoryStore parity implementation

**Files:**
- Create: `internal/platform/ratelimit/memory.go`
- Test: `internal/platform/ratelimit/memory_test.go`
- Read-only comparison: `internal/platform/httpapi/ratelimit.go`
- Read-only comparison: `internal/platform/httpapi/ratelimit_test.go`

**Interfaces:**
- Consumes: `Policy`, `BucketKey`, injected clock.
- Produces: `MemoryStore` implementing `Store`; no HTTP dependency.

- [ ] **Step 1: Write failing Store parity and cleanup tests**

```go
func TestMemoryStoreMatchesGCRAGolden(t *testing.T)
func TestMemoryStoreKeysAreIndependent(t *testing.T)
func TestMemoryStoreDeniedTouchPreventsIdleReset(t *testing.T)
func TestMemoryStoreReadyRejectsPolicyOrKeyMismatch(t *testing.T)
func TestMemoryStoreConcurrentSameKeyIsExact(t *testing.T)
```

Use 100 goroutines against one digest with `PerMinute: 1, Burst: 20` and a fixed clock; total allows
must equal 20. Use different digests to prove independence. Keep cleanup interval/TTL semantics equal
to the design.

- [ ] **Step 2: Run RED, implement, then run GREEN**

```powershell
go test -p 1 ./internal/platform/ratelimit -run TestMemoryStore -count=1
go fmt ./internal/platform/ratelimit
go test -p 1 ./internal/platform/ratelimit -run TestMemoryStore -count=1
```

Implementation may use a mutex/map because it is a reference/rollback backend. It must call the same
`Evaluate` function and update `last_seen` on denies. Do not import `internal/platform/httpapi`, parse
env, resolve a CredentialRef, or wire it to `NewRouter`.

- [ ] **Step 3: Prove RL1 has zero runtime effect and commit**

```powershell
git diff --name-only origin/release/v0.1-launch...HEAD
rg -n "internal/platform/ratelimit" cmd internal/platform/httpapi deploy db/migrations
git add internal/platform/ratelimit/memory.go internal/platform/ratelimit/memory_test.go
git commit -m "test(ratelimit): add GCRA reference store"
```

Expected: `rg` finds no import/wiring outside the new package.

### Task 4: Run RL1 gates, request review, and STOP

- [ ] **Step 1: Run focused and full required gates**

```powershell
go fmt ./internal/platform/ratelimit
go vet ./...
go test -p 1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
git diff --check origin/release/v0.1-launch...HEAD
```

- [ ] **Step 2: Inspect secret/diff boundaries**

```powershell
git diff --stat origin/release/v0.1-launch...HEAD
git diff --name-status origin/release/v0.1-launch...HEAD
git status --short
```

Handoff must state: no SQL, migration, DB call, CredentialRef wiring, compose, runtime behavior,
staging/production, merge, or deploy. Open RL1 PR and STOP. RL1 approval does not authorize RL2.

---

## RL2 — PostgreSQL Objects and Disposable Proof (Locked Forecast)

> **NO-GO from this document.** Before any RL2 edit, RL1 must be human-merged, DBR1 verifier must
> be merged, current migration/role inventory must be refreshed, and an exact migration proposal must
> receive a named migration approval. CredentialRef and staging remain separate STOPs.

### Task 5: Refresh evidence and obtain exact migration/DBR approval

**Files:**
- Review: `db/migrations/`
- Review: `contracts/database/role-policy.v1.json` (from merged DBR1)
- Review: `internal/platform/dbroles/`
- Create in RL2 approval PR: `docs/evidence/2026-08-28-shared-rate-limit-migration-review.md`

- [ ] **Step 1: Verify branch/base and dynamic inventory read-only**

```powershell
git fetch origin
git status --short --branch
git merge-base HEAD origin/release/v0.1-launch
Get-ChildItem db/migrations -File | Sort-Object Name
go run ./cmd/db-role-verify -h
```

Do not connect to any database during this step. Record next migration number, DBR policy version,
all proposed objects/functions/grants, lock/WAL estimate, rollback path, and exact test-only PG18 digest.

- [ ] **Step 2: Present the exact proposal and STOP**

The approval artifact must name:

```text
schema: httpapi
tables: rate_limit_policy, rate_limit_bucket
routines: consume_rate_limit, rate_limit_ready, prune_rate_limit_buckets, apply_rate_limit_policy
owner: xm_migrator
api grant: EXECUTE consume + ready only
worker grant: EXECUTE prune only
lifecycle grant: EXECUTE apply-policy only
PUBLIC: no schema/table/routine privilege
```

No SQL file is created until migration and DBR reviewers explicitly approve this exact object/ACL set.

### Task 6: Create the atomic PG18 migration after approval

**Files:**
- Create: `db/migrations/000014_httpapi_rate_limit.up.sql` (or refreshed next free number)
- Create: `db/migrations/000014_httpapi_rate_limit.down.sql` (local/disposable only)
- Modify: `contracts/database/role-policy.v1.json`
- Modify/Test: `internal/platform/dbroles/policy_test.go`

**Interfaces:**
- Produces: schema/tables/index/functions and exact DBR policy objects/grants.
- Consumes: merged DBR owner/capability roles and RL1 algorithm contract.

- [ ] **Step 1: Write failing DBR policy tests before SQL**

Assert exact owner, PUBLIC denial, SECURITY DEFINER/search_path, API/worker split, table denial, and
unknown object fail-closed. Run:

```powershell
go test -p 1 ./internal/platform/dbroles -run 'Policy.*RateLimit' -count=1
```

- [ ] **Step 2: Write the migration in one transaction-safe unit**

The table contract is exact:

```sql
CREATE SCHEMA httpapi AUTHORIZATION xm_migrator;

CREATE TABLE httpapi.rate_limit_policy (
  environment text PRIMARY KEY,
  policy_revision bigint NOT NULL CHECK (policy_revision > 0),
  algorithm_version text NOT NULL CHECK (algorithm_version = 'gcra-v1'),
  key_version integer NOT NULL CHECK (key_version > 0),
  per_minute integer NOT NULL CHECK (per_minute > 0),
  burst integer NOT NULL CHECK (burst > 0),
  idle_ttl_seconds integer NOT NULL CHECK (
    idle_ttl_seconds::bigint >= greatest(
      60::bigint,
      2 * ((burst::bigint * 60 + per_minute - 1) / per_minute))),
  cleanup_interval_seconds integer NOT NULL CHECK (
    cleanup_interval_seconds > 0 AND cleanup_interval_seconds < idle_ttl_seconds),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_by text NOT NULL,
  change_ref text NOT NULL
);

CREATE TABLE httpapi.rate_limit_bucket (
  environment text NOT NULL,
  policy_revision bigint NOT NULL CHECK (policy_revision > 0),
  key_version integer NOT NULL CHECK (key_version > 0),
  key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
  tat_us bigint NOT NULL CHECK (tat_us >= 0),
  last_seen_us bigint NOT NULL CHECK (last_seen_us >= 0),
  PRIMARY KEY (environment, policy_revision, key_version, key_digest)
);

CREATE INDEX rate_limit_bucket_last_seen_idx
  ON httpapi.rate_limit_bucket (last_seen_us, environment);
```

Implement routines with schema-qualified SQL, one captured `clock_timestamp()`, checked integer math,
atomic `ON CONFLICT`, deny-touch, bounded status, and no dynamic SQL. Each function is
`SECURITY DEFINER SET search_path = pg_catalog, httpapi`; revoke PUBLIC before granting exact EXECUTE.

- [ ] **Step 3: Add local-only down and immutable-migration checks**

Down drops only the three functions, two tables, and `httpapi` schema in dependency order. It is never
used as live rollback. Run governance against the current merge base:

```powershell
$env:GOVERNANCE_BASE_REF='origin/release/v0.1-launch'
bash scripts/check-governance.sh
Remove-Item Env:GOVERNANCE_BASE_REF
```

- [ ] **Step 4: Commit migration/DBR policy only**

```powershell
git add db/migrations contracts/database/role-policy.v1.json `
  internal/platform/dbroles/policy_test.go
git commit -m "feat(ratelimit): add atomic postgres policy and buckets"
```

### Task 7: Implement PostgresStore and policy lifecycle command

**Files:**
- Create: `internal/platform/ratelimit/postgres.go`
- Test: `internal/platform/ratelimit/postgres_test.go`
- Integration test: `internal/platform/ratelimit/postgres_integration_test.go`
- Create: `cmd/rate-limit-policy/main.go`
- Test: `cmd/rate-limit-policy/main_test.go`

**Interfaces:**
- Consumes: `*pgxpool.Pool`, `BucketKey`, approved DB functions.
- Produces: `PostgresStore` implementing `Store`; optimistic-concurrency policy lifecycle command.

- [ ] **Step 1: Write failing status/error mapping tests**

```go
func TestPostgresStoreMapsAllowedLimitedAndPolicyMismatch(t *testing.T)
func TestPostgresStoreRejectsInvalidFunctionResult(t *testing.T)
func TestPostgresStoreTimeoutIsUnavailableNotLimited(t *testing.T)
func TestPolicyCommandRequiresExactExpectedRevision(t *testing.T)
```

Use a narrow query interface/fake rows. Typed error kinds are exact: policy_missing,
policy_mismatch, timeout, unreachable, permission_denied, invalid_result.

- [ ] **Step 2: Implement Store with one function call per decision**

```go
row := pool.QueryRow(ctx,
  `select status, allowed, retry_after_seconds, policy_revision, observed_at
     from httpapi.consume_rate_limit($1, $2, $3)`,
  key.Environment, key.KeyVersion, key.Digest[:])
```

Never SELECT/update bucket directly. Validate non-negative, bounded values and exact status enum.
Ready calls only `httpapi.rate_limit_ready`.

- [ ] **Step 3: Implement policy apply as Platform Lifecycle Operation**

The command accepts explicit environment, expected/new revision, per-minute, burst, TTL, interval,
key version, change ref, and updater. Initial apply requires expected=0/new=1; later applies require
new=expected+1. It calls only `httpapi.apply_rate_limit_policy`, exits nonzero on conflict, uses the
DBR `xm_lifecycle_runtime` identity with a separately approved CredentialRef, and never runs from API startup.

- [ ] **Step 4: Run focused tests and commit**

```powershell
go fmt ./internal/platform/ratelimit ./cmd/rate-limit-policy
go test -p 1 ./internal/platform/ratelimit ./cmd/rate-limit-policy -count=1
git add internal/platform/ratelimit/postgres.go internal/platform/ratelimit/postgres_test.go `
  internal/platform/ratelimit/postgres_integration_test.go cmd/rate-limit-policy
git commit -m "feat(ratelimit): add postgres store and policy lifecycle"
```

### Task 8: Add bounded River cleanup

**Files:**
- Create: `internal/platform/jobs/rate_limit_cleanup.go`
- Test: `internal/platform/jobs/rate_limit_cleanup_test.go`
- Integration test: `internal/platform/jobs/rate_limit_cleanup_integration_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify/Test: `cmd/platform-worker/config.go`
- Modify/Test: `cmd/platform-worker/config_test.go`

**Interfaces:**
- Consumes: `prune_rate_limit_buckets(2000)` via worker role.
- Produces: unique 10-minute River periodic job and bounded cleanup metrics.

- [ ] **Step 1: Write failing worker/permission behavior tests**

Assert batch=2000, unique periodic registration, retry on function failure, denied-touch survival,
concurrent `SKIP LOCKED`, and no SQL reference to `audit`/Action history.

- [ ] **Step 2: Implement the minimal job**

The worker executes only:

```sql
select deleted_count, backlog_rows, oldest_seconds
from httpapi.prune_rate_limit_buckets(2000)
```

No direct DELETE, no unbounded loop, no API goroutine. Expose cleanup interval only if policy/worker
registration needs it; DB policy remains authoritative for TTL.

- [ ] **Step 3: Run tests and commit**

```powershell
go fmt ./internal/platform/jobs ./cmd/platform-worker
go test -p 1 ./internal/platform/jobs ./cmd/platform-worker -run RateLimit -count=1
git add internal/platform/jobs/rate_limit_cleanup* internal/platform/jobs/client.go `
  cmd/platform-worker/config.go cmd/platform-worker/config_test.go
git commit -m "feat(ratelimit): prune idle buckets with River"
```

### Task 9: Prove two-replica concurrency, faults, privacy, and real ACLs

**Files:**
- Create: `tests/security/shared-rate-limit.compose.yaml`
- Create: `scripts/test-shared-rate-limit.ps1`
- Extend: `internal/platform/ratelimit/postgres_integration_test.go`
- Extend: `internal/platform/jobs/rate_limit_cleanup_integration_test.go`

**Interfaces:**
- Consumes: locked PG18 digest, DBR1 disposable harness and approved migration.
- Produces: machine-readable evidence with exact cleanup and no external DB access.

- [ ] **Step 1: Build a self-owned disposable harness**

The PowerShell script generates a random compose project/database suffix, refuses non-loopback or external
DSNs, starts its own PG18 container, creates test-only roles, applies migrations, and always removes its
exact containers/volume/roles in `finally`. It never accepts a staging/production DSN.

- [ ] **Step 2: Run two independent pools as two API replicas**

Test names and assertions:

```go
func TestPostgresStoreTwoReplicasShareExactBurst(t *testing.T)     // 100 then 1000, total=20
func TestPostgresStoreDifferentKeysDoNotShareLock(t *testing.T)    // each total=20
func TestPostgresStoreAppClockSkewIsIrrelevant(t *testing.T)
func TestPostgresStoreFutureLastSeenBlocksClockRollbackGrant(t *testing.T)
func TestPostgresStoreFailureReturnsUnavailable(t *testing.T)
```

Use separate pgx pools. Do not expose a production `now` parameter; owner-only fixtures may seed
future `last_seen_us` to test rollback.

- [ ] **Step 3: Run real-role negative probes**

API login: consume/ready PASS; table SELECT/DML, prune, CREATE/ALTER/DROP/TRUNCATE/GRANT fail with
SQLSTATE 42501. Worker login: prune PASS; consume/table DML/DDL fail. PUBLIC routine EXECUTE fails.
Verifier proves owner/search_path/default ACL. Query raw table bytes and prove test principal/route text is absent.

- [ ] **Step 4: Run backend fault and cleanup races**

Terminate pool connections, pause/stop the disposable PG, revoke test role EXECUTE, delete test policy,
hold pool connections past 100ms, and run cleanup while denies continue. Every undecidable request maps
to unavailable; active bucket is never deleted/reset.

- [ ] **Step 5: Run gates, commit, and STOP before staging**

```powershell
pwsh -File scripts/test-shared-rate-limit.ps1
go test -p 1 ./internal/platform/ratelimit ./internal/platform/jobs -run RateLimit -count=1
go vet ./...
go test -p 1 ./...
bash scripts/check-governance.sh
git add tests/security/shared-rate-limit.compose.yaml scripts/test-shared-rate-limit.ps1 `
  internal/platform/ratelimit/postgres_integration_test.go `
  internal/platform/jobs/rate_limit_cleanup_integration_test.go
git commit -m "test(ratelimit): prove shared postgres enforcement"
```

Handoff says disposable only. Stop for fresh migration/DBR/CredentialRef/staging review. Do not configure
a real ref or alter `deploy/compose/launch.yaml` in RL2.

---

## RL3 — Runtime Wiring and Staging Shadow (Locked Forecast)

> **NO-GO from this document.** Requires merged RL2, DBR2 owner/lifecycle and DBR3 restricted API/worker
> roles, separately approved migration execution, human-configured CredentialRef, and named staging approval.

### Task 10: Wire config, CredentialRef key source, dedicated pool, and readiness

**Files:**
- Create: `cmd/platform-api/ratelimit.go`
- Test: `cmd/platform-api/ratelimit_test.go`
- Modify: `cmd/platform-api/config.go`
- Modify: `cmd/platform-api/config_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/httpapi/health.go`
- Modify: `internal/platform/httpapi/health_test.go`
- Modify: `internal/platform/httpapi/router.go`

**Interfaces:**
- Consumes: existing database CredentialRef path, new HMAC CredentialRef, `ratelimit.Store`.
- Produces: memory/shadow/postgres backend assembly and composite Ready checker.

- [ ] **Step 1: Write fail-closed config/key tests**

Exact cases: missing/unknown backend, off/auto/open rejected, the RL4 production-wired binary accepting
only postgres in `ENVIRONMENT=production`, shadow/postgres missing ref/key version rejected, HMAC material not strict unpadded base64url decoding
to exactly 32 bytes rejected, timeout <=0 or >= request timeout rejected, max conns !=1..4 rejected, secret
value absent from logs/errors.

- [ ] **Step 2: Build a CredentialRef-backed key source**

`XM_RATE_LIMIT_HMAC_KEY_REF` contains only `secret://...`; env Provider maps it to
`XM_RATE_LIMIT_HMAC_KEY`. Resolve with purpose `platform-api rate-limit bucket HMAC`, decode strict
base64url without padding to 32 bytes, construct Keyer, and never retain/refmt the raw string after init.

- [ ] **Step 3: Build the dedicated pool**

Clone/parse the already validated API DSN, set runtime `application_name`, MaxConns 4, MinConns 0, and
use a 100ms per-consume context. Do not mutate or share the normal Query pool config. Startup/pool errors
are bounded and secret-free.

- [ ] **Step 4: Make readiness composite without changing health**

Add a minimal checker interface so `/readyz` runs DB Ping plus active backend `Ready`. In postgres/shadow,
verify policy/algorithm/key/function/grant. Keep `/healthz` byte-compatible and probes outside `/api/v1`.

- [ ] **Step 5: Run tests and commit**

```powershell
go fmt ./cmd/platform-api ./internal/platform/httpapi
go test -p 1 ./cmd/platform-api ./internal/platform/httpapi -run 'RateLimit|Ready|Health' -count=1
git add cmd/platform-api internal/platform/httpapi/health.go `
  internal/platform/httpapi/health_test.go internal/platform/httpapi/router.go
git commit -m "feat(ratelimit): assemble private shared backend"
```

### Task 11: Replace HTTP internals with Store decisions and add shadow telemetry

**Files:**
- Modify: `internal/platform/httpapi/ratelimit.go`
- Modify: `internal/platform/httpapi/ratelimit_test.go`
- Create: `internal/platform/ratelimit/shadow.go`
- Test: `internal/platform/ratelimit/shadow_test.go`
- Create: `internal/platform/ratelimit/observe.go`
- Test: `internal/platform/ratelimit/observe_test.go`
- Modify: `cmd/platform-api/ratelimit.go`

**Interfaces:**
- Consumes: resolved principal, fixed route identity, Keyer, MemoryStore/PostgresStore.
- Produces: exact 429/503 responses and bounded metrics/logs.

- [ ] **Step 1: Write HTTP error/privacy tests first**

```go
func TestRateLimitBackendFailureReturns503AndDoesNotCallHandler(t *testing.T)
func TestRateLimitPolicyMismatchReturns503AndDoesNotCallHandler(t *testing.T)
func TestRateLimitMissingPrincipalFailsClosed(t *testing.T)
func TestRateLimitUnknownRouteNeverUsesRawPath(t *testing.T)
func TestRateLimitProbesRemainExempt(t *testing.T)
func TestRateLimitLogsContainNoPrincipalDigestOrRawPath(t *testing.T)
```

Preserve existing 429 body and calculated Retry-After. New 503 code is exact
`RATE_LIMIT_BACKEND_UNAVAILABLE`, Retry-After `1`.

- [ ] **Step 2: Wire Store without embedding SQL/algorithm in HTTP**

HTTP constructs `CanonicalIdentity`, HMACs it, calls `Store.Consume`, then maps typed result. It does not
read TAT, policy values, SQLSTATE strings, or fallback to memory after a Postgres error.

- [ ] **Step 3: Add shadow comparison**

Shadow evaluates both stores but MemoryStore alone determines response. PG errors/disagreements increment
bounded metrics and make readiness unavailable; no principal/digest/raw path enters logs or labels.

- [ ] **Step 4: Add exact bounded signals and cardinality tests**

Cover decisions, duration, backend error kind, retry seconds, pool acquire, shadow disagreement,
clock rollback, cleanup. Base has no Prometheus/OpenTelemetry exporter: implement a fixed-bucket,
atomic `Observer` snapshot and emit one `rate_limit_summary` structured log every 60 seconds. Dimension
values are enum plus registered route template only; do not add a dependency or per-allow request log.

- [ ] **Step 5: Run tests and commit**

```powershell
go fmt ./internal/platform/httpapi ./internal/platform/ratelimit
go test -p 1 ./internal/platform/httpapi ./internal/platform/ratelimit -run RateLimit -count=1
git add internal/platform/httpapi/ratelimit.go internal/platform/httpapi/ratelimit_test.go `
  internal/platform/ratelimit/shadow.go internal/platform/ratelimit/shadow_test.go `
  internal/platform/ratelimit/observe.go internal/platform/ratelimit/observe_test.go `
  cmd/platform-api/ratelimit.go
git commit -m "feat(ratelimit): enforce fail-closed HTTP decisions"
```

### Task 12: Prepare and execute separately approved staging shadow

**Files:**
- Create: `docs/runbooks/SWITCH-SHARED-RATE-LIMIT.md`
- Modify only after staging approval: `deploy/compose/launch.yaml`
- Modify only after staging approval: `deploy/compose/.env.example`
- Create if required by approved topology: `deploy/compose/shared-rate-limit-staging.override.yaml`

- [ ] **Step 1: Write the runbook and exact topology diff, then STOP**

The runbook contains preflight, DBR verifier, migration/bootstrap change ref, secret ref names only,
two-API topology, current/target backend, per-instance build/application_name/policy evidence, ready checks,
shadow metrics, enforcement gate, rollback to exact one memory replica, and recovery. It contains no real value.

- [ ] **Step 2: Obtain four independent approvals**

Required names in Handoff: migration execution, DBR role/grants, CredentialRef provisioning, staging topology/shadow.
Any missing approval stops before compose/database changes.

- [ ] **Step 3: Human provisions secret; authorized operator applies migration/bootstrap**

Codex verifies only redacted status/current_user/application_name/policy revision/ACL evidence. It never reads
the secret value or runs a production command.

- [ ] **Step 4: Start two staging API replicas in shadow and soak**

Collect: decision p50/p95/p99, timeout/lock/acquire %, DB CPU/IO/WAL deltas, sustained rps, hot-key isolation,
cleanup backlog, shadow disagreements, app-clock skew, DB restart/latency, ready removal/recovery. Verify
MemoryStore remains response authority and no replica reports postgres enforcement.

- [ ] **Step 5: Evaluate Redis thresholds and commit artifacts**

If any spec §16 threshold fires, mark RL4 BLOCKED and start a new ADR. Otherwise commit only reviewed
runbook/config/topology changes and redacted evidence; open RL3 PR and STOP. Production remains untouched.

---

## RL4 — Enforcement and Production Change (Locked Forecast)

> **Current status: NO-GO.** Staging enforcement and production enforcement are two separate human STOPs.

### Task 13: Switch staging to Postgres enforcement and prove rollback

- [ ] **Step 1: Review RL3 soak and assert no Redis threshold fired**

Every metric/decision comes from a bounded evidence artifact. Missing data is a stop, not a pass.

- [ ] **Step 2: Obtain named staging enforcement approval**

Approval names build digest, migration/policy revision, key version/ref name, DB roles, replica count,
window, success metrics, abort thresholds, and rollback operator.

- [ ] **Step 3: Prevent mixed backend before traffic**

Drain each instance, set postgres, verify ready/backend/build/policy, and only then return it to service.
The inventory must show zero memory/shadow instance before enforcement test begins.

- [ ] **Step 4: Run staging behavior/fault/rollback suite**

Prove two replicas share one burst; 429/Retry-After exact; DB stop/timeout/permission loss gives 503 and
ready removal; health remains live; recovery returns ready; cleanup does not reset active denies. Then
exercise rollback: drain to one replica, switch that replica memory, prove all others stopped, and restore
postgres through the same gate.

- [ ] **Step 5: Publish redacted evidence and STOP**

Do not infer production approval from staging success.

### Task 14: Execute a separately governed production change

- [ ] **Step 1: Create the production change record**

It references Task/PR/CI/build, DBR verifier, backup/restore, RL3/RL4 staging evidence, exact policy/key
versions, capacity headroom, monitoring, abort thresholds, rollback steps, operator/approver separation.

- [ ] **Step 2: Obtain production migration/credential/deployment approvals**

Approvals are explicit and current. Codex does not handle real values, merge, SSH, or deploy.

- [ ] **Step 3: Human operator performs canary/rolling cutover**

At every instance boundary verify postgres backend, restricted current_user, expected application_name,
ready, policy/key version, and no mixed memory enforcement. Any mismatch aborts and drains the instance.

- [ ] **Step 4: Observe and close**

Watch 429/503, latency, pool/locks, DB/WAL, cleanup, ready, upstream platform SLO, and external watchdog.
Close only after rollback window expires and all evidence is linked. No automatic merge/deploy is added.

---

## Final Verification Before Any Slice Completion

Run only commands appropriate to the approved slice; never substitute a skipped external gate with prose.

```powershell
go fmt ./internal/platform/ratelimit ./internal/platform/httpapi ./internal/platform/jobs `
  ./cmd/platform-api ./cmd/platform-worker
go vet ./...
go test -p 1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
git diff --check origin/release/v0.1-launch...HEAD
git status --short
```

Each Handoff lists tests run/not run, exact base/head, schema/role/policy/key versions without secrets,
rollback evidence, unverified external facts, approvals, risks, and next STOP. Human merge remains mandatory.

## Approval Summary

- **RL0:** docs-only, may be approved now.
- **RL1:** may request a separate contract/reference-model approval after RL0 review.
- **RL2:** NO-GO until fresh DBR1 + exact migration approval; no staging.
- **RL3:** NO-GO until migration/DBR/CredentialRef/staging approvals all exist.
- **RL4 staging enforcement:** NO-GO until shadow evidence and separate approval.
- **RL4 production:** NO-GO until staging/rollback/recovery evidence and a separate production change.
- **Live enforcement today:** **NO-GO**.
