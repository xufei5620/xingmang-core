sprint-section: 7.5

# XM-DS1 deterministic follow-up · pure accumulator

## status

READY-FOLLOWUP 7346a2a29396066a1a5c9cd953e3fabf3af901de

This correction follows the merged DS1 implementation (`5b182a3` / release
`f6a13c6`). It removes the accumulator's wall-clock side effect and does not
change schema, policy, writer cadence, API, or deployment behavior.

## branch / commit / base

- branch: `ai/codex/XM-DS1-deterministic-followup`
- worktree: `K:/星芒统一控制平台/wt-xmDS1-deterministic`
- base: `release/v0.1-launch@7346a2a29396066a1a5c9cd953e3fabf3af901de` (latest release tip read immediately before work)
- implementation commit: recorded on the final READY-FOLLOWUP line after commit
- source issue: `DailyAccumulator` assigned `time.Now().UTC()` in `mergeSample` and `Merge`

## scope / summary

- Removed both `time.Now().UTC()` assignments from
  `internal/platform/ops/rollup.go`.
- `DailyAccumulator.AggregatedAt` now remains zero during pure accumulation;
  persistence/calling code owns the processing timestamp. Equal samples therefore
  produce deeply equal accumulator values and no wall-clock dependency.
- Added `internal/platform/ops/rollup_deterministic_followup_test.go`, which runs
  the same input twice (with a delay to defeat coarse clock resolution) and asserts
  `reflect.DeepEqual`.
- No migrations, generated SQL, policy bytes, shared runtime wiring, credentials,
  staging/production access, or raw-delete behavior changed.

## files_changed

```text
internal/platform/ops/rollup.go
internal/platform/ops/rollup_deterministic_followup_test.go
docs/handoffs/slices/XM-DS1-deterministic-followup.md
```

## tests_run

- TDD RED (before code change):
  `go test -p 1 ./internal/platform/ops -run '^TestDailyAccumulatorDeterministicAcrossRuns$' -count=1 -v`
  failed as expected, showing distinct `AggregatedAt` timestamps.
- GREEN: same targeted command after removing wall-clock writes — PASS.
- `go test -p 1 ./internal/platform/ops ./internal/platform/jobs` — PASS.
- `go test -race -p 1 ./internal/platform/ops ./internal/platform/jobs` — PASS.
- `go vet ./internal/platform/ops ./internal/platform/jobs` — PASS.
- `git diff --check` — PASS.
- `D:\Git\bin\bash.exe scripts/check-governance.sh` with
  `GOVERNANCE_BASE_REF=release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1` — PASS.
- `gitleaks git --redact --no-banner --log-opts='7346a2a..HEAD'` — PASS, no leaks.

## tests_not_run

- No PostgreSQL harness or migration tests: this is a pure function/test-only
  correction with no SQL/schema changes.
- No frontend, Docker, staging, production, upstream, credential, API, worker,
  retention, or deployment commands; deploy is explicitly **not required**.

## risks / decisions

- `AggregatedAt` is intentionally zero until a persistence layer sets it; callers
  must not infer processing time from a pure accumulator result.
- DS1's existing CR-0002 invoice exclusion, 16-key registry handling, and DBR1
  boundary remain unchanged.

## follow_ups

1. Acceptance line reviews this follow-up and may append a `MERGED` line; no
   `deploy-local.sh` run is needed.
2. Future DS2 persistence code should set `aggregated_at` at commit time, outside
   the pure accumulator.
