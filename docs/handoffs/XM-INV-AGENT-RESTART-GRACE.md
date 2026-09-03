# XM-INV-AGENT-RESTART-GRACE: restart-safe reconcile schedule, rolling reconcile window, and readiness grace for an active rescan

- **status:** implemented and self-tested locally; gates below. Not merged, not pushed, not tagged.
- **branch:** `ai/claude/XM-INV-AGENT-RESTART-GRACE` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `a0efc92`, RC77 line), worktree `K:/发票/wt-XM-INV-RESTART-GRACE`.
- **commits (5, in order):**
  - `044a225` feat(source-agent): persist reconcile/full-scan schedule across restarts (part C)
  - `1633dba` fix(source-agent): rolling reconcile window for the usage economic stream (part B)
  - `0ab1f58` feat(api): readiness grace for an active source economic rescan (part A)
  - `05e0fba` docs: XM-INV-AGENT-RESTART-GRACE operations notes and handoff
  - a follow-up commit widening the part A activity window after a production-data correction (see
    "Post-delivery correction" below) -- hash filled in once committed.

## Production problem (verified before this work started, magnitude corrected after -- see below)

`deploy/roll-forward.sh` recreates the source-agent containers on every deploy. In
`agents/sourceagent/runner.go:98` (pre-fix), `lastReconcile`/`lastFull` were local variables in
`SyncRunner.Run`, reset to zero on every process start. `modeAt` (`runner.go:179-190`) therefore
made the first cycle after *any* restart `ScanReconcile` for Sub2API (or `ScanFull` for New API),
regardless of whether one was actually due.

Worse, in `agents/sourceagent/economics_db.go:194-202` (pre-fix), `prepareCursor` rewound a new
`ScanReconcile` cycle's `PositionCursor` all the way back to the cutover manifest
(`canonicalDomainCursor(domains, cutoverPositions)`) -- and this was not restart-specific: **every**
`ScanReconcile` cycle did this, including the routine one every `SOURCE_RECONCILE_INTERVAL` (6h,
`deploy/docker-compose.sources.yml:24`), restart or not. The cutover manifest position
(`c.Manifest.HighWaters[c.Stream].Cursor`) is captured exactly once, at cutover time, into a
create-only encrypted state file (`agents/sourceagent/cutover.go:297` `CaptureCutover`, reading
`usage_cursor` from a single read-only snapshot transaction) and never advances afterward. So this
was not "rescans the whole 4M-row table every time" -- it rescans **everything since cutover**, a
window whose size grows by one more day's worth of usage every day that passes without this fix,
regardless of restarts. Per `agents/sourceagent/economics_db.go:118-132` (pre-fix numbering), the
stream watermark only advances when the whole cycle completes (`!hasMore` at the end of `Scan`), so
for the entire duration of any given cycle:

- Backend readiness (`evaluateSourceStreamHealth`, `backend/internal/postgresstore/source_sync.go`)
  reported `ECONOMIC_WATERMARK_STALE` once `SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS` (15m,
  `backend/cmd/api/runtime.go`) elapsed, and `/readyz` returned 503 for as long as the rescan ran.
- Every Sub2API funding lot was shown as `source_unavailable`
  (`backend/internal/application/service.go` `ListFundingLots`/eligibility summaries, both gated on
  `stream.Ready`), and invoice submission was refused with `SOURCE_NOT_READY`/`SOURCE_GAP`-adjacent
  reasons for the same duration.

`deploy/docker-compose.sources.yml` sets `SOURCE_RECONCILE_INTERVAL: 6h` for both sources; per the
above, the periodic reconcile *also* rewinds to cutover, independent of restarts -- confirmed by
reading `prepareCursor` (the branch that resets `PositionCursor` does not check "is this a
restart", only "is this a new cycle and is the mode Full or Reconcile").

### Post-delivery correction (production data, after the first four commits landed)

The original task brief estimated the rescan at "~4M rows, ~1000 pages x 100 rows per ~6-minute
cycle, roughly 5 hours" -- an estimate, not a direct measurement, and it read as "rescans the whole
table." The team lead relayed the actual production observation for the restart-triggered cycle
this fix addresses (Sub2API usage, source cutover 2026-08-25): started 06:31Z, published complete
06:56Z, 3,327 batches, roughly 330k records -- about **25 minutes**, not 5 hours, and bounded to the
~9 days of usage since cutover, not the full source table. Both corrections are exactly what the
code above (cutover manifest = one fixed, non-advancing position) predicts, and part B's fix -- start
from the previous reconcile's baseline instead of the fixed cutover position -- is unaffected by the
correction: it is the right fix regardless of the window's absolute size, and it is what keeps this
window from continuing to grow, day over day, once deployed.

What the correction *did* change: part A's activity-window sizing (`economicRescanActivityMaxAge`,
`backend/cmd/api/runtime.go`), which was originally derived only from the agent's own page-to-page
pacing (`2*SOURCE_POLL_INTERVAL + SOURCE_ECONOMIC_SAFETY_DELAY`, 7 minutes) -- comfortably covering
gaps *while the agent is actively sending pages*, but not accounting for the separate gap after the
agent's last page lands (`source_economic_scan_cycles.cycle_status` flips `receiving`->`processing`)
and before the backend's own projection workers finish processing every event in the cycle and
publish it, during which nothing touches that row's `updated_at` at all. The observed 25-minute
cycle doesn't reveal how that time split between the two phases, so the fix (see "Files changed"
below) adds a separate, generously-margined `economicRescanProcessingTailAllowance` (30 minutes) for
that second gap, sized to exceed the entire observed cycle even under the worst-case assumption that
all 25 minutes were motionless in `processing`, while staying well inside `SOURCE_RECONCILE_INTERVAL`
(6h) so a genuinely stalled cycle is still caught same-day. New total: `2*SOURCE_POLL_INTERVAL +
SOURCE_ECONOMIC_SAFETY_DELAY + economicRescanProcessingTailAllowance` = 2m + 5m + 30m = 37 minutes.

## Design and what changed, by part

### Part C -- persist the reconcile/full-scan schedule (`agents/sourceagent/`)

`ScheduleState{LastReconcileAt, LastFullAt string}` (RFC3339Nano UTC, empty = never) is persisted
in the *same* JSON envelope file as the cursor and sequence (`fileStateEnvelope`, new
`last_reconcile_at`/`last_full_at` fields, `state_store_file.go`), via a new `FileScheduleStore`
sharing that file's mutex and process lock with the existing `FileCursorStore`/`FileSequenceStore`
-- no new file, no new lock, no new schema-version bump (so an old `state.json` decodes the two new
fields as empty strings and behaves exactly as before, once, then persists going forward -- this is
the literal "load unchanged" requirement).

`SyncRunner` gained two new fields: `Schedule ScheduleStore` (nil keeps today's in-memory-only
behavior, unchanged -- every existing test that constructs a bare `&SyncRunner{...}` still passes
without modification) and `OnScheduleError func(error)` (purely observational; a load or save
failure never stops the runner, it only falls back to the safe pre-existing behavior of forcing
one more cycle on the next restart). `Run` loads the schedule once at start and saves it after each
completed reconcile/full cycle (`runner.go`).

Wired into `agents/cmd/source-agent-prod/main.go`'s `runFromEnvironment`:
`Schedule: sourceagent.FileScheduleStore{State: state}` (the same `state` already used for
`FileCursorStore`/`FileSequenceStore`), plus an `OnScheduleError` log line.

### Part B -- rolling reconcile window for the usage stream (`agents/sourceagent/economics_db.go`)

Two new `ScanCursor` fields (`types.go`):

- `ReconcileBaselineCursor string` -- the domain-cursor position where the last **completed**
  `ScanReconcile` cycle finished. Set in `Scan` at the moment a reconcile cycle completes
  (`!hasMore && req.Mode == ScanReconcile`), to `next.WatermarkCursor` (equal to `next.CeilingCursor`
  at that point -- the ceiling this cycle proved complete).
- `ReconcileWindowBounded bool` -- true once a cycle's starting position was computed by this new
  logic. Used only to detect and abandon a **legacy in-flight** reconcile cycle left by a
  pre-upgrade binary (see below); it has no other effect and does not need resetting once true.

`prepareCursor` now branches by mode explicitly instead of a combined `Full || Reconcile` check:

- `ScanFull` (New API only; Sub2API never produces this mode) is **unchanged**: still rewinds to
  the cutover manifest. Full scans are full by design.
- `ScanReconcile` now calls a new pure helper, `reconcileWindowStart(domains, baseline, watermark,
  cutoverPositions)`: prefer `baseline` (the previous reconcile's recorded end point); if empty or
  it fails to parse against the current domain set, fall back to the live `watermark` (i.e. no
  rewind -- for the very first reconcile ever, watermark already equals the cutover cursor, so this
  reduces to the original behavior exactly once); if that also fails to parse, fall back to
  `cutoverPositions`. The result is always clamped up to the cutover floor per domain, defensively.
- `StreamCredits` is completely untouched: its branch (reset to zero on every new cycle, regardless
  of mode) is checked first in the switch and is structurally unreachable from the `ScanReconcile`
  branch above. This is why credits' "restart at zero every cycle" behavior needed no test changes
  and no code changes at all -- confirmed by reading `reconcile_test.go` and `balance_delta_test.go`
  first, as instructed: neither file exercises `EconomicDBConnector.prepareCursor`'s credits branch
  at all (`reconcile_test.go` only exercises the wholly separate V2 `FileReconciler` with a
  `"payments"` stream fixture; `balance_delta_test.go` only exercises `BalanceDBConnector`'s delta
  reconciliation, a third, also-separate code path that does not use `SyncRunner`'s
  Reconcile/Full/Incremental mode at all -- it is driven purely by `cursor.Completed`). Both were
  re-run unmodified and pass.

**Legacy in-flight cycle on upgrade.** `shouldAbandonLegacyReconcileCycle(cursor, newCycle, mode,
stream)` returns true exactly when: the cycle is in flight (`!newCycle`, i.e. `Completed=false`),
`mode == ScanReconcile`, `stream == StreamUsage`, and `!cursor.ReconcileWindowBounded`. When true,
`prepareCursor` treats it the same as starting a fresh cycle (recomputing the ceiling/horizon from
the rolling-window baseline) instead of resuming the stale, rewound-to-cutover position. This is
reported via `ScanPage.Warnings = ["legacy_reconcile_cycle_abandoned"]`, which the existing
`OnCycle` log callback in `main.go` already prints (`warnings=%q`) -- no new logging plumbing
needed.

**Upgrade behavior on first deploy of this fix (explicit, as requested):** the persisted schedule
(part C) is *also* new, so a restart into this build with no prior schedule recorded will still
force one `ScanReconcile` cycle (schedule absent = same as before). If that restart happens while
an old-binary rescan is genuinely still in flight on disk (`Completed=false`,
`ReconcileWindowBounded` absent because the field didn't exist), the new binary detects and
abandons it, restarting from the rolling-window baseline (empty the first time under this code, so
falling back to the live watermark) instead of resuming the old rewound-to-cutover position. Net
effect on the very first deploy: still one forced reconcile cycle (from part C's absent schedule),
but it is now a *bounded* rolling-window reconcile instead of a full rescan from cutover, because
part B's abandonment logic recomputes the starting position under the new rules regardless of
whether part C's schedule was present. From the second reconcile onward, both parts apply fully.

### Part A -- readiness grace for an active rescan (`backend/`)

`SourceStreamHealth` gained `ActiveRescanUpdatedAt time.Time` (`postgresstore/source_sync.go`): the
`updated_at` of that (source, stream)'s `source_economic_scan_cycles` row still in
`receiving`/`processing` status, if any. `SourceFreshnessPolicy` gained
`EconomicRescanActivityMaxAge time.Duration` (zero disables the grace entirely, the safe default).

`evaluateSourceStreamHealth`: when the pre-existing `ECONOMIC_WATERMARK_STALE` condition is met
(stream is not `identities`, watermark exists but exceeds the budget), a new helper
`economicRescanActivityWithinWindow(activeAt, policy)` decides whether to emit the new, named,
non-fatal reason `ECONOMIC_RESCAN_ACTIVE` instead. `item.Ready` computation changed from
`len(Reasons)==0` to "every reason is in a small non-fatal set" (currently just this one reason) --
so `Ready` itself flips back to true, not just a readyz-layer overlay. This matters because
`ListFundingLots`/`ListEligibilitySummaries` (`application/service.go`) read `stream.Ready` directly
and never go through `validateSourceRuntimeReadiness` (that function is readyz-only). That function
was still updated (mirroring its existing `EVENTS_PENDING`-alone tolerance) so it does not reject
its own new legitimate input as internally inconsistent.

The activity window is derived, not independently configured:
`economicRescanActivityMaxAge := economicRescanActivityPollWindows*sourcePollInterval +
economicSafetyDelay + economicRescanProcessingTailAllowance` in `backend/cmd/api/runtime.go` (2
minutes + 5 minutes + 30 minutes = 37 minutes) -- both `economicRescanActivityPollWindows` (2) and
`economicRescanProcessingTailAllowance` (30m) are named constants this budget is built from (per
the brief's "never a hard-coded magic number without a named constant"); see the "Post-delivery
correction" section above for why the window has two separate terms and how the second one is
calibrated. Threaded through `application.Options` ->
`Service.sourceEconomicRescanActivityMaxAge` -> `sourceFreshnessPolicy()`. It is **not** part of the
existing all-or-nothing 3-value freshness bundle check in `NewService` (heartbeat/watermark/
identities) -- it is independently optional, and zero (absent) is always safe (grace never
applies, identical to pre-fix behavior), so a deployment that has not been updated to set it simply
gets no grace, not a startup failure.

New SQL: both `SourceHealth`'s and `SourceReadinessHealth`'s queries gained
`LEFT JOIN source_economic_scan_cycles sesc ON ... AND sesc.cycle_status IN
('receiving','processing')`. This is bound by the existing partial unique index
`source_economic_one_active_scan_cycle` (migration 0009) -- at most one row can ever match per
(source, stream) -- so `SourceReadinessHealth`'s documented bounded-cost guarantee for `/readyz`
(see its doc comment) is preserved; a test (`TestSourceHealthQueryJoinsScanCyclesThroughTheActivePartialIndex`)
asserts the join predicate matches the index's `WHERE` clause exactly.

## Deviation from the brief, flagged explicitly

The brief named `cycle_status='processing'` specifically for the grace condition. Reading
`source_sync.go`'s `CommitSourceBatch` (the INSERT/UPSERT into `source_economic_scan_cycles`) shows
a cycle only reaches `'processing'` once its **final** page has landed (`final_sequence` set, i.e.
`ScanComplete=true` from the agent) -- during the bulk of a multi-hour `SOURCE_RECONCILE_INTERVAL`
rescan (agents send ~1000 pages per ~6-minute outer cycle before hitting
`SOURCE_MAX_PAGES_PER_CYCLE` and yielding), the row is still `'receiving'`, with `updated_at` bumped
on every page the receiver accepts. Granting the grace only for `'processing'` would leave
`/readyz` (and the five-stream gate) failed for nearly the entire rescan -- the exact opposite of
what this fix is for. I therefore treat both `'receiving'` and `'processing'` as "active"; `'blocked'`
(and no row at all) still never counts and always fails closed, matching "a processing cycle that
stopped updating beyond the window stays not-ready." This is implemented, tested (unit and
integration, both cycle statuses covered by the same code path since the SQL predicate lists both),
and documented in `docs/PRODUCTION-RUNBOOK.md` and `docs/CONFIGURATION.md`.

No other deviations from the brief.

## Files changed

- `agents/sourceagent/state_store_file.go` -- `fileStateEnvelope` schedule fields, `ScheduleState`,
  `ScheduleStore`, `FileScheduleStore`; `validateStoredFileCursor`/`validateStoredFileCursorForStream`
  extended for the two new usage-only `ScanCursor` fields.
- `agents/sourceagent/state_store_file_test.go` -- new tests (legacy-file-loads-unchanged,
  durability, sharing the envelope safely with cursor/sequence writes).
- `agents/sourceagent/runner.go` -- `Schedule`/`OnScheduleError` fields, load/save wiring in `Run`.
- `agents/sourceagent/runner_test.go` -- new tests (resumes persisted schedule, persists on
  completion end-to-end across two runner instances, load-failure falls back safely, nil `Schedule`
  keeps pre-existing behavior).
- `agents/cmd/source-agent-prod/main.go` -- wires `FileScheduleStore` + `OnScheduleError` into the
  production `SyncRunner`; `buildVersion` bumped `"0.3.0"` -> `"0.3.1"`.
- `agents/sourceagent/types.go` -- `ScanCursor.ReconcileBaselineCursor`/`ReconcileWindowBounded`.
- `agents/sourceagent/economics_db.go` -- `shouldAbandonLegacyReconcileCycle`,
  `reconcileWindowStart`, `prepareCursor` restructured into an explicit per-mode switch, `Scan`
  records the reconcile baseline on completion and reports the abandonment warning.
- `agents/sourceagent/economics_db_test.go` (new) -- pure unit tests for both extracted helpers,
  every required scenario.
- `agents/cmd/source-agent-prod/economic_bridge_semantics_integration_test.go` -- new end-to-end
  test against the real Postgres bridge SQL (gated on `SOURCE_AGENT_TEST_DATABASE_URL`, same as the
  file's existing tests) proving the rolling window, legacy abandonment, and unaffected credits
  semantics together.
- `backend/internal/postgresstore/source_sync.go` -- `SourceFreshnessPolicy.EconomicRescanActivityMaxAge`,
  `SourceStreamHealth.ActiveRescanUpdatedAt`, `economicRescanActiveReason`,
  `nonFatalStreamHealthReasons`, `economicRescanActivityWithinWindow`, `evaluateSourceStreamHealth`
  changes, both SQL queries' new join.
- `backend/internal/postgresstore/source_sync_test.go` -- new pure tests for the grace window's
  boundaries and the join's index-bound predicate.
- `backend/internal/postgresstore/source_sync_integration_test.go` -- new end-to-end test driving
  the real `CommitSourceBatch` write path.
- `backend/internal/application/service.go` -- `Options`/`Service` new field, `sourceFreshnessPolicy()`
  updated, bounds-checked independently of the existing 3-value bundle.
- `backend/internal/application/rescan_grace_integration_test.go` (new) -- application-layer
  end-to-end test: `ListFundingLots` stays actionable during an active rescan, becomes
  `source_unavailable` once it stalls.
- `backend/cmd/api/runtime.go` -- `economicRescanActivityPollWindows`/
  `economicRescanProcessingTailAllowance` constants, activity-window derivation, `Options` wiring,
  `validateSourceRuntimeReadiness` extended
  (`readySourceStreamAllowedReasons`/`notReadySourceStreamAllowedReasons`/`reasonsWithinSet`/
  `containsReason`).
- `backend/cmd/api/main_test.go` -- new test for the extended `validateSourceRuntimeReadiness`
  tolerance, every combination.
- `docs/CONFIGURATION.md`, `docs/PRODUCTION-RUNBOOK.md` -- updated as described above.

## Version bump: every place `SOURCE_AGENT_VERSION`/`agent_version` "0.3.0" is referenced

Bumped in this branch: `agents/cmd/source-agent-prod/main.go:30` (`var buildVersion = "0.3.1"`).

**Not edited by this agent** (per the brief: report exact lines, do not edit the release gate
scripts; extended the same caution to the release-identity env template since it is part of the
same release-ceremony, owned by the RC pipeline, not this feature branch):

- `scripts/release-image-gate.ps1:10` -- `[string]$SourceAgentVersion = '0.3.0',` (default parameter
  value).
- `scripts/release-image-gate.ps1:252` -- `-BuildArguments @('SOURCE_AGENT_VERSION=' +
  $SourceAgentVersion)` (consumes the parameter above; no literal to change here, but confirms the
  parameter default at line 10 is the thing to bump).
- `scripts/verify.ps1:186` -- `SOURCE_AGENT_VERSION = '0.3.0'` (the full-source-tree gate's own
  fixture/expected value).
- `deploy/.env.production.example:116` -- `SOURCE_AGENT_VERSION=0.3.0` (the release env template
  RC-bump ceremony updates alongside the release identity commit).

All four must move to `0.3.1` together with (or as part of) the next RC's release-identity bump
that includes this branch's changes, the same way past agent-version bumps in this repository have
been handled by that ceremony rather than by the feature branch itself.

## Gates

All commands run from `K:/发票/wt-XM-INV-RESTART-GRACE`. `agents/` uses
`SOURCE_AGENT_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_grace?sslmode=disable`;
`backend/` uses `INVOICE_TEST_DATABASE_URL` with the same DSN. `HTTP(S)_PROXY`/`ALL_PROXY` unset
before every Go command per the documented Windows toolchain quirk.

| Gate | Command | Result |
| --- | --- | --- |
| agents build | `go build ./...` (in `agents/`) | exit 0 |
| agents vet | `go vet ./...` | exit 0 |
| agents gofmt | `gofmt -l` on every changed file, CRLF stripped first (see note below) | clean |
| agents tests | `go test -count=1 ./...` (with the DB URL set) | `ok` all 3 packages (1 has no tests) |
| backend build | `go build ./...` (in `backend/`) | exit 0 |
| backend vet | `go vet ./...` | exit 0 |
| backend gofmt | same CRLF-stripped check on every changed file | clean |
| backend full suite | `go test -p 1 -count=1 ./...` (with the DB URL set) | `ok` all packages, two runs: first run had one transient loopback-timeout failure in `cmd/eligibility-repair` (`dial tcp 127.0.0.1:55432: ... did not properly respond`), confirmed a flake by rerunning that package alone (pass) and then rerunning the *entire* suite again (pass, including that package, in 100% green) |
| gitleaks | `gitleaks git --no-banner --log-opts="a0efc92..HEAD" .` | `no leaks found`, 3 commits scanned |

**gofmt note:** `gofmt -l .` in this repository reports essentially every `.go` file, changed or
not, because the local checkout has `core.autocrlf=true` and gofmt writes LF while the working tree
has CRLF. Confirmed this is purely a checkout artifact, not a real formatting problem: `git show
HEAD:<any untouched .go file>` (LF, as stored) is already gofmt-clean, and `scripts/verify.ps1` does
not invoke gofmt at all. Every file this branch touches was verified gofmt-clean after stripping
`\r` before comparison. Recorded in memory (`windows-toolchain-quirks`) for future agents so this
does not need re-discovering.

## What was not verified

- **No dedicated DB-fixture integration test for `EconomicDBConnector.Scan`'s rolling window
  beyond what `economic_bridge_semantics_integration_test.go`'s new test covers.** That test does
  exercise the real bridge SQL end to end (two successive reconcile cycles, a simulated legacy
  in-flight cycle, and the credits stream), which is the highest-value scenario; it does not attempt
  every combination of domain-cursor edge cases the pure unit tests already cover deterministically.
- **`agents/cmd/source-agent-prod`'s existing `TestSub2APIBridgePreservesFinancialAndCutoverSemantics`/
  `TestNewAPIBridgePreservesTransitionSafetyAndConfigurationDrift`** were re-run unmodified against
  the real bridge SQL and pass, confirming no regression in `EconomicDBConnector`/`BalanceDBConnector`'s
  other, unrelated semantics -- but they were not specifically designed to catch a part-B regression,
  so they are corroborating evidence, not the primary proof (the new test above is).
- **The version-bump ceremony's downstream effects** (rebuilding/retagging the `source-agent` image,
  re-running `release-image-gate.ps1` with the bumped default, updating the release identity commit)
  were not performed -- out of scope for a feature branch per the brief and this repository's
  established RC-pipeline ownership.
- **No production/staging deployment or roll-forward rehearsal** was performed; this is a
  worktree-only change, not pushed, tagged, or merged.
