# R2-10 Cluster Job Ownership Implementation Plan

> **Approval artifact only.** Do not execute a slice until a human explicitly approves that
> slice and every named dependency. Each slice uses its own worktree/branch/PR; Codex does not
> merge or deploy.

**Goal:** mechanically prove that a logical worker cluster has one scheduler lease, one logical
JobRow per periodic slot, an explicit at-least-once/idempotency contract, a version-compatible
fleet and a detector for duplicate/missing ownership evidence.

**Spec:** `docs/superpowers/specs/2026-08-29-r2-10-job-ownership-design.md`

## Global Gates

- R210-0 is docs-only and grants no implementation authority.
- Reuse River v0.45 PostgreSQL leadership; do not add a second lease table/elector.
- One database/River schema belongs to exactly one environment/worker cluster.
- A logical duplicate is multiple JobRows for one job/slot；attempts on one row are retries.
- River Work remains at-least-once；external reads may repeat and enter R2-15 budgets.
- No external write, Foundation-B Action, production scale-out or third-party cron change.
- R210-1 has no migration；R210-2 uses only harness-owned pinned disposable PG18.
- R210-2 的 database-binding 读取权限必须进入获批 DBR next policy；disposable 管理身份
  通过不代表 runtime 最小权限已获批。
- R210-3 metric/alert/UI/DBR contracts require separate approval；R210-4 staging is human-only.
- R2-10 is not satisfied until R210-4 evidence is accepted.
- Every slice runs go fmt/vet/test, frontend gates when touched, governance and diff check.

## Slice Map

### R210-1 — manifest and current six-job contract

- Create: `contracts/jobs/cluster-jobs.v1.json`
- Create: `internal/platform/jobs/job_manifest.go`
- Create: `internal/platform/jobs/job_manifest_test.go`
- Create: `internal/platform/jobs/effective_manifest.go`
- Create: `internal/platform/jobs/effective_manifest_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: the six Args/contract tests
- Modify: `cmd/platform-worker/README.md`

### R210-2 — fleet identity and true two-replica proof

- Create: `contracts/jobs/job-fleet-manifest.schema.json`
- Create: `contracts/jobs/job-fleet-inventory.schema.json`
- Create: `contracts/jobs/job-fleet-keyring.v1.json`
- Create: `internal/platform/jobs/fleet_manifest.go`
- Create: `internal/platform/jobs/fleet_manifest_test.go`
- Create: `internal/platform/jobs/fleet_keyring.go`
- Create: `internal/platform/jobs/fleet_keyring_test.go`
- Create: `internal/platform/jobs/fleet_inventory.go`
- Create: `internal/platform/jobs/fleet_inventory_test.go`
- Create: `internal/platform/jobs/cluster_identity.go`
- Create: `internal/platform/jobs/cluster_identity_test.go`
- Create: `internal/platform/jobs/cluster_ownership_integration_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `cmd/platform-worker/config.go`
- Modify: `cmd/platform-worker/config_test.go`
- Modify: `cmd/platform-worker/main.go`
- Modify: `deploy/compose/.env.example`
- Create: `docs/runbooks/WORKER-CLUSTER-OWNERSHIP.md`

### R210-3 — probe, metric, alert and read-only jobs page

- Create: `contracts/jobs/cluster-jobs.v2.json`
- Create: `internal/platform/jobs/ownership_probe.go`
- Create: `internal/platform/jobs/ownership_probe_test.go`
- Create: `internal/platform/jobs/ownership_probe_integration_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `internal/platform/ops/freshness.go`
- Modify: `internal/platform/ops/metrickeys_test.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/rules_test.go`
- Modify after approved DBR CR: next `contracts/database/role-policy.v*.json`
- Create: `tests/security/job-ownership-database-role.test.sh`
- Create: `web/apps/admin-web/src/pages/JobsPage.tsx`
- Create: `web/apps/admin-web/src/pages/JobsPage.test.tsx`
- Modify: `web/apps/admin-web/src/router.tsx`
- Modify: `web/apps/admin-web/src/router.test.tsx`
- Modify: `docs/modules/ops/README.md`

### R210-4 — staging acceptance packet

- Create: `docs/evidence/TEMPLATE-r2-10-multi-replica-acceptance.md`
- Create: `docs/runbooks/SWITCH-WORKER-CLUSTER-MULTI-REPLICA.md`
- Modify only after approval: `deploy/compose/launch.yaml`, deployment config/fleet artifact

---

## 0. Preflight for Every Slice

- [ ] Verify exact worktree, branch, clean status, target base and approval references.
- [ ] Fetch `origin/release/v0.1-launch`; require exact approved tip, not only merge-base.
- [ ] Record `go.mod`, `VERSIONS.lock`, River migration mirror digest and max River version.
- [ ] Inventory every `river.NewPeriodicJob` and every non-River ticker. Any new ticker or seventh
      unknown periodic ID is a STOP, not something to auto-add to the contract.
- [ ] Record what is not run (staging/production/real credentials) explicitly.

## R210-1 — Freeze the Existing Contract

### Task 1: Write the literal JobManifestV1

**Interfaces:**

```go
type OwnershipMode string
const OwnershipClusterSingleton OwnershipMode = "cluster_singleton"

type JobSpec struct {
    ID, Kind, Queue, OwnerProcess, ScheduleConfig, RunOnStartSource string
    Ownership OwnershipMode
    CatchUp, Execution, SideEffectClass, IdempotencyEvidence string
    EnqueueFences, UniqueStates []string
}

type JobManifest struct { Version int; Scheduler, ClusterModel string; Jobs []JobSpec }
func LoadJobManifest([]byte) (JobManifest, string, error)
func RegisteredPeriodicJobSpecs() []JobSpec
```

- [ ] Write RED tests before contract/loader:

```go
func TestManifestCoversExactlySixRegisteredPeriodicJobs(t *testing.T)
func TestManifestPinsStableIDsKindsQueuesScheduleSourcesAndCatchup(t *testing.T)
func TestEveryArgsUsesArgsQueueEffectivePeriodAndExplicitDefaultStates(t *testing.T)
func TestProductionArgsContainNoReplicaRunID(t *testing.T)
func TestManifestRejectsUnknownDuplicateMissingAndNonRiverJobs(t *testing.T)
func TestEverySideEffectDeclaresAtLeastOnceIdempotencyEvidence(t *testing.T)
```

- [ ] Run the full jobs package and preserve RED output.
- [ ] Implement strict JSON loader and literal six rows. Unknown fields/values fail closed.
- [ ] Refactor `NewClient` to consume one typed registry when constructing `PeriodicJob`s；do not
      change runtime intervals/queues/enabled defaults. Each Args explicitly assigns
      `rivertype.UniqueOptsByStateDefault()` and tests its frozen v0.45 state set.
- [ ] Run focused and full jobs tests, then commit exact files. No runtime fleet wiring in R210-1.

### Task 2: Freeze EffectiveJobManifest canonical bytes

```go
type EffectiveJobSpec struct {
    ID, Kind, Queue, Ownership, CatchUp, SideEffectClass string
    Enabled, RunOnStart bool
    IntervalSeconds int64
}
type EffectiveJobManifest struct {
    ContractVersion int
    Environment, WorkerClusterID, RiverSchema string
    Jobs []EffectiveJobSpec
}
func BuildEffectiveManifest(Config, JobManifest) (EffectiveJobManifest, string, error)
```

- [ ] RED: byte-order sorting, integer seconds, empty/invalid cluster/environment/schema,
      sub-second period, disabled jobs, mismatched config source and cross-process golden hash.
- [ ] Freeze canonical JSON field order, UTF-8, decimal integers and lowercase SHA-256.
- [ ] Add a golden containing all six default jobs and another with one disabled job.
- [ ] Assert a schedule/RunOnStart/enabled change changes hash；logger pointer/secret/provider does not.
- [ ] Commit with docs and stop. R210-1 does not prove multiple replicas.

## R210-2 — Fleet Identity and River Leadership Proof

### Task 3: Signed JobFleetManifestV1

**Frozen fields:** environment, cluster ID, River schema, database binding hash, epoch, job
contract version, effective hash, logical replica IDs/build digests, build capability rows,
signed active-binding inventory epoch/digest, validity, change ref and nonce. Inventory v1 freezes
sorted binding hash→environment/cluster/status rows and its own epoch/validity/nonce.

- [ ] RED tests: strict manifest/inventory decoders, literal bytes/signature goldens, wrong purpose/protocol/key,
      expired/not-yet-valid, duplicate/missing replica, unknown build, build capability mismatch,
      effective hash mismatch, manifest/inventory digest mismatch, duplicate active binding and
      replayed lower epoch. Also reject non-canonical IDs, Unicode/path separators, wrong hash/build
      digest length/case, nonpositive epoch, overlong validity and duplicate nonce.
- [ ] Use Ed25519 verification with an independently configured public keyring. Runtime has no
      signing private key. Key IDs/purpose/protocol/validity are exact. Embed the reviewed keyring
      bytes in the worker build and pin a golden SHA-256；an arbitrary adjacent file cannot replace it.
- [ ] Every replica must appear exactly once；all concurrently allowed builds must advertise the
      same effective manifest hash. Autoscaling/dynamic replica sets are not approved in v1.
- [ ] The release signer emits a separately signed active binding inventory and refuses one
      database binding hash assigned to multiple active clusters/environments. Fleet manifest pins
      its exact epoch/digest. Test replay, duplicate binding, closed/reopened epochs and history mutation.
- [ ] Commit contract/loader only after security review. Fleet signing workflow is separate authority.

### Task 4: Construct cluster/replica/River client identity before DB access

```go
type WorkerClusterIdentity struct { Environment, WorkerClusterID, RiverSchema string; ManifestVersion int; EffectiveManifestHash, DatabaseBindingHash string }
type WorkerReplicaIdentity struct { LogicalReplicaID, BootID, BuildDigest string }
func ValidateReplicaAgainstFleet(WorkerClusterIdentity, WorkerReplicaIdentity, SignedJobFleetManifestV1, SignedJobFleetInventoryV1, Keyring, time.Time) error
func RiverClientID(WorkerClusterIdentity, WorkerReplicaIdentity) (string, error)
```

- [ ] RED tests: CSPRNG BootID, restart changes BootID, duplicate logical/boot IDs, 127-byte cap,
      invalid chars, environment/schema mismatch and missing fleet entry.
- [ ] Parse and validate fleet signature/slot/build/effective config before any secret/DB call.
- [ ] Add exact non-secret config names `XM_WORKER_CLUSTER_ID`, `XM_WORKER_LOGICAL_REPLICA_ID`,
      `XM_JOB_FLEET_MANIFEST_PATH`, and `XM_JOB_FLEET_INVENTORY_PATH`; path content is signed and paths are not accepted from
      per-request input. The verification keyring is the repo-tracked, build-pinned
      `contracts/jobs/job-fleet-keyring.v1.json`, not a deployment-selectable path；key rotation
      requires a reviewed contract/build change.
- [ ] After resolving the existing database CredentialRef, query only `current_database()`,
      `pg_control_system().system_identifier` and River migration/schema readiness；canonicalize
      the system ID/database/schema with fixed length-prefixing and require the signed database
      binding hash before River start. DBR must separately approve the minimum built-in EXECUTE or
      a fixed safe wrapper；host/port/DSN text is never accepted as identity. Binding mismatch or
      duplicate active binding fails closed.
- [ ] Add literal canonical-byte/SHA goldens for system identifier/database/schema, plus field-order,
      length, Unicode and host/port/DSN-substitution negative tests.
- [ ] Set `river.Config.ID` explicitly to cluster/logical-replica/boot identity.
- [ ] Emit one startup snapshot with cluster/replica/build/manifest/River versions but no raw DSN,
      hostname, PID, key material or full client ID.
- [ ] A manifest change requires fleet quiesce；unchanged effective hash permits approved current/next
      build rolling. Write the runbook before wiring config.

### Task 5: Prove real leadership and failover with two independent clients

Harness creates and exclusively owns a random Compose project/network/volume with the pinned PG18
digest, exposes only random `127.0.0.1` port, verifies labels/digest/fresh catalog/sentinel, runs
platform + exact River migrations, and destroys the whole project in `finally`. External/admin DSN,
fixed port, `0.0.0.0`, wrong label/version/digest and Skip are fatal.

```go
func TestTwoClientsShareOneRiverLeaderAndOneLogicalRowPerSlot(t *testing.T)
func TestLeaderCrashFailsOverWithinBoundWithoutDuplicateRows(t *testing.T)
func TestPartitionedOldLeaderStopsAfterTrustWindow(t *testing.T)
func TestPostgresRestartReelectsWithoutDuplicatingPersistedSlot(t *testing.T)
func TestRetryOnOneJobRowIsNotLogicalDuplicate(t *testing.T)
func TestDuplicateClientIDFailsTheHarness(t *testing.T)
func TestSameSchemaRejectsCrossEnvironmentOrCluster(t *testing.T)
func TestSignedFleetInventoryRejectsSameDatabaseBindingForTwoClusters(t *testing.T)
func TestManifestMismatchPreventsSecondClientStart(t *testing.T)
```

- [ ] Use at least three >=1s test slots, DB clock and all enabled test job IDs.
- [ ] Query `river_leader/default` and safe `river_job` columns；never assume process logs prove lease.
- [ ] Kill/partition the elected client, wait only the documented River trust/failover bound, and
      assert the old client inserts nothing after expiry.
- [ ] Count logical rows by periodic metadata + scheduled slot. `attempt >1` on one ID is retry.
- [ ] Run twice and record River v0.45/migration bundle hashes. Stop；no staging or production.

## R210-3 — Duplicate Probe and Read-Only Operations Surface

### Task 6: Publish manifest v2 and write ownership probe RED tests

v2 adds `job_ownership_probe` on maintenance queue. Its enablement changes effective manifest and
therefore requires the quiesced rollout gate even if code already exists disabled.

```go
type OwnershipCode string
type JobSlotEvidence struct { JobID string; SlotStart, SlotEnd time.Time; LogicalRows, Attempts int; Code OwnershipCode }
type OwnershipReport struct { ClusterID, ManifestHash string; EvaluatedAt time.Time; CoverageComplete bool; Jobs []JobSlotEvidence }
type OwnershipReader interface { PreviousCompleteSlots(context.Context, EffectiveJobManifest, time.Time) (OwnershipReport, error) }
```

- [ ] RED: ok, duplicate, overlap, missing, bounded fault-window failover gap, unexpected ID,
      missing/expired leader, manifest mismatch, retry-only and truncated retention. Probe checks
      previous complete slot and uses DB time.
- [ ] Reader uses fixed SQL over only leader/job safe columns. No Args/errors/attempted_by/raw IDs are
      returned to UI or logged. One PostgreSQL `clock_timestamp()` snapshot determines evaluated_at,
      all slot boundaries and leader remaining；app clocks cannot change the verdict.
- [ ] Probe writes `platform.jobs.ownership` ops observation with counts/codes/coverage/manifest facts;
      add exact metric registry/contract test. Unknown value fails closed.
- [ ] Add rules for duplicate/missing/unexpected/probe stale；test zero/one/two consecutive windows,
      recovery and evidence link. `failover_gap_expected` is visible evidence but does not page unless
      it exceeds the one-slot/window bound. Notification test message is mandatory before activation.
- [ ] DBR current policy is extended by next version + independent CR；API never reads River tables.

### Task 7: Replace `/jobs` placeholder with an honest read-only page

- [ ] RED frontend tests for loading/live/empty/error/stale/permission denied and malformed value.
- [ ] Show cluster/environment, manifest version/hash prefix, leader presence/term remaining, probe
      freshness, and one row per job with last complete slot/result/retry count.
- [ ] Stable explanations distinguish logical duplicate from retry.
- [ ] No Drawer, run-now, unlock, leader takeover, force retry or write button. Existing ops scope is
      server-enforced；new scope/Action is out of slice.
- [ ] Source/freshness/coverage appear on every read. No prototype/sample values in live state.
- [ ] Run admin typecheck/tests/Storybook and browser at 1440/1024/800/390 plus keyboard order.

## R210-4 — Human Staging Acceptance

### Task 8: Prepare, but do not execute, the staging packet

Required evidence:

1. exact release/image/River bundle/job manifest/fleet manifest/keyring/DBR hashes；
2. two explicit worker replica slots with same effective hash；
3. harness suite green twice, no Skip；
4. rollback to one replica and approved prior manifest；
5. probe/alert/UI ready with raw delete/external writes disabled；
6. failure injection: leader kill, DB partition, duplicate ID, manifest mismatch, duplicate row；
7. no credentials or raw River args in artifacts.

### Task 9: HUMAN execution only after staging approval

1. verify backup/DBR/fleet artifact；
2. fleet-quiesce if job manifest changes；
3. start two replicas and verify one leader/identical effective hash；
4. observe at least three normal windows for all enabled jobs；
5. kill/partition leader and verify bounded failover/no duplicate；at most one explicitly classified
   fault-window gap may occur and must bind the old/new term evidence；
6. inject detector fixtures and verify stable alerts/UI；
7. soak at least the longest practical job window or use approved accelerated shadow jobs；
8. restore exact prior image/manifest/one replica on any mismatch.

Production remains a new approval. R2-10 is marked satisfied only when the accepted evidence packet
has zero duplicate/missing/unexpected/manifest incidents in normal soak, only bounded approved
failover-gap evidence during fault injection, and no skipped gate.

## Final Requirement-to-Evidence Matrix

| Requirement | Evidence |
|---|---|
| logical cluster vs replica | signed fleet identity + unique client ID tests |
| cluster scheduler lease | two-client `river_leader/default` + expiry/failover tests |
| enqueue second fence | exact six-job Args/queue/period/ID contract tests |
| retry vs duplicate | JobRow ID/attempt grouping fixtures |
| duplicate/missing detection | ownership probe stable-code tests |
| version capability | signed build capability matrix + mismatch/quiesce tests |
| one environment per schema | cross-environment/cluster rejection tests |
| side-effect safety | per-job at-least-once/idempotency declaration tests |
| UI honesty | source/freshness/coverage/state/browser tests |
| P0 completion | human accepted two-replica staging/failure/rollback packet |

Any missing, stale, indirect or skipped evidence leaves R2-10 incomplete.
