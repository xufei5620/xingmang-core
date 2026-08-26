# Retired Zero-Balance Account Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a monotonic encrypted set of safely retired zero-balance external IDs so legitimate zero-balance deletions do not stop synchronization while any ID reuse remains fail-closed.

**Architecture:** Upgrade only the sender-side `balanceReconciliationState` from v1 to v2. Each atomic full capture rejects retired-ID reappearance, retires only missing canonical-zero/non-negative prior rows, and atomically stores the captured snapshot, emission snapshot, and sorted retirement set in the existing encrypted current-state file. The receiver, source SQL bridges, upstream applications, keys, AAD, cutover manifest, and balance snapshot schemas remain unchanged.

**Tech Stack:** Go 1.25 source agent, AES-256-GCM `EncryptedStateFile`, file-backed cursor/spool state, Docker Compose, PostgreSQL 15/18 integration gates, PowerShell release tooling.

**Spec:** `docs/superpowers/specs/2026-08-26-retired-zero-balance-account-design.md`

## Global Constraints

- Never modify Sub2API or New API source code, application tables, containers, or business data.
- Retire only a previously acknowledged row whose `ServiceUnits == "0"` and `BalanceNegative == false`.
- A retired external ID is permanent and any reappearance fails before state replacement or publication.
- `retired_zero_account_ids` is strictly numeric-sorted, unique, monotonic, valid, bounded to 100,000 entries, and disjoint from captured rows.
- Keep `cutoverSchemaVersion`, the AES key, AAD, cursor schema, receiver schema, and PostgreSQL bridge contracts unchanged.
- Version-1 and legacy current state migrate in memory with an empty retirement set only because the permissive missing-zero binary has never run in production.
- Version-2 state is a one-way production upgrade after its first successful write or ACK; never restore an older cursor/state over accepted receiver sequence.
- Both payment completion and usage occurrence must remain at or after `2026-08-31T16:00:00Z` to become invoice eligible.
- Do not push to GitHub.

---

### Task 1: Version-2 state model and strict loader

**Files:**
- Modify: `agents/sourceagent/economics_db.go:337-353,640-661,727-749`
- Test: `agents/sourceagent/balance_delta_test.go`

**Interfaces:**
- Consumes: existing `EncryptedStateFile.Load`, `BalanceSnapshot`, `compareDecimalIDs`, and v1 `balance_delta_v1` state.
- Produces: `balanceReconciliationState` v2 with `RetiredZeroAccountIDs []string`, `validateRetiredBalanceAccountIDs([]string, []BalanceSnapshotRow) error`, and an in-memory v1-to-v2 loader.
- Test helpers: `testBalanceState(t *testing.T, schema int, kind string, retired []string) balanceReconciliationState` and `requireReplaceBalanceState(t *testing.T, store EncryptedStateFile, state balanceReconciliationState)`.

- [ ] **Step 1: Add failing v1 migration and v2 validation tests**

```go
func testBalanceState(t *testing.T, schema int, kind string, retired []string) balanceReconciliationState {
    t.Helper()
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil)
    emission := testBalanceSnapshot(t, "reconciliation", captured.AsOf, strings.Repeat("a", 64), nil)
    return balanceReconciliationState{SchemaVersion: schema, StateKind: kind,
        BaseSnapshotID: strings.Repeat("a", 64), CapturedSnapshot: captured,
        EmissionSnapshot: emission, RetiredZeroAccountIDs: retired}
}

func requireReplaceBalanceState(t *testing.T, store EncryptedStateFile, state balanceReconciliationState) {
    t.Helper()
    if err := store.Replace(context.Background(), state); err != nil { t.Fatal(err) }
}

func TestBalanceDeltaLoadsV1StateAsV2WithEmptyRetiredSet(t *testing.T) {
    state := testBalanceState(t, 1, "balance_delta_v1", nil)
    _, current := testBalanceStores(t, state.CapturedSnapshot)
    requireReplaceBalanceState(t, current, state)
    loaded, legacy, exists, err := loadCurrentBalanceReconciliationState(context.Background(), current)
    if err != nil || !exists || legacy != nil || loaded.SchemaVersion != 2 || len(loaded.RetiredZeroAccountIDs) != 0 {
        t.Fatalf("v1 migration failed: loaded=%#v legacy=%#v exists=%t err=%v", loaded, legacy, exists, err)
    }
}

func TestBalanceDeltaRejectsInvalidRetiredSets(t *testing.T) {
    for _, ids := range [][]string{{"2", "2"}, {"10", "9"}, {"01"}, {"1\n"}} {
        state := testBalanceState(t, 2, "balance_delta_v2", ids)
        if err := validateBalanceReconciliationState(state); err == nil {
            t.Fatalf("invalid retired set accepted: %#v", ids)
        }
    }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `cd agents && go test ./sourceagent -run '^TestBalanceDelta(LoadsV1StateAsV2WithEmptyRetiredSet|RejectsInvalidRetiredSets)$' -count=1`

Expected: FAIL because v2 fields and validation do not exist.

- [ ] **Step 3: Add the minimal v2 model and loader**

```go
const (
    balanceReconciliationStateSchemaVersionV1 = 1
    balanceReconciliationStateSchemaVersion   = 2
    maxRetiredBalanceAccounts                  = 100_000
)

type balanceReconciliationState struct {
    SchemaVersion         int             `json:"schema_version"`
    StateKind             string          `json:"state_kind"`
    BaseSnapshotID        string          `json:"base_snapshot_id"`
    CapturedSnapshot      BalanceSnapshot `json:"captured_snapshot"`
    EmissionSnapshot      BalanceSnapshot `json:"emission_snapshot"`
    RetiredZeroAccountIDs []string        `json:"retired_zero_account_ids,omitempty"`
}
```

Decode the shared struct once. Accept strict v2 directly; accept v1 only with state kind `balance_delta_v1` and an empty retirement field, validate its existing snapshots/subset invariants, then normalize it in memory to v2/`balance_delta_v2`. Do not rewrite the file during load.

- [ ] **Step 4: Run focused and full source-agent tests**

Run: `cd agents && go test ./sourceagent -count=1 && go test ./... -count=1`

Expected: PASS.

### Task 2: Monotonic retirement transition and reappearance fence

**Files:**
- Modify: `agents/sourceagent/economics_db.go:663-725`
- Test: `agents/sourceagent/balance_delta_test.go`

**Interfaces:**
- Consumes: v2 `RetiredZeroAccountIDs` from Task 1.
- Produces: `buildBalanceReconciliationStateWithRetired(baseSnapshotID string, previous, captured BalanceSnapshot, retired []string) (balanceReconciliationState, error)` and a numeric-sorted retirement union.
- Test helpers: `buildStateWithCapturedRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error)` and `buildStateWithMissingPriorRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error)`.

- [ ] **Step 1: Add failing transition tests**

```go
func buildStateWithCapturedRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error) {
    t.Helper()
    previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", nil)
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", []BalanceSnapshotRow{row})
    return buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, retired)
}

func buildStateWithMissingPriorRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error) {
    t.Helper()
    previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", []BalanceSnapshotRow{row})
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil)
    return buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, retired)
}

func TestBalanceDeltaRetiresMultipleZeroAccountsInNumericOrder(t *testing.T) {
    previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", []BalanceSnapshotRow{
        {ExternalUserID: "2", ServiceUnits: "0"}, {ExternalUserID: "9", ServiceUnits: "0"},
        {ExternalUserID: "10", ServiceUnits: "0"}, {ExternalUserID: "11", ServiceUnits: "50"},
    })
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", []BalanceSnapshotRow{
        {ExternalUserID: "3", ServiceUnits: "0"}, {ExternalUserID: "11", ServiceUnits: "50"},
    })
    state, err := buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, []string{"1"})
    if err != nil || !reflect.DeepEqual(state.RetiredZeroAccountIDs, []string{"1", "2", "9", "10"}) {
        t.Fatalf("retirement union failed: state=%#v err=%v", state, err)
    }
}

func TestBalanceDeltaRetiredAccountReappearanceAlwaysFails(t *testing.T) {
    for _, row := range []BalanceSnapshotRow{
        {ExternalUserID: "9", ServiceUnits: "0"}, {ExternalUserID: "9", ServiceUnits: "1"},
        {ExternalUserID: "9", ServiceUnits: "0", BalanceNegative: true},
        {ExternalUserID: "9", ServiceUnits: "0", BaselineMember: true},
    } {
        if _, err := buildStateWithCapturedRow(t, []string{"9"}, row); err == nil {
            t.Fatalf("retired ID reappeared without failure: %#v", row)
        }
    }
}

func TestBalanceDeltaNonZeroRemovalLeavesRetiredSetUnchanged(t *testing.T) {
    for _, row := range []BalanceSnapshotRow{
        {ExternalUserID: "9", ServiceUnits: "1"},
        {ExternalUserID: "9", ServiceUnits: "0", BalanceNegative: true},
    } {
        if _, err := buildStateWithMissingPriorRow(t, []string{"2"}, row); err == nil {
            t.Fatalf("unsafe removal was accepted: %#v", row)
        }
    }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `cd agents && go test ./sourceagent -run '^TestBalanceDelta(RetiresMultipleZeroAccountsInNumericOrder|RetiredAccountReappearanceAlwaysFails|NonZeroRemovalLeavesRetiredSetUnchanged)$' -count=1`

Expected: FAIL because safe missing IDs are not persisted and reappearance is not fenced.

- [ ] **Step 3: Implement the ordered transition**

```go
func buildBalanceReconciliationStateWithRetired(
    baseSnapshotID string,
    previous, captured BalanceSnapshot,
    retired []string,
) (balanceReconciliationState, error)
```

Before comparing rows, reject any intersection between retired IDs and captured rows. During the existing two-pointer comparison, append only safe missing prior IDs to a separately ordered `newlyRetired` slice; unsafe missing rows return the existing error. Merge old and new retirement slices numerically, reject duplicates/overflow, compute ordinary changed rows, build the emission snapshot, and validate the complete v2 state. Keep `buildBalanceReconciliationState` as a test-compatible wrapper that passes an empty retirement set.

- [ ] **Step 4: Run balance, race, and module tests**

Run: `cd agents && go test ./sourceagent -run '^TestBalanceDelta' -count=1 && go test -race ./sourceagent -run '^TestBalanceDelta' -count=1 && go test ./... -count=1`

Expected: PASS.

### Task 3: Atomic persistence, ACK-loss replay, and size failure

**Files:**
- Modify: `agents/sourceagent/economics_db.go:587-638`
- Test: `agents/sourceagent/balance_delta_test.go`

**Interfaces:**
- Consumes: `buildBalanceReconciliationStateWithRetired` and normalized v2 state.
- Produces: production `prepareReconciliationCycle` that carries the acknowledged retirement set into the next atomic `Current.Replace`.

- [ ] **Step 1: Add failing persistence tests**

```go
func TestBalanceDeltaPreparedRetirementIsRetryStable(t *testing.T) {
    rows := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "0", BaselineMember: true}}
    baseline := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", rows)
    baselineStore, currentStore := testBalanceStores(t, baseline)
    connector := &BalanceDBConnector{Source: SourceSub2API, SourceID: baseline.SourceID,
        Manifest: testBalanceManifest(baseline), Baseline: baselineStore, Current: currentStore}
    cursor := ScanCursor{Version: 2, CutoverAt: baseline.CutoverAt, SnapshotID: baseline.SnapshotID, Completed: true}
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil)
    captures := 0
    first, err := connector.prepareReconciliationCycle(context.Background(), cursor, func(context.Context) (BalanceSnapshot, error) {
        captures++
        return captured, nil
    })
    if err != nil { t.Fatal(err) }
    retry, err := connector.prepareReconciliationCycle(context.Background(), cursor, func(context.Context) (BalanceSnapshot, error) {
        captures++
        return BalanceSnapshot{}, errors.New("must not recapture")
    })
    if err != nil || captures != 1 || !reflect.DeepEqual(first, retry) {
        t.Fatalf("prepared retirement drifted: first=%#v retry=%#v captures=%d err=%v", first, retry, captures, err)
    }

    acknowledged := cursor
    acknowledged.SnapshotID = first.Snapshot.SnapshotID
    if _, err = connector.prepareReconciliationCycle(context.Background(), acknowledged, func(context.Context) (BalanceSnapshot, error) {
        return testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:02:00Z", "", nil), nil
    }); err != nil { t.Fatal(err) }
    state, _, _, err := loadCurrentBalanceReconciliationState(context.Background(), currentStore)
    if err != nil || !reflect.DeepEqual(state.RetiredZeroAccountIDs, []string{"1"}) {
        t.Fatalf("acknowledged retirement was not monotonic: state=%#v err=%v", state, err)
    }
}

func TestBalanceDeltaRetirementOverflowPreservesCurrentFile(t *testing.T) {
    retired := make([]string, maxRetiredBalanceAccounts)
    for i := range retired { retired[i] = strconv.Itoa(i + 1) }
    rows := []BalanceSnapshotRow{{ExternalUserID: "100001", ServiceUnits: "0"}}
    captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", rows)
    emission := testBalanceSnapshot(t, "reconciliation", captured.AsOf, strings.Repeat("a", 64), nil)
    state := balanceReconciliationState{SchemaVersion: 2, StateKind: "balance_delta_v2",
        BaseSnapshotID: strings.Repeat("a", 64), CapturedSnapshot: captured,
        EmissionSnapshot: emission, RetiredZeroAccountIDs: retired}
    baselineStore, currentStore := testBalanceStores(t, captured)
    requireReplaceBalanceState(t, currentStore, state)
    before, err := os.ReadFile(currentStore.Path)
    if err != nil { t.Fatal(err) }
    connector := &BalanceDBConnector{Source: SourceSub2API, SourceID: captured.SourceID,
        Manifest: testBalanceManifest(captured), Baseline: baselineStore, Current: currentStore}
    cursor := ScanCursor{Version: 2, CutoverAt: captured.CutoverAt,
        SnapshotID: emission.SnapshotID, Completed: true}
    _, err = connector.prepareReconciliationCycle(context.Background(), cursor, func(context.Context) (BalanceSnapshot, error) {
        return testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil), nil
    })
    after, readErr := os.ReadFile(currentStore.Path)
    if err == nil || readErr != nil || !bytes.Equal(before, after) {
        t.Fatalf("overflow did not preserve current state: prepare=%v read=%v", err, readErr)
    }
}

func TestBalanceDeltaLegacySnapshotMigratesToV2(t *testing.T) {
    baseline := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", nil)
    baselineStore, currentStore := testBalanceStores(t, baseline)
    legacy := testBalanceSnapshot(t, "reconciliation", "2026-08-24T00:00:00Z", "", nil)
    if err := currentStore.Replace(context.Background(), legacy); err != nil { t.Fatal(err) }
    connector := &BalanceDBConnector{Source: SourceSub2API, SourceID: baseline.SourceID,
        Manifest: testBalanceManifest(baseline), Baseline: baselineStore, Current: currentStore}
    cursor := ScanCursor{Version: 2, CutoverAt: baseline.CutoverAt, SnapshotID: legacy.SnapshotID, Completed: true}
    if _, err := connector.prepareReconciliationCycle(context.Background(), cursor, func(context.Context) (BalanceSnapshot, error) {
        return testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", nil), nil
    }); err != nil { t.Fatal(err) }
    state, _, _, err := loadCurrentBalanceReconciliationState(context.Background(), currentStore)
    if err != nil || state.SchemaVersion != 2 || len(state.RetiredZeroAccountIDs) != 0 {
        t.Fatalf("legacy migration did not write empty v2 retirement state: state=%#v err=%v", state, err)
    }
}
```

- [ ] **Step 2: Run the persistence tests and verify RED**

Run: `cd agents && go test ./sourceagent -run '^TestBalanceDelta(PreparedRetirementIsRetryStable|RetirementPersistsAcrossAcknowledgedCycles|RetirementOverflowPreservesCurrentFile|LegacySnapshotMigratesToV2)$' -count=1`

Expected: FAIL because prepare does not propagate or persist the retirement set.

- [ ] **Step 3: Carry state through prepare and preserve atomicity**

Track `retired := state.RetiredZeroAccountIDs` only when the cursor acknowledges `state.EmissionSnapshot.SnapshotID`; pass it into the v2 builder. If `cursor.SnapshotID == state.BaseSnapshotID`, return the already prepared emission without capture or replacement. Rely on `EncryptedStateFile.Replace` marshalling and size validation before `writePrivateAtomicFile`, and validate the retirement-count bound before calling Replace.

- [ ] **Step 4: Run focused and complete tests**

Run: `cd agents && go vet ./... && go test ./... -count=1`

Expected: PASS.

### Task 4: Protocol, runbook, and full repository verification

**Files:**
- Modify: `docs/SOURCE-SYNC-PROTOCOL.md:81-98`
- Modify: `docs/SOURCE-AGENT-RUNBOOK.md:32-40,287-305,317-405`
- Test: `scripts/verify.ps1`

**Interfaces:**
- Consumes: final v2 behavior from Tasks 1-3.
- Produces: unambiguous operator rules for zero retirement, nonzero/negative failure, reappearance failure, v1 migration, one-way rollback, and complete state backup.

- [ ] **Step 1: Replace the obsolete blanket-missing runbook rule**

Document three separate canaries: zero/non-negative missing succeeds with no synthetic event and a durable retired ID; positive or negative-marked missing fails; retired-ID reappearance fails. State that `state.json`, `balance-current.enc`, `pending.enc` when present, state key, spool key, exact image, and hashes are one recovery generation.

- [ ] **Step 2: Run formatting and repository gates**

Run: `git diff --check`

Run: `cd agents && gofmt -w sourceagent/economics_db.go sourceagent/balance_delta_test.go && go vet ./... && go test ./... -count=1`

Run: `powershell -NoProfile -File scripts/verify.ps1`

Expected: all commands exit 0, including PostgreSQL 15/18 source-contract integration.

- [ ] **Step 3: Request independent code and security review**

Review must explicitly approve the v1 migration prerequisite, retirement monotonicity, reappearance fence, retry semantics, overflow preservation, docs, and production rollout. Fix all Critical and Important findings, then repeat affected tests.

### Task 5: Signed RC47 artifact and narrow production rollout

**Files:**
- Create: `release/0.1.0-rc47-exact1/`
- Create: `release/0.1.0-rc47-deploy/`
- Create on server with `record=/root/invoice-system/deployment-records/rc47-retired-zero-account-$(date -u +%Y%m%dT%H%M%SZ)` and then `install -d -m 0700 "$record"`
- Modify on server: `/root/invoice-system/deploy/.env.production` image tag only after exact archive verification

**Interfaces:**
- Consumes: reviewed source commit/tag and exact image-gate manifest.
- Produces: signed RC47 release, exact source-agent image, encrypted predeployment evidence, and a healthy production canary.

- [ ] **Step 1: Commit and sign source**

Run: `git add agents/sourceagent/economics_db.go agents/sourceagent/balance_delta_test.go docs/SOURCE-SYNC-PROTOCOL.md docs/SOURCE-AGENT-RUNBOOK.md docs/superpowers/plans/2026-08-26-retired-zero-balance-account.md`

Run: `git commit -S -m "fix: retire removed zero-balance source accounts"`

Run: `git tag -s v0.1.0-rc47-signed -m "SoloV Invoice v0.1.0-rc47 signed release"`

Run: `git verify-commit HEAD && git tag -v v0.1.0-rc47-signed`

Expected: both signatures validate with the Invoice release ED25519 key.

- [ ] **Step 2: Build and scan immutable images**

Run: `powershell -NoProfile -File scripts/release-image-gate.ps1 -ReleaseName 0.1.0-rc47 -ImageTag 0.1.0-rc47 -SourceAgentVersion 0.3.0 -IdPMode keycloak -ReleaseDirectory release/0.1.0-rc47-exact1`

Expected: application image gate passes; the known IdP canary may keep overall public-launch status blocked, but Trivy, SBOM, source tests, PostgreSQL 15/18, and exact image IDs must pass.

- [ ] **Step 3: Package and verify the exact source-agent image**

Create a signed transfer directory containing `git archive v0.1.0-rc47-signed`, `git bundle create --all`, the exact gate evidence archive, `docker save invoice-source-agent:0.1.0-rc47`, SHA-256 manifest, and release-key signature. Verify all hashes and signatures locally and again on `fiberstate`. Do not rebuild on the server.

- [ ] **Step 4: Stop and back up only the failing stream before the one-way write**

Stop `newapi-balances`; prove no running container or lock. Record the RC46 image ID, state/cursor file hashes, absence or exact inspected contents of `pending.enc`, encrypted state backup, and the fact that production never ran the permissive working-tree binary. Preserve all files as one rollback unit.

- [ ] **Step 5: Load RC47 and recreate all ten agents uniformly**

Load the exact signed image archive, verify its immutable ID against the gate manifest, atomically change only the source-agent tag, and recreate all ten agents with `--no-build`. Do not restart Sub2API, New API, Invoice API, PostgreSQL, Keycloak, or Nginx.

- [ ] **Step 6: Verify state migration and dual-source canary**

Require `newapi-balances` to publish one successful complete cycle, write valid v2 encrypted state, retire the missing old zero account, and remain healthy without restart growth. Require all ten agents on one exact image ID, source queues with no queued/processing/failed/dead, New API external account `48` and Sub2API account `1113` verified under one invoice user, both eligibility states active, no historical/pre-policy claimable amount, upstream containers healthy/restart 0, and public `/healthz` and `/readyz` HTTP 200.

- [ ] **Step 7: Sign and download production evidence**

Store only counts, hashes, image IDs, policy values, booleans, and redacted status. Sign the server evidence, download it under `release/`, verify signatures locally, and confirm no raw OIDC subject, email, token, password, or key is present.
