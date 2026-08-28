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
- Canonical format is exact `0x02 || u32be(key_version) ||` five length-prefixed UTF-8 segments;
  format version and HMAC key version are separate domains and fixed golden bytes are normative.
- Database receives only a 32-byte HMAC-SHA256 digest; no principal, raw path, query, IP, or secret.
- PG mode policy is authoritative in `httpapi.rate_limit_policy`; env quota values are bootstrap-only.
- `429 RATE_LIMITED` means a valid quota decision; all undecidable backend/policy/key failures are
  `503 RATE_LIMIT_BACKEND_UNAVAILABLE`, `Retry-After: 1`, and never call the handler.
- Failure mode is always closed in code. There is no failure-mode variable or off/auto/fail-open/degraded-local mode.
- `/healthz` remains dependency-free; `/readyz` verifies the active rate-limit backend.
- Dedicated API pool defaults and caps at 4 connections, min 0, 100ms decision deadline, and
  `application_name=platform-api-rate-limit`.
- Before replica expansion, fleet connection maxima plus a `max(10,20%)` DB reserve must fit
  `max_connections`; unknown pgx defaults are measured, never counted as zero.
- Default quota remains 120/minute, burst 20, idle TTL 15m, cleanup interval 10m, batch 2000.
- Denied requests update `last_seen`; cleanup must never reset an actively denied bucket.
- Migration, DBR, CredentialRef, staging database, staging enforcement, and production are separate STOPs.
- DBR1 verifier must cover schema/table/routine owner, SECURITY DEFINER exact
  `search_path=pg_catalog,httpapi,pg_temp`, httpapi CREATE denial, PUBLIC/default ACLs,
  API consume-only and worker prune-only permissions before any staging database use.
- Existing migrations are immutable; live rollback is forward-fix/application rollback, never `down`.
- RL0/RL1 may be proposed now; RL2-RL4 and all live enforcement remain NO-GO.
- No task may read or print a real credential, use an external admin DSN, merge, or deploy production.
- RL2 reads a human-approved `DBR_APPROVED_MERGE_SHA`, proves it is a target-base ancestor, and reuses
  the hardened self-owned-cluster harness evidenced at DBR `7019230`; it creates no competing harness.

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

type ReadyState struct {
    Environment      string
    PolicyRevision   int64
    Algorithm        string
    ActiveKeyVersion uint32
}

type Store interface {
    Consume(context.Context, BucketKey) (Decision, error)
    Ready(context.Context, string) (ReadyState, error)
}
```

`Evaluate` uses:

```text
interval_us  = ceil(60_000_000 / per_minute)
tolerance_us = (burst - 1) * interval_us
now_us       = max(observed_now_us, last_seen_us)
allowed      = now_us >= tat_us - tolerance_us
```

Use checked integer arithmetic and exact `Algorithm == "gcra-v1"`. Although Go fields are `uint32`,
validation caps every SQL-backed integer at `math.MaxInt32`, validates whole-second TTL/interval and all
ceil/multiply/add conversions. Invalid/overflowing policy/state returns a typed error, never an allow decision.

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
different 32-byte test keys. Assert the unknown route field is exact `<unmatched-api-route>` and never
contains the actual URL path.

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

Implement the spec byte-for-byte: leading `0x02`, then four-byte `u32be(KeyVersion)`, then environment,
principal type, principal ID, uppercase method and route as `u32be(UTF8 byte length)||UTF8 bytes`.
Enforce the per-segment/16 KiB aggregate limits, no Unicode normalization, exact principal enums and
ASCII method token. Pin the spec canonical hex and HMAC digest (`00..1f` test key) as golden vectors.
`HMACDigest` accepts exactly 32 decoded key bytes and never formats key/digest into an error.

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
- Dynamically review: `contracts/database/role-policy.v${DBR_CURRENT_POLICY_VERSION}.json` from the approved DBR merge
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

Do not connect to any database during this step. Read `DBR_APPROVED_MERGE_SHA` and
`DBR_CURRENT_POLICY_VERSION` from the approved CR/Task；prove the SHA is a target-base ancestor and
the current policy file declares that exact version. Record next migration/policy versions, all proposed
objects/functions/grants, lock/WAL estimate, rollback path, and exact test-only PG18 digest. Unknown,
mismatch, missing current file or pre-existing next version is STOP.

- [ ] **Step 2: Present the exact proposal and STOP**

The approval artifact must name:

```text
schema: httpapi
tables: rate_limit_policy, rate_limit_bucket
routines (exact input signatures):
  consume_rate_limit(text, integer, bytea)
  rate_limit_ready(text)
  prune_rate_limit_buckets(text, integer)
  bootstrap_rate_limit_policy(text, bigint, bigint, text, integer, integer, integer, integer, integer, text, text)
owner: xm_migrator
api grant: EXECUTE consume + ready only
worker grant: EXECUTE prune only
lifecycle grant: EXECUTE bootstrap-policy only (expected=0,new=1, insert-only)
PUBLIC: no schema/table/routine privilege
```

No SQL file is created until migration and DBR reviewers explicitly approve this exact object/ACL set.

### Task 6: Create the atomic PG18 migration after approval

**Files:**
- Create: `db/migrations/000014_httpapi_rate_limit.up.sql` (or refreshed next free number)
- Create: `db/migrations/000014_httpapi_rate_limit.down.sql` (local/disposable only)
- Dynamically create: `contracts/database/role-policy.v${DBR_NEXT_POLICY_VERSION}.json`
- Modify/Test: `internal/platform/dbroles/policy_test.go`

**Interfaces:**
- Produces: schema/tables/index/functions and exact DBR policy objects/grants.
- Consumes: merged DBR owner/capability roles and RL1 algorithm contract.

- [ ] **Step 1: Write failing DBR policy tests before SQL**

Assert the approved current policy remains byte-unchanged；next=current+1 adds only exact rate-limit
objects/grants. Also assert exact owner, PUBLIC denial, SECURITY DEFINER/search_path, API/worker split,
table denial, unknown object/version fail-closed. Run:

```powershell
go test -p 1 ./internal/platform/dbroles -run 'Policy.*RateLimit' -count=1
```

- [ ] **Step 2: Write the migration in one transaction-safe unit**

The table contract is exact:

```sql
CREATE SCHEMA httpapi AUTHORIZATION xm_migrator;

CREATE TABLE httpapi.rate_limit_policy (
  environment text PRIMARY KEY REFERENCES core.environment(id) ON DELETE RESTRICT,
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
  updated_by text NOT NULL CHECK (octet_length(updated_by) BETWEEN 1 AND 256),
  change_ref text NOT NULL CHECK (octet_length(change_ref) BETWEEN 1 AND 256),
  applied_by_db_role text NOT NULL
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
  ON httpapi.rate_limit_bucket (environment, last_seen_us);
```

Implement four routines with schema-qualified SQL, one captured `clock_timestamp()`, checked integer math,
atomic `ON CONFLICT`, deny-touch, bounded status, and no dynamic SQL. Each routine is
`SECURITY DEFINER SET search_path = pg_catalog, httpapi, pg_temp`; verifier also proves exact `proconfig`
and that only migrator can CREATE in httpapi. Bootstrap derives `applied_by_db_role=session_user`, accepts
only expected=0/new=1, and cannot update. Revoke PUBLIC before granting exact per-signature EXECUTE.

- [ ] **Step 3: Add local-only down and immutable-migration checks**

Down names and drops all four exact routine signatures, two tables, index and `httpapi` schema in dependency
order. Disposable proof runs up→down→up and asserts the schema is empty after down and all ACLs return after
the second up. It is never used as live rollback. Run governance against the current merge base:

```powershell
$env:GOVERNANCE_BASE_REF='origin/release/v0.1-launch'
bash scripts/check-governance.sh
Remove-Item Env:GOVERNANCE_BASE_REF
```

- [ ] **Step 4: Commit migration/DBR policy only**

```powershell
git add db/migrations contracts/database/role-policy.v$env:DBR_NEXT_POLICY_VERSION.json `
  internal/platform/dbroles/policy_test.go
git commit -m "feat(ratelimit): add atomic postgres policy and buckets"
```

### Task 7: Implement PostgresStore and insert-only policy bootstrap command

**Files:**
- Create: `internal/platform/ratelimit/postgres.go`
- Test: `internal/platform/ratelimit/postgres_test.go`
- Integration test: `internal/platform/ratelimit/postgres_integration_test.go`
- Create: `contracts/ops/rate-limit-fleet-manifest.v1.schema.json`
- Create: `contracts/ops/rate-limit-fleet-keyring.v1.json`
- Create: `internal/platform/ratelimit/fleet_manifest.go`
- Test: `internal/platform/ratelimit/fleet_manifest_test.go`
- Create: `cmd/rate-limit-policy/main.go`
- Test: `cmd/rate-limit-policy/main_test.go`

**Interfaces:**
- Consumes: `*pgxpool.Pool`, `BucketKey`, approved DB functions.
- Produces: `PostgresStore` implementing `Store`; revision-1-only lifecycle bootstrap command.

```go
type FleetReplica struct {
    ReplicaID, BuildDigest, Backend, ProcessEnvironment string
    EffectivePerMinute, EffectiveBurst int32
}
type SignedFleetManifest struct {
    Kind string
    Version int
    Environment, FleetGeneration, ChangeRef string
    Complete bool
    GeneratedAt, ValidUntil time.Time
    ExpectedReplicaCount int
    Replicas []FleetReplica
    PayloadSHA256, SignatureKeyID, Signature string
}
type TrustedFleetKey struct {
    KeyID, Algorithm, PublicKey, Fingerprint, Purpose string
    ValidFrom, ValidUntil time.Time
    RevokedAt *time.Time
}
type TrustedFleetKeyring interface {
    Lookup(string, string, time.Time) (TrustedFleetKey, error)
}
func VerifyFleetManifest([]byte, TrustedFleetKeyring, time.Time) (SignedFleetManifest, error)
```

- [ ] **Step 1: Write failing status/error mapping tests**

```go
func TestPostgresStoreMapsAllowedLimitedAndPolicyMismatch(t *testing.T)
func TestPostgresStoreRejectsInvalidFunctionResult(t *testing.T)
func TestPostgresStoreTimeoutIsUnavailableNotLimited(t *testing.T)
func TestPolicyCommandRequiresExactExpectedRevision(t *testing.T)
func TestFleetManifestRejectsBadSignatureEmbeddedKeyExpiredOrIncomplete(t *testing.T)
func TestFleetManifestRejectsWrongPurposeDomainFingerprintAndRevokedKey(t *testing.T)
func TestFleetManifestCanonicalBytesAndSignatureMatchGolden(t *testing.T)
func TestFleetManifestRejectsMissingDuplicateUnknownOrStaleReplica(t *testing.T)
func TestFleetManifestRejectsBuildEnvironmentBackendOrQuotaMismatch(t *testing.T)
func TestPolicyCommandNeverCallsBootstrapWhenFleetManifestInvalid(t *testing.T)
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

- [ ] **Step 3: Implement revision-1 bootstrap as Platform Lifecycle Operation**

The command accepts explicit environment, expected/new revision, TTL, interval, key version, updater,
`--fleet-manifest`, `--fleet-keyring`, and `--expected-manifest-sha256`, but only expected=0/new=1.
Per-minute/burst/change-ref come from the verified manifest and must equal any separately approved CLI
assertions. It validates Ed25519 signature against the independent keyring, canonical payload hash,
kind/version/environment/change-ref, validity window, `complete=true`, exact count/unique IDs, approved
build digest, memory backend and one equal quota. Missing/unknown/stale/mismatch exits before acquiring a
write transaction or calling `bootstrap_rate_limit_policy`. On success it verifies the inserted row against
the manifest plus DB-derived `session_user`, exits nonzero if any policy exists, uses DBR lifecycle identity,
and never runs from API startup. Revision 2+ still needs separate Action + append-only history.

- [ ] **Step 4: Run focused tests and commit**

```powershell
go fmt ./internal/platform/ratelimit ./cmd/rate-limit-policy
go test -p 1 ./internal/platform/ratelimit ./cmd/rate-limit-policy -count=1
git add internal/platform/ratelimit/postgres.go internal/platform/ratelimit/postgres_test.go `
  internal/platform/ratelimit/postgres_integration_test.go cmd/rate-limit-policy
git commit -m "feat(ratelimit): add postgres store and policy bootstrap"
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
- Consumes: `prune_rate_limit_buckets(environment, 2000)` via worker role.
- Produces: unique 10-minute River periodic job and bounded cleanup metrics.

- [ ] **Step 1: Write failing worker/permission behavior tests**

Assert environment arg, batch=2000, unique kind+args+queue+period registration, retry/at-least-once
idempotency, denied-touch survival, concurrent `SKIP LOCKED`, no audit/Action reference, and:

```go
func TestPruneReturnsHasMoreAndBacklogCappedAtBatchPlusOne(t *testing.T)
func TestPruneRuntimePathNeverRunsExactCount(t *testing.T)
func TestExactBacklogCountRequiresSeparateLowFrequencyApprovedDiagnostic(t *testing.T)
```

- [ ] **Step 2: Implement the minimal job**

The worker executes only:

```sql
select deleted_count, has_more, backlog_capped, oldest_seconds
from httpapi.prune_rate_limit_buckets($1, 2000)
```

No direct DELETE, no unbounded loop, no API goroutine. Candidate selection reads at most batch+1；
`backlog_capped<=2001` and `has_more` replace exact runtime count. Current environment policy TTL applies
to old revisions/key versions. Exact `count(*)` is a separately approved low-frequency read-only diagnostic
under migrator/approved ops evidence identity, never part of routine/job/request. A 1,000,000-row disposable
fixture plus `EXPLAIN (ANALYZE, BUFFERS)` proves `(environment,last_seen_us)` and bounded scan.

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
- Extend/reuse after DBR merge: `tests/security/database-role-cluster.compose.yaml`
- Extend/reuse after DBR merge: `scripts/test-database-roles.ps1`
- Create: `scripts/test-shared-rate-limit.ps1`
- Extend: `internal/platform/ratelimit/postgres_integration_test.go`
- Extend: `internal/platform/jobs/rate_limit_cleanup_integration_test.go`

**Interfaces:**
- Consumes: locked PG18 digest, DBR1 disposable harness and approved migration.
- Produces: machine-readable evidence with exact cleanup and no external DB access.

- [ ] **Step 1: Reuse the approved self-owned disposable harness**

Read `DBR_APPROVED_MERGE_SHA` from the approved CR/Task, prove it is an ancestor of the target base and its
harness contains the hardening evidenced at `7019230`. Reuse that DBR1 harness: it accepts no external admin
DSN, creates an exclusive random Compose project/container/network/volume/database from the exact PG18
RepoDigest, uses fixed production role names/exact SQL, verifies labels/version/fresh fingerprint, and tears
down the whole labeled project in `finally`. Do not create a second harness or random/test-only role namespace.

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
git add tests/security/database-role-cluster.compose.yaml scripts/test-database-roles.ps1 `
  scripts/test-shared-rate-limit.ps1 `
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

Exact cases: missing backend only defaults to memory in development; staging/production missing and every
unknown/off/auto/open value reject; there is no failure-mode variable. Test
`principal.Environment == process ENVIRONMENT == ReadyState.Environment == DB policy.environment`
and HMAC ref scope environment equality. Test a
keyring of exactly one primary plus at most one staged slot: paired ref/version, positive int32, distinct
version/ref, DB-selected active version present; strict unpadded base64url decoding to exactly 32 bytes;
timeout <=0 or >= request timeout; max conns !=1..4; and secret value absent from logs/errors/audit.

```go
func TestRateLimitPrimaryAndStagedSecretSuccessAndFailureAreAudited(t *testing.T)
func TestRateLimitSecretAuditHasExactCallerPurposeEnvironmentRefProviderAndCode(t *testing.T)
func TestRateLimitSecretAuditLogsAndErrorsContainNoMaterialOrDigest(t *testing.T)
```

- [ ] **Step 2: Build a CredentialRef-backed key source**

Primary and optional staged ref contain only `secret://...` and their scope/name environment must equal
`cfg.Environment`. Wrap the mapped provider with
`secrets.NewAudited(inner, recorder, cfg.Environment)`, create resolve context with
`secrets.WithCaller(ctx,"api:platform")`, then call
`Resolve(resolveCtx, ref,"platform-api rate-limit bucket HMAC")` for each slot. Tests cover primary/staged
success and failure and assert AccessRecord caller/purpose/environment/ref/provider/success/error code while
excluding material. Resolve/decode both slots at startup, retain only bounded `{version,[32]byte}`, and let DB policy select active.
Never format material/digest in errors; tests prove success/failure audit contains only bounded kind/ref metadata.
This only prepares the bounded keyring. Rotation itself remains blocked until a separate policy Action plus
append-only history exists; its runbook must quiesce the full fleet, switch DB revision/version once, verify all
replicas select the staged slot, restore ingress once, then withdraw the old slot after an observation window.

- [ ] **Step 3: Build the dedicated pool**

Clone/parse the already validated API DSN, set runtime `application_name`, MaxConns 4, MinConns 0, and
use a 100ms context covering acquire+function+decode, capped by the parent deadline. Do not mutate/share the
normal pool config. Before two replicas, inventory all pool maxima and prove the spec fleet formula plus reserve
against DB `max_connections`; unknown defaults STOP. Startup/pool errors are bounded and secret-free.

- [ ] **Step 4: Make readiness composite without changing health**

Add a minimal checker interface so `/readyz` runs DB Ping plus active backend `Ready`. `ReadyState` returns
DB environment；require it equal configured process environment before atomically publishing the snapshot.
In postgres/shadow, verify policy/algorithm, DB-selected keyring slot, staged parse state, function/grant. Keep `/healthz`
byte-compatible and probes outside `/api/v1`.

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
func TestRateLimitPrincipalProcessAndDBEnvironmentMismatchFailsClosed(t *testing.T)
func TestRateLimitHMACRefScopeEnvironmentMismatchFailsClosed(t *testing.T)
func TestRateLimitUnknownRouteNeverUsesRawPath(t *testing.T)
func TestRateLimitProbesRemainExempt(t *testing.T)
func TestRateLimitLogsContainNoPrincipalDigestOrRawPath(t *testing.T)
```

Environment mismatch tests assert HMAC/Store.Consume/handler call counts all remain zero. Preserve existing
429 body and calculated Retry-After. New 503 code is exact
`RATE_LIMIT_BACKEND_UNAVAILABLE`, Retry-After `1`.

- [ ] **Step 2: Wire Store without embedding SQL/algorithm in HTTP**

HTTP constructs `CanonicalIdentity`, HMACs it, calls `Store.Consume`, then maps typed result. It does not
read TAT, policy values, SQLSTATE strings, or fallback to memory after a Postgres error.

- [ ] **Step 3: Add shadow comparison**

Shadow evaluates both stores but MemoryStore alone determines response. Live differences first increment
`unclassified`; middleware must not infer cause from one request. Only the controlled one-replica ordered parity
lane may emit `algorithm_mismatch`, and only the controlled two-replica aggregate trace may emit
`expected_consolidation`. Any algorithm mismatch or unclosed unclassified difference blocks RL4.

- [ ] **Step 4: Add exact bounded signals and cardinality tests**

Cover decisions, duration, backend error kind, retry seconds, pool acquire, raw shadow difference and the
three bounded classifications,
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
two-API topology, fleet connection budget, every replica's effective memory quota, DB revision-1 equality,
per-instance build/application_name/policy/keyring evidence, removal of legacy quota env vars after postgres
activation, ready checks, classified shadow metrics, fleet-wide quiesce gate, rollback to exact one memory
replica with explicit quota, and recovery. It contains no real value.

- [ ] **Step 2: Obtain four independent approvals**

Required names in Handoff: migration execution, DBR role/grants, CredentialRef provisioning, staging topology/shadow.
Any missing approval stops before compose/database changes.

- [ ] **Step 3: Human provisions secret; authorized operator applies migration/bootstrap**

Codex verifies only redacted status/current_user/application_name/policy revision/ACL evidence. It never reads
the secret value or runs a production command.

- [ ] **Step 4: Start two staging API replicas in shadow and soak**

Run paired memory baseline and shadow with identical replay/replica count: each 30m warm-up + 60m measurement,
at least 100k decisions and 10k per top route; then at least 24h ambient soak. Aggregate 60s snapshots across
the fleet; “sustained” means five consecutive complete 1m windows and ratios use fleet decisions as denominator.
Collect p50/p95/p99, timeout/lock/acquire %, paired DB CPU/IO/WAL deltas, rps, hot/multi-key isolation,
cleanup, classified differences, clock skew, DB restart/latency and ready recovery. Missing samples,
algorithm mismatch, unclosed unclassified results or failed connection budget block RL4.

- [ ] **Step 5: Evaluate Redis thresholds and commit artifacts**

If any spec §16 threshold fires, mark RL4 BLOCKED and start a new ADR. Otherwise commit only reviewed
runbook/config/topology changes and redacted evidence; open RL3 PR and STOP. Production remains untouched.

---

## RL4 — Enforcement and Production Change (Locked Forecast)

> **Current status: NO-GO.** Staging enforcement and production enforcement are two separate human STOPs.

### Task 13: Quiesce staging fleet, switch to Postgres enforcement, and prove rollback

- [ ] **Step 1: Review RL3 soak and assert no Redis threshold fired**

Every metric/decision comes from a bounded evidence artifact. Missing data is a stop, not a pass.

- [ ] **Step 2: Obtain named staging enforcement approval**

Approval names build digest, migration/policy revision, key version/ref name, DB roles, replica count,
window, success metrics, abort thresholds, and rollback operator.

- [ ] **Step 3: Prevent mixed backend with fleet-wide quiesce**

Block ingress, drain **all** API replicas, switch and validate every replica offline, then require one inventory
showing the same build/backend/revision/key version and zero serving memory/shadow instance. Only after the
full fleet passes is ingress restored once. No instance returns early; no rolling/canary activation.

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

- [ ] **Step 3: Human operator performs fleet-wide quiesced activation**

Binary/schema-compatible preparation may be rolling while enforcement stays unchanged. Activation blocks
ingress and drains the full fleet, switches all replicas, verifies restricted current_user, application_name,
ready, policy/key version and zero mixed backend, then restores ingress once. Any mismatch keeps ingress
blocked and executes the approved all-fleet rollback; there is no enforcement canary.

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

## RL0 Round-1 Review Closure Matrix

| ID | Review item | Document closure / future executable gate |
|---|---|---|
| C1 | SECURITY DEFINER search path | exact `pg_catalog,httpapi,pg_temp`; proconfig + schema CREATE verifier |
| C2 | ambiguous canonical bytes | `0x02`, `u32be(key_version)`, byte limits and fixed hex/HMAC goldens |
| C3 | impossible key rotation | bounded primary+staged keyring; DB-selected active; audited resolve; quiesced future Action/history rotation |
| C4 | mixed-backend rolling cutover | fleet-wide ingress block/drain/switch/inventory/restore; no enforcement canary |
| C5 | lifecycle bypass/history loss | RL2 bootstrap insert-only revision 1; revision 2+ Action + append-only history separately approved |
| I1 | stale DBR/harness | dynamic approved merge SHA; reuse hardened `7019230` self-owned-cluster contract |
| I2 | four routines vs three-function down | four exact signatures; disposable up→down→up and empty-schema assertion |
| I3 | missing environment constraint | policy FK to `core.environment(id) ON DELETE RESTRICT` |
| I4 | Go/SQL numeric drift | Go values capped to PG int32; checked ceil/multiply/add/epoch/retry math |
| I5 | unaudited HMAC read | real `NewAudited` + `WithCaller` + `Resolve(purpose)`；primary/staged success/failure AccessRecord tests |
| I6 | bootstrap parity / stale env authority | signed complete fleet manifest + principal/process/Ready/DB/ref-scope equality；remove legacy quota vars after activation |
| I7 | per-replica pool only | full-fleet connection equation and 20%/10-connection reserve gate |
| I8 | non-decidable performance threshold | paired 60m windows, sample minima, 60s aggregation, five-window sustained definition |
| I9 | shadow disagreement ambiguity | raw/unclassified separated from controlled algorithm mismatch and expected consolidation |
| I10 | old-revision TTL/prune ambiguity | environment argument, current-policy TTL, environment-first index, 100k EXPLAIN/concurrency proof |
| M1 | function count wording | all prose/grants use four routines and exact signatures |
| M2 | unbounded/forgeable metadata | 1..256-byte updater/change ref; DB-derived `session_user` |
| M3 | omitted HMAC residual threat | equality/enumeration/compromised-API/route-shape residuals explicit |
| M4 | meaningless failure-mode variable | variable removed; fail-closed is non-configurable invariant |

## RL0 Round-2 Review Closure Matrix

| ID | Review item | Document closure / future executable gate |
|---|---|---|
| R2-I1 | wrong audited-secret API | exact `NewAudited(inner,recorder,env)` + `WithCaller` + `Resolve(purpose)` API and both-slot success/failure audit tests |
| R2-I2 | environment equality prose-only | ReadyState.Environment + principal/process/DB/ref-scope four-way equality；mismatch 503 with zero HMAC/consume/handler calls |
| R2-I3 | DBR v1 in-place mutation | approved current version + ancestor proof；publish next version/exact CR diff；current immutable；unknown/mismatch STOP |
| R2-I4 | fleet parity had no evidence input | canonical Ed25519 signed complete fleet manifest/keyring with replica/build/env/backend/quota；invalid manifest rejects before INSERT |
| R2-M1 | prune backlog unbounded | runtime has_more + batch+1 capped backlog；exact count moved to approved low-frequency diagnostic；1M-row EXPLAIN |

## Approval Summary

- **RL0:** docs-only, may be approved now.
- **RL1:** may request a separate contract/reference-model approval after RL0 review.
- **RL2:** NO-GO until fresh DBR1 + exact migration approval; no staging.
- **RL3:** NO-GO until migration/DBR/CredentialRef/staging approvals all exist.
- **RL4 staging enforcement:** NO-GO until shadow evidence and separate approval.
- **RL4 production:** NO-GO until staging/rollback/recovery evidence and a separate production change.
- **Live enforcement today:** **NO-GO**.
