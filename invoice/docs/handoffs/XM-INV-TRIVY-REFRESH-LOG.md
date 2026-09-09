# XM-INV-TRIVY-REFRESH-LOG: per-run logging and a distinct exit code for Trivy cache refresh gate contention

- **status:** implemented and self-tested end to end, including a real
  lock-contention run (this test process holding the real shared lock
  while a child `refresh-trivy-cache.ps1` invocation correctly exits 75)
  and a real `-Force`-free registration `-WhatIf` run. Not done: the real
  Scheduled Task was not re-registered (explicitly out of scope), and no
  change was made to the real shared `invoice-release-gate-trivy-0-74-0`
  Docker volume.
- **branch:** `ai/claude/XM-INV-TRIVY-REFRESH-LOG`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `840a2f7` (tag-adjacent RC74 line),
  worktree `K:/发票/wt-XM-INV-TRIVY-LOG`.
- **commits:**
  - `d4bcd86` -- `feat(scripts): log every Trivy cache refresh run and skip cleanly on gate contention`
  - `54ccdfb` -- `feat(scripts): retry the Trivy cache refresh task after a failed or skipped run`
  - `b22c1b2` -- `docs(runbook): document Trivy refresh run logs, latest.json, and exit code 75`
  - this handoff file lands last, uncommitted as of writing (see below).

## Summary

`InvoiceTrivyCacheRefresh` (the daily Scheduled Task registered by
`register-trivy-refresh-task.ps1`) ran on 2026-09-03 05:30 and ended
`LastTaskResult=1` while a release image gate run was holding the same
shared Trivy cache lock at the same time. Nothing was logged anywhere
(Task Scheduler's own operational log was disabled), so the cause could
not be confirmed after the fact -- a manual rerun and an on-demand
`Start-ScheduledTask` both then succeeded, consistent with contention
rather than a real defect, but unproven.

**The lock already exists and needed no new marker.** Read both
`refresh-trivy-cache.ps1` and `release-image-gate.ps1` rather than
guessing: both already take the exact same exclusive file lock
(`release\.trivy-0.74.release-gate.lock`, opened via `[IO.File]::Open(...,
[IO.FileShare]::None)` in `Enter-TrivyReleaseGateLock` /
`refresh-trivy-cache-lib.ps1`, and identically inline in
`release-image-gate.ps1` itself), and `refresh-trivy-cache.ps1` already
fails fast (does not wait) if it is already held, before ever touching
`Test-DockerVolumeExists` or any other volume operation. So the dispatch's
"a lock file, a label, or a running container... read the code, do not
guess" was already answered: a host-side lock file, already shared, already
volume-safe. No new marker was implemented or needed.

## 1. Per-run logging (`refresh-trivy-cache.ps1`)

Wrapped the entire script body (after resolving `-WorkDirectory`, before
the docker/curl availability checks) in a `try { ... } catch { ... }
finally { ... }`:

- **`Start-Transcript`** to `logs\trivy-cache-refresh\runs\<UTC stamp
  (yyyyMMddTHHmmssZ)>-<pid>.log` (same stamp format `run-detached-lib.ps1`'s
  own `New-DetachedRunId` uses, for consistency), capturing all
  `Write-Host`/native-output-through-the-host console output plus the
  transcript's own header/footer (which already includes start/end time).
  `Start-Transcript`/`Stop-Transcript`'s own console announcement lines are
  piped to `Out-Null` so the script's existing console output is
  byte-for-byte unchanged -- confirmed directly (see Tests below).
- The `finally` block appends an explicit footer (start/end time, exit
  code, and on failure the exception message and `$_.InvocationInfo.
  PositionMessage`) directly to the log file, prunes `runs\*.log` down to
  the 30 most recent by `LastWriteTimeUtc`, and writes `runs\latest.json`
  (`started_at`/`finished_at`/`exit_code`/`action_db`/`action_java_db`/
  `error`).
- **`-WhatIf` gotcha found and fixed while testing this end to end:** this
  script's own `-WhatIf` sets `$WhatIfPreference` for its whole scope,
  which `New-Item`/`Start-Transcript`/`Add-Content`/`Set-Content`/
  `Remove-Item` all independently honor and silently no-op under --
  confirmed directly (a `-WhatIf` run produced zero log files before this
  fix). Every one of this logging block's own state-changing calls is now
  pinned `-WhatIf:$false` so a run's own log is always written regardless
  of whether the cache-mutating part of the run is a preview.
- **A second, more consequential bug found only by testing the actual exit
  code, not just the console text:** the script's pre-existing tail line,
  `$global:LASTEXITCODE = 0`, does **not** actually set the real process
  exit code -- confirmed directly with a minimal repro (`$global:
  LASTEXITCODE = 75` with no `exit` statement, invoked via `pwsh -File`,
  still exits 0). It only ever looked like it worked because 0 is also
  what a script returns by default when it runs off the end without
  calling `exit`. This was invisible before because every prior path
  through this script was either a true success (0, matching the default
  by coincidence) or an uncaught throw (1, via PowerShell's own
  terminating-error handling, unrelated to that line). It became load-
  bearing and wrong the moment this branch needed a real 75. Fixed by
  replacing that line with an explicit `exit $exitCode` (matching
  `run-detached.ps1`'s own already-correct `exit 0`/`exit 4` convention).

Lock contention is now caught and handled as a **distinct, non-error
outcome**: the catch block matches the caught exception's message against
the same substring `scripts/test-refresh-trivy-cache.ps1` already asserts
on (`'already using the shared Trivy cache lock'`, from `Enter-
TrivyReleaseGateLock`'s existing throw) and, on a match, sets exit code
**75**, writes `error = 'gate holds the cache volume; skipped'`, and does
**not** rethrow. Every other exception is logged the same way (message +
failing line) and then rethrown unmodified, so the console rendering and
exit code (1) for a genuine failure are exactly what they were before this
branch -- confirmed directly (see Tests).

## 2. Scheduled Task retry (`register-trivy-refresh-task*.ps1`)

`New-InvoiceTrivyRefreshTaskDefinition` gains `-RestartCount` (default 3)
and `-RestartInterval` (default `New-TimeSpan -Minutes 30`), passed to
`New-ScheduledTaskSettingsSet` alongside the existing `-StartWhenAvailable`.
Confirmed directly in this environment: `RestartCount`/`RestartInterval`
are real, supported parameters of this machine's `ScheduledTasks` module,
and Task Scheduler's restart-on-failure is driven by the action's own
non-zero exit code -- which now includes 75, so a run skipped for gate
contention retries later the same day, same as a genuine failure would.
`register-trivy-refresh-task.ps1` exposes both as its own top-level
parameters (same defaults) and prints them in its task-definition summary.

`RestartInterval` must be a positive `TimeSpan` (enforced explicitly,
since `New-ScheduledTaskSettingsSet` does not itself validate this);
`RestartCount` is range-validated `0..10`.

## 3. Docs

Appended to `docs/PRODUCTION-RUNBOOK.md`'s existing Trivy section only
(nothing existing rewritten, no RC-identity line touched, following the
same pattern `docs/handoffs/XM-INV-GATE-TMPDIR.md` already used for its
own addition to this section): the new `-RestartCount`/`-RestartInterval`
parameters, and a new "Reading a refresh run's own log" paragraph covering
the run-log path, `latest.json`'s shape, and what exit codes 0/1/75 mean.

## Files changed

- `scripts/refresh-trivy-cache.ps1` -- per-run logging (`Start-Transcript`
  + explicit footer + pruning + `latest.json`), lock-contention exit 75,
  the `-WhatIf:$false` fixes, and the `exit $exitCode` fix. The unchanged
  body between the lock acquisition and the summary loop was re-indented
  one level (mechanical, via a scoped `sed`, verified with a line-count
  diff and a parse check before and after) to reflect its new nesting;
  no logic in that block changed.
- `scripts/test-refresh-trivy-cache.ps1` -- new fixtures: a successful
  run's log file + `latest.json` content (including the 35-fake-logs-plus-
  one-real-run pruning-to-30 proof), and the exit-75 lock-contention path
  (this test process holds the real shared lock, spawns a real child CLI
  invocation, asserts exit 75 + the skip message + the live test volume's
  digest state is unchanged + both the run log and `latest.json` record
  75). Both new blocks reuse the file's own existing "skip gracefully if
  the real lock is genuinely held by someone else already" convention.
- `scripts/register-trivy-refresh-task-lib.ps1` -- `-RestartCount`/
  `-RestartInterval` on `New-InvoiceTrivyRefreshTaskDefinition`.
- `scripts/register-trivy-refresh-task.ps1` -- same two as top-level
  parameters, passed through; printed summary line updated; doc comment
  updated.
- `scripts/test-register-trivy-refresh-task.ps1` -- default-value fixture
  (3, 30 min), an explicit-override fixture, and rejection fixtures for a
  zero `RestartInterval` and an out-of-range `RestartCount`.
- `docs/PRODUCTION-RUNBOOK.md` -- Trivy section addition described above.

**Not touched:** `scripts/release-image-gate.ps1`/`-lib.ps1` (the lock
mechanism they already contain was read, not modified),
`scripts/test-release-image-gate.ps1` (run anyway, unaffected, still
passes), `scripts/run-detached*.ps1` (read for its stamp/run-directory
conventions, which this branch's logging reuses, but the file itself is
unmodified), `scripts/verify-release-image-artifacts.ps1`,
`RELEASE-READINESS.md`, `docs/IMAGE-SCAN-REVIEW.md`, any release/deploy/RC
file or identity string, any migration, any tag, any production or server
connection, the real shared `invoice-release-gate-trivy-0-74-0` Docker
volume, and the real Windows Task Scheduler (the one pre-existing
`InvoiceTrivyCacheRefresh` registration found on this machine during
testing was only ever inspected and previewed via `-WhatIf`, never
registered/updated for real).

## Tests run

```
pwsh -NoProfile -Command "[scriptblock]::Create((Get-Content -Raw <file>))"
    # parses clean for all five touched .ps1 files: refresh-trivy-cache.ps1,
    # register-trivy-refresh-task-lib.ps1, register-trivy-refresh-task.ps1,
    # test-refresh-trivy-cache.ps1, test-register-trivy-refresh-task.ps1

pwsh -NoProfile -File scripts\test-refresh-trivy-cache.ps1
    # exit 0, "All refresh-trivy-cache fixtures passed." Includes every
    # pre-existing fixture (unaffected, incl. the real end-to-end trivy-db
    # download/digest/extract/seed/self-check chain and the pre-existing
    # -WhatIf/idempotent CLI checks against a disposable test volume) plus
    # this branch's two new live fixtures: (a) a successful run's log file
    # + latest.json content, and 35 synthetic pre-seeded run logs pruned
    # down to 30 after one more real run; (b) this test process holding
    # the real shared release-gate lock while a real child CLI invocation
    # exits 75, prints the documented skip message, leaves the live test
    # volume's digest state unchanged, and records 75 in both its own run
    # log and latest.json. Never touches the real invoice-release-gate-
    # trivy-0-74-0 volume -- only disposable trivy-refresh-test-*/testonly-*
    # volumes, cleaned up in their own finally blocks.

pwsh -NoProfile -File scripts\test-register-trivy-refresh-task.ps1
    # exit 0, "All register-trivy-refresh-task fixtures passed." Includes
    # every pre-existing fixture plus new RestartCount/RestartInterval
    # fixtures (default value via XmlConvert.ToTimeSpan on the CimInstance's
    # ISO-8601 duration string, explicit override, zero-interval rejection,
    # out-of-range count rejection).

pwsh -NoProfile -File scripts\test-release-image-gate.ps1
    # exit 0, "Release image gate offline/static fixtures passed." --
    # release-image-gate.ps1 itself is unmodified; run anyway per the gate
    # list, confirms no regression from sharing its lock file.

pwsh -NoProfile -File scripts\register-trivy-refresh-task.ps1 -WhatIf
    # Run for real (side-effect-free by design). Found, and left
    # untouched, a real pre-existing InvoiceTrivyCacheRefresh registration
    # on this machine -- printed "Update ... in place", Settings now shows
    # StartWhenAvailable=True, RestartCount=3, RestartInterval=PT30M.
    # Confirmed via Get-ScheduledTask afterward that the real task's
    # settings were not touched (this was -WhatIf).

Manual end-to-end verification beyond the two test files (both against
disposable state, cleaned up afterward):
  - A minimal repro proving $global:LASTEXITCODE = N alone (no `exit`)
    does not change a script's real process exit code -- the bug behind
    the "exit $exitCode" fix above.
  - Two independent real-process lock-contention repros (a background
    holder + a separate contender process, then the same shape against
    the real refresh-trivy-cache.ps1 + a real held lock) confirming a
    second process cannot acquire the same exclusive lock file and that
    refresh-trivy-cache.ps1 now exits 75 in that case (0 before the
    exit-statement fix, then correctly 75 after it).
  - A real generic-failure run (bad registry hostname) confirming exit 1,
    a populated run log with the exception message and failing line, and
    latest.json's error field populated with exit_code=1 -- the "every
    other error rethrows unchanged" contract.

gitleaks git --no-banner --log-opts="840a2f7..HEAD" .
    # "3 commits scanned", "no leaks found"
```

## Not run

- Anything against the real shared `invoice-release-gate-trivy-0-74-0`
  volume, or a real (non -WhatIf) registration/update of the real
  `InvoiceTrivyCacheRefresh` Scheduled Task -- both explicitly out of
  scope; the acceptance line re-registers it.
- A real `release-image-gate.ps1` ceremony run (unmodified by this
  branch; its own static test suite already covers it and was rerun here
  as a regression check only).
- Backend/web/agents test suites (no Go or web files touched).

## Risks / follow-ups

- **Native command output (raw docker/curl stdout, not already captured
  into a PowerShell variable) is not guaranteed to land in the transcript
  file** when the process's own stdout is not a real console (confirmed
  directly: a native command's output was invisible in a transcript file
  when the parent pwsh's own stdout was redirected, exactly the case for
  a detached or Scheduled Task run). In practice this script already
  routes nearly all docker/curl output through `$output = & docker ...
  2>&1` and only prints via `Write-Host` after parsing it, so this gap is
  believed narrow (mainly curl's own download-progress feedback during an
  interactive manual run) -- not independently re-verified against a real
  full download-and-seed run's log for completeness, since every CLI test
  in this branch deliberately exercises the already-current (fast, no
  download) path to avoid re-downloading the ~110 MiB/915 MiB databases
  again. Worth a spot check next time a real refresh actually downloads
  something.
- **`New-ScheduledTaskSettingsSet -RestartCount 0` is allowed** (a caller
  can explicitly disable the retry) but was not exercised against a real
  Task Scheduler registration -- only inspected via the CimInstance
  settings object directly.
- This branch's logging/exit-code changes take effect the next time
  `refresh-trivy-cache.ps1` runs (manually, detached, or scheduled); nothing
  here re-registers the real Scheduled Task, so the real
  `InvoiceTrivyCacheRefresh`'s `RestartCount`/`RestartInterval` stay at
  whatever they were (`StartWhenAvailable` only, no restart policy) until
  the acceptance line's real (non -WhatIf) registration run picks this up.
