# XM-INV-AGENT-CREDITS-RECONCILE-FIX: the credits stream's periodic reconcile permanently failed after 0.3.1

- **status:** implemented and self-tested locally; gates below all green. Not merged, not pushed, not
  tagged.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (HEAD `65d2098`, RC82 line), worktree
  `K:/发票/wt-XM-INV-AUTOLOGIN`.
- **commit:** a single commit on this branch (`fix(agent): stamp the reconcile baseline cursor only
  on the usage stream (credits reconcile loop)`), containing the fix, the regression tests, the
  agent version bump, and this doc. See `git show --stat` on that commit for the exact file list.

## Incident summary

**2026-09-03 14:58Z**, first credits-stream reconcile under agent build `0.3.1` (shipped by
`XM-INV-AGENT-RESTART-GRACE`, RC82 roll-forward) failed, and every retry since has failed the same
way. Discovered during the RC82 roll-forward's own verify step, which checks each economic stream's
watermark freshness.

- The credits watermark went stale starting at its last successful advance, **14:52Z**
  (`SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS` = 15m, so `/readyz` began returning 503 for the credits
  stream at 15:07Z and has stayed there since).
- The Sub2API **payments** stream's reconcile completed successfully at **14:59Z** the same day --
  unaffected, see "Blast radius" below for why.
- Interim production mitigation applied by the team lead: rewrote `last_reconcile_at` in the
  credits stream's `state.json` (the `FileScheduleStore`-persisted schedule from
  `XM-INV-AGENT-RESTART-GRACE` part C) forward, to defer the next forced reconcile until this fix
  ships. This does not repair the stale watermark; it only stops the agent from immediately
  re-entering the failing loop on its next scheduled reconcile before the fix is deployed.

## Root cause

`agents/sourceagent/economics_db.go`'s `EconomicDBConnector.Scan` (shared by the usage and credits
streams) recorded a `ReconcileBaselineCursor` on every completed (`!hasMore`) page whenever
`req.Mode == ScanReconcile`, regardless of which stream was scanning:

```go
if req.Mode == ScanReconcile {
    next.ReconcileBaselineCursor = next.WatermarkCursor
}
```

This is exactly right for the **usage** stream -- it is the `XM-INV-AGENT-RESTART-GRACE` part B
rolling reconcile window, and the field exists precisely so a periodic reconcile resumes from the
previous one's endpoint instead of rewinding to the cutover manifest every time.

It is meaningless, and actively harmful, for the **credits** stream. Credits resets its
`PositionCursor` to zero on every new cycle regardless of mode (`prepareCursor`'s
`case c.Stream == StreamCredits` branch, checked first and unconditionally, before the
`ScanReconcile` branch that sets the baseline is ever reached) -- so credits never had, and never
needed, a rolling-window baseline to resume from. Nothing reads `ReconcileBaselineCursor` on the
credits path.

The stored-cursor validator, `validateStoredFileCursorForStream` in
`agents/sourceagent/state_store_file.go` (line ~498, added by `XM-INV-AGENT-RESTART-GRACE`),
enforces that invariant explicitly:

```go
if streamID != StreamUsage && (cursor.ReconcileBaselineCursor != "" || cursor.ReconcileWindowBounded) {
    return errors.New("stored cursor carried usage-only reconcile window metadata")
}
```

So the very first completed credits `ScanReconcile` page under `0.3.1` produced a `CursorAfter` this
validator rejects. That rejection surfaces before the batch is ever committed to durable state: the
runner first tries to spool the pending batch
(`EncryptedFilePendingStore.SaveIfAbsent` -> `validatePending` in
`agents/sourceagent/pending_spool.go`), which calls the same validator on `pending.CursorAfter` and,
on any error there, collapsed it to the single generic message:

```go
if err := validateStoredFileCursorForStream(pending.CursorAfter, s.StreamID); err != nil || pending.CursorAfter.Revision != pending.CursorBefore.Revision {
    return errors.New("pending batch cursor transition is invalid")
}
```

Every retry recomputes the same illegal cursor and fails the same way -- a permanent loop, not a
transient error. The batch is never spooled, never acknowledged, and the credits watermark never
advances past 14:52Z.

## Blast radius: which streams could hit this

Only two streams route through `EconomicDBConnector.Scan`, the connector with the bug --
`agents/cmd/source-agent-prod/main.go`'s `buildDBConnector` wires connectors per stream:

| Stream | Connector | At risk? |
| --- | --- | --- |
| `usage` | `EconomicDBConnector` | No -- this is the field's intended, correct use |
| `credits` | `EconomicDBConnector` | **Yes -- this incident** |
| `payments` | `PaymentV3DBConnector` (`payment_v3.go`) | No -- different type, different `Scan`/`prepare` methods; never references `ReconcileBaselineCursor` at all |
| `balances` | `BalanceDBConnector` (`economics_db.go`, but a distinct struct/`Scan`) | No -- driven entirely by `cursor.Completed` and a durable snapshot/delta cycle, not `ScanMode`; never references `ReconcileBaselineCursor` |
| `identities` | `IdentityDBConnector` (`identity_db.go`) | No -- unrelated connector entirely |

**Why payments succeeded at 14:59Z the same day:** it isn't that payments happens to complete in one
page, or takes a different multi-page path through the same buggy code -- it is a structurally
separate connector. `PaymentV3DBConnector.prepare` (payment_v3.go:159-164) still rewinds fully to the
cutover manifest on every `ScanFull`/`ScanReconcile` cycle (the pre-`XM-INV-AGENT-RESTART-GRACE`
behavior, never changed for payments) and has no `ReconcileBaselineCursor` field-setting logic in it
at all. Read to confirm, not guessed.

## Fix

1. `agents/sourceagent/economics_db.go`: extracted the baseline-stamping decision into a small pure
   helper, `reconcileBaselineCursorAfterCompletion(mode ScanMode, stream, completedWatermarkCursor string) string`,
   mirroring the existing style of `shouldAbandonLegacyReconcileCycle`/`reconcileWindowStart`. It
   returns the completed watermark cursor only for `mode == ScanReconcile && stream == StreamUsage`,
   and `""` for every other stream/mode combination (including credits). `Scan` now always calls it
   on cycle completion instead of conditionally assigning inline, so a non-usage stream's field is
   explicitly cleared rather than merely "not written this time" (defensive: it was never legitimately
   non-empty for credits in the first place). Comment updated to explain the usage-only invariant and
   name this incident.
2. `agents/sourceagent/pending_spool.go` `validatePending`: the `CursorAfter` validation branch now
   wraps the underlying cause with `%w` instead of discarding it, while keeping the exact prefix
   `"pending batch cursor transition is invalid"` (checked: nothing in `agents/` or `backend/`
   string-matches the old generic message; the only place that constructs it is this line). The
   revision-mismatch check was split out into its own `if`, since it has no underlying cause to wrap.
   Result: `pending batch cursor transition is invalid: stored cursor carried usage-only reconcile
   window metadata` -- this exact message, with the cause visible in the log, would have made this
   incident's root cause obvious from the first failed retry instead of requiring source-reading to
   diagnose.
3. `agents/cmd/source-agent-prod/main.go`: `buildVersion` bumped `"0.3.1"` -> `"0.3.2"`. No test in
   `agents/` asserts the literal string `"0.3.1"` (checked via grep), so nothing else needed updating
   in this repository. Per the established convention (see `XM-INV-AGENT-RESTART-GRACE`'s handoff),
   the release-tooling mirrors (`scripts/release-image-gate.ps1`, `scripts/verify.ps1`,
   `deploy/.env.production.example`) are **not** edited by this change -- that is the release-identity
   ceremony's responsibility, not this feature commit's.

No other files changed. This is deliberately the smallest possible fix: it does not touch
`prepareCursor`'s credits branch (already correct and untouched by this bug), does not touch
`reconcileWindowStart` or `shouldAbandonLegacyReconcileCycle` (usage-only helpers, unaffected), and
does not touch `PaymentV3DBConnector`, `BalanceDBConnector`, or any backend code.

## Tests added (`agents/sourceagent`)

All new tests were written first, confirmed to fail (the first three failed to compile --
`reconcileBaselineCursorAfterCompletion` did not exist yet -- and would have failed at the assertion
had the helper merely stubbed empty; the fourth failed with `pending spool accepted a credits cursor
carrying usage-only reconcile baseline metadata` under the pre-fix message), then made to pass by the
implementation above.

- `TestReconcileBaselineCursorAfterCompletion` (`economics_db_test.go`) -- table test over every
  `(mode, stream)` combination for both usage and credits: usage records the baseline only on
  `ScanReconcile`, credits never records one regardless of mode. This is requirement (b) (usage
  behavior unchanged) and half of (a) (credits never stamps it) in one table.
- `TestCreditsReconcileCompletionCursorPassesStoredCursorValidation` (`economics_db_test.go`) --
  ties the helper directly to the actual regression signature: builds a V2 cursor via the existing
  `testV3Cursor` fixture, applies the helper, and asserts
  `validateStoredFileCursorForStream(cursor, StreamCredits)` returns no error. This is requirement
  (a)'s second half.
- `TestUsageReconcileCompletionCursorPassesStoredCursorValidation` (`economics_db_test.go`) -- the
  usage counterpart, proving the recorded baseline still passes stored-cursor validation (it must,
  usage is the one stream the validator allows it on).
- `TestValidatePendingRejectsCreditsCursorCarryingReconcileBaseline` (`pending_spool_test.go`) --
  requirement (c): builds a real encrypted pending-spool store, a valid V3 credits batch (new test
  helper `creditEventProjectionForTest`, following the existing `candidateProjectionForTest`
  pattern), and a `CursorAfter` carrying the historical bug's illegal
  `ReconcileBaselineCursor`. Asserts `SaveIfAbsent` fails with a message that both starts with the
  exact prefix `"pending batch cursor transition is invalid"` and contains the underlying cause
  `"stored cursor carried usage-only reconcile window metadata"`. This is the test that most directly
  reproduces the production failure mode.

## Gates

All commands run from `K:/发票/wt-XM-INV-AUTOLOGIN/agents` (and `backend/` for the cross-module
check), with `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` unset per the documented Windows toolchain quirk.

| Gate | Command | Result |
| --- | --- | --- |
| agents gofmt | `gofmt -l` on every changed file, CRLF stripped first (see note below) | clean |
| agents vet | `go vet ./...` | exit 0, no output |
| agents tests | `go test -count=1 ./...` | `ok` for `cmd/source-agent-prod`, `cmd/source-keygen`, `sourceagent`; `cmd/source-agent` has no test files |
| backend build | `go build ./...` | exit 0 |
| backend/agents import check | `grep -rl "invoice-system/agents" backend/**/*.go` | no matches -- backend does not import agents |

**gofmt note:** this checkout has `core.autocrlf=true`; `gofmt -l`/`-w` on the real CRLF files either
falsely reports every file or (if `-w` is run directly) strips CRLF to LF. Every changed file was
checked by stripping `\r` into a scratch copy first, per the workflow recorded in this repository's
`windows-toolchain-quirks` memory. One file (`economics_db_test.go`) needed an actual alignment fix
inside the new table-test map literals; after `gofmt -w` was run (on the real file, inadvertently
converting it to LF), its CRLF endings were restored byte-for-byte before committing, and the exact
same CRLF-stripped gofmt check was re-run clean.

## Deviations from the brief

None. The one open question the brief flagged (whether anything compares the full
`"pending batch cursor transition is invalid"` string) was checked by grep across `agents/` and
`backend/`: nothing does. The prefix-plus-`%w` approach was applied as specified.

## Verification plan (after deploy)

1. Deploy agent build `0.3.2`. The persisted `last_reconcile_at` mitigation means the credits stream
   will not immediately force a reconcile; either wait for the next scheduled
   `SOURCE_RECONCILE_INTERVAL` (6h) cycle, or force one out-of-band per the existing operational
   runbook.
2. Confirm the next credits reconcile cycle completes: its final page has `ScanComplete=true` and
   the batch's `mode` metadata is `"reconcile"` (i.e. it ran as `ScanReconcile`, not silently
   downgraded).
3. Read the stored credits cursor (via the agent's read-only check-state command,
   `FileStateStore.CheckReadOnly`) and confirm `reconcile_baseline_cursor` is empty/absent in the
   persisted JSON -- this is the exact field this fix stops stamping.
4. Confirm `/readyz` and the credits stream's watermark both recover (watermark advances past the
   cycle's completion time, `ECONOMIC_WATERMARK_STALE` clears).
5. Confirm no repeat of the `"pending batch cursor transition is invalid"` log line for the credits
   stream across at least one full subsequent reconcile cycle.
