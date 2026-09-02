# XM-INV-GATE-TMPDIR: same-volume TMPDIR for the gate's own Trivy downloads, plus a daily refresh Scheduled Task

- **status:** both tasks implemented and verified offline/statically end to
  end. Not run: the real release image gate, and any live change to the
  shared `invoice-release-gate-trivy-0-74-0` Docker volume or Windows Task
  Scheduler.
- **branch:** `ai/claude/XM-INV-GATE-TMPDIR`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `4c72520` (which already contains
  XM-INV-TRIVY-REFRESH-FIX), worktree `K:/发票/wt-XM-INV-GATE-TMPDIR`.
- **commits:**
  - `821d34c` -- `fix(release-gate): keep Trivy download TMPDIR on the cache volume`
  - `26acaae` -- `feat(scripts): register daily Trivy cache refresh task`
  - this handoff file lands last, in its own commit.

## Summary

Two independent pieces of PowerShell release tooling, no Go/web changes.

**Task A.** `scripts/release-image-gate.ps1` runs Trivy's own
`--download-db-only`/`--download-java-db-only` against the shared, live
`invoice-release-gate-trivy-0-74-0` volume at the start of every release,
whenever that cache is stale. Trivy 0.74 downloads into `$TMPDIR`
(defaulting to the container's own `/tmp`) and only then moves the result
into `--cache-dir`; when `--cache-dir` is a mounted Docker volume, as it
always is here, that move crosses filesystems and can silently fail,
leaving no `db`/`java-db` directory behind while still logging success.
This exact gap was found and flagged as a follow-up (out of that branch's
scope) by the team lead while reseeding the real shared volume during the
XM-INV-TRIVY-REFRESH-FIX investigation -- see
`docs/handoffs/XM-INV-TRIVY-REFRESH-FIX.md`, "Other bugs found" #2, and
`RELEASE-READINESS.md`'s counterpart risk note. This branch closes that
follow-up in the gate itself.

**Task B.** New `scripts/register-trivy-refresh-task.ps1` registers, or
updates in place, a Windows Scheduled Task (`InvoiceTrivyCacheRefresh`)
that runs `scripts/refresh-trivy-cache.ps1` once daily at 05:30 local
time, so an operator no longer has to run that refresh by hand ahead of a
release.

## Task A: live-volume TMPDIR fix

`release-image-gate-lib.ps1` gains three new pure functions (all taking
explicit parameters only, no ambient script state, consistent with every
other function already in that file):

- `Assert-SafeTrivyCacheContainerDirectory` -- validates the one container
  path these functions accept before it is ever embedded into a shell
  command or a docker `-e` value (mirrors `refresh-trivy-cache-lib.ps1`'s
  own `Assert-SafeTrivyCacheSubPath`/`Assert-SafeOciDigestForShell`
  "validate before shelling out" convention).
- `Get-TrivyCacheDownloadArguments` -- identical to `release-image-
  gate.ps1`'s own `Get-TrivyArguments` (`--rm`, docker socket mount, cache
  volume mount, Trivy image, then `Command`) but also sets `TMPDIR` to
  `<mounted-cache-dir>/tmp`.
- `New-TrivyCacheTmpDirectoryDockerArguments` /
  `New-TrivyCacheTmpDirectoryCleanupDockerArguments` -- `mkdir -p` /
  `rm -rf` that same `tmp` directory directly against the live cache
  volume, reusing the already-pulled Trivy tool image with its entrypoint
  overridden to `sh` (the same "`--entrypoint` override for a utility
  shell command against an already-pulled image" pattern the gate already
  uses for its Keycloak runtime-pruning proof, confirmed directly: the
  official Trivy image is Alpine-based with a real `/bin/sh` -- not
  re-verified by executing a real container in this branch, since the gate
  itself was not run; flagged under Risks below).

`release-image-gate.ps1` itself:

- Adds one script variable, `$trivyCacheContainerDirectory =
  '/root/.cache/trivy'`, as the single source of truth for the container-
  side mount path (previously duplicated inline only inside
  `Get-TrivyArguments`, which now references the variable -- behavior
  unchanged).
- Before the two download calls: creates the `tmp` directory on the live
  `$TrivyCacheVolume` (`New-TrivyCacheTmpDirectoryDockerArguments`).
- Both download calls now build their docker arguments via
  `Get-TrivyCacheDownloadArguments` instead of `Get-TrivyArguments`, wrapped
  in a `try` block.
- A `finally` block always runs
  `New-TrivyCacheTmpDirectoryCleanupDockerArguments` afterward, whether or
  not the downloads succeeded.
- Every other Trivy invocation in the file (`--version`, the per-image
  scan, the per-image SBOM generation) is untouched and still goes through
  the original `Get-TrivyArguments`.

**Not changed:** the gate's exit-code protocol (42 = pending canary
remains exit 42; every other failure remains exit 1), the release
manifest schema (`tools.scriptSha256`/`librarySha256` etc. are still
computed dynamically and just reflect the new file contents), the release
identity strings (`releaseName`, image tags, the `0.1.0-rc72` literals),
and `scripts/verify-release-image-artifacts.ps1` (untouched, not opened).
The literal `'--timeout', '15m'` and `--download-db-only`/`--download-
java-db-only`/`--no-progress` tokens are preserved exactly, so the
pre-existing `test-release-image-gate.ps1` count check on that literal
(`-lt 4`) still passes unchanged.

New fixtures in `test-release-image-gate.ps1` (offline/static, no Docker):

- `Get-TrivyCacheDownloadArguments` for both `--download-db-only` and
  `--download-java-db-only`: asserts the exact, ordered docker argument
  array, including `-e TMPDIR=/root/.cache/trivy/tmp` and the cache-volume
  mount.
- `New-TrivyCacheTmpDirectoryDockerArguments` /
  `-CleanupDockerArguments`: asserts the exact `mkdir -p`/`rm -rf`
  argument arrays, including the `--entrypoint sh` override.
- All three reject an unsafe container directory (missing leading slash,
  shell metacharacters) with the reviewed error message.
- Static source checks against the gate's own text confirm it defines the
  single-source-of-truth directory variable, routes both download calls
  through the new arguments builder, creates the tmp directory beforehand,
  and a structural regex confirms both download calls sit inside a
  `try { ... } finally { ... New-TrivyCacheTmpDirectoryCleanupDockerArguments ... }`
  block.

## Task B: daily refresh Scheduled Task

New `scripts/register-trivy-refresh-task-lib.ps1` (mirrors the existing
`-lib.ps1` split pattern):

- `Get-InvoiceTrivyRefreshTaskArguments` -- builds the exact `pwsh`
  argument string (`-NoProfile -ExecutionPolicy Bypass -File "<fully
  qualified refresh-trivy-cache.ps1 path>"`); rejects a non-fully-
  qualified path.
- `New-InvoiceTrivyRefreshTaskDefinition` -- builds the
  Action/Trigger/Settings/Principal via `New-ScheduledTaskAction`/
  `-Trigger`/`-SettingsSet`/`-Principal` (confirmed directly in this
  environment: these four cmdlets are pure, local `CimInstance`
  constructors -- none of them opens a session against, or otherwise
  touches, the real Task Scheduler service; only `Get-`/`Register-`/
  `Set-`/`Unregister-ScheduledTask` do that). Daily trigger at
  `-StartTime` (default `05:30`), `LogonType S4U`, `RunLevel Limited`.
- `Get-LocalTimeOfDayFromTaskTriggerStartBoundary` -- parses a trigger's
  `StartBoundary` (confirmed directly: a plain UTC ISO-8601 string, e.g.
  `2026-09-02T21:30:00Z`, not a `DateTime`) back to local `HH:mm`, so a
  test can assert the configured time regardless of the machine's
  timezone or the date the check happens to run on.

New `scripts/register-trivy-refresh-task.ps1`:

- `[CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]`, so
  `-WhatIf` and `-Confirm:$false` are both available for free.
- `-RepoRoot` defaults to the script's own repo root (`Split-Path -Parent
  $PSScriptRoot`); resolves and requires `scripts\refresh-trivy-
  cache.ps1` to exist under it.
- Resolves `pwsh`'s own full path via `Get-Command` (not a bare `pwsh.exe`
  relying on `PATH` at trigger time) and the current user as
  `$env:USERDOMAIN\$env:USERNAME` for `-UserId`, both overridable.
- Always prints the full task definition (executable, arguments, working
  directory, trigger, principal, settings) before doing anything else --
  satisfies "`-WhatIf` prints the task definition instead of registering"
  without a separate WhatIf-only code path, since printing happens
  whether or not `-WhatIf` was passed.
- Looks up the task by name/path first: if absent, `Register-
  ScheduledTask`; if already present, `Set-ScheduledTask` to update its
  action/trigger/settings/principal in place (preserves Task Scheduler's
  own run history for the task rather than recreating it). Both paths are
  gated behind `$PSCmdlet.ShouldProcess(...)`.
- No secret is ever collected, stored, or embedded anywhere in this
  script; the task's `LogonType` is `S4U` specifically so it can run
  under the current user's identity at 05:30 (an hour nobody is likely to
  be interactively logged in) without Task Scheduler storing a password.
  This does require the target account to hold the "Log on as a batch
  job" user right -- documented inline in the lib file's doc comment and
  in the runbook addition below; not verified against a real Task
  Scheduler registration in this branch (see Risks).
- `RunLevel Limited`, not `Highest` -- the refresh needs no elevation.

New `scripts/test-register-trivy-refresh-task.ps1` (offline/static, dot-
sources only the `-lib.ps1` file, never the main script, never `Get-`/
`Register-`/`Set-`/`Unregister-ScheduledTask`):

- Exact-string fixture for `Get-InvoiceTrivyRefreshTaskArguments`, plus a
  substring check that its output never contains anything looking like a
  credential; rejects a relative path.
- `New-InvoiceTrivyRefreshTaskDefinition` fixtures assert the built
  Action's `Execute`/`Arguments`/`WorkingDirectory`, the Trigger's
  `CimClassName` (`MSFT_TaskDailyTrigger`), `DaysInterval` (1), `Enabled`,
  and local time-of-day (both the `05:30` default and an explicit
  `23:15`), the Settings' `StartWhenAvailable`, and the Principal's
  `UserId`/`LogonType` (`S4U`)/`RunLevel` (`Limited`). Also covers six
  invalid `-StartTime` values and two non-fully-qualified path
  parameters, all rejected.
- Static source checks against `register-trivy-refresh-task.ps1` confirm:
  no credential-collecting construct (`-Password`, `Get-Credential`,
  `ConvertTo-SecureString`, `[PSCredential]`, `SecureString`) appears
  anywhere in the file; `SupportsShouldProcess` is present; the script
  never invokes `refresh-trivy-cache.ps1` directly (only schedules it);
  `TaskName` defaults to `InvoiceTrivyCacheRefresh`; and the register-vs-
  update-in-place branch (`Get-ScheduledTask` lookup, then `Register-
  ScheduledTask`/`Set-ScheduledTask`) is present.

Documented in `docs/PRODUCTION-RUNBOOK.md`'s Trivy section: a new
paragraph appended immediately after the existing "Keeping the Trivy
cache warm ahead of a gate run" paragraph (nothing existing rewritten, no
RC-identity line touched), covering what the task does, the `-WhatIf`/
plain-run examples, how to inspect it (`Get-ScheduledTask
InvoiceTrivyCacheRefresh`, `Get-ScheduledTaskInfo`), and how to remove it
(`Unregister-ScheduledTask`).

## Files changed

- `scripts/release-image-gate-lib.ps1` -- `Assert-
  SafeTrivyCacheContainerDirectory`, `Get-TrivyCacheDownloadArguments`,
  `New-TrivyCacheTmpDirectoryDockerArguments`, `New-
  TrivyCacheTmpDirectoryCleanupDockerArguments` (all new).
- `scripts/release-image-gate.ps1` -- new `$trivyCacheContainerDirectory`
  variable; `Get-TrivyArguments` now references it instead of a duplicated
  literal; the two Trivy download-update lines are replaced with a tmp-
  directory-create call, a `try` block routing both downloads through
  `Get-TrivyCacheDownloadArguments`, and a `finally` block that always
  cleans the tmp directory up.
- `scripts/test-release-image-gate.ps1` -- new fixtures for all three new
  lib functions (happy path, unsafe-directory rejection) plus static
  source-wiring checks against the gate script (new block inserted right
  after the existing `ConvertFrom-TrivyDatabaseTimestamp` fixture).
- `scripts/register-trivy-refresh-task-lib.ps1` (new) -- `Assert-
  RegisterTrivyRefreshTaskPowerShellRuntime`, `Get-
  InvoiceTrivyRefreshTaskArguments`, `New-
  InvoiceTrivyRefreshTaskDefinition`, `Get-
  LocalTimeOfDayFromTaskTriggerStartBoundary`.
- `scripts/register-trivy-refresh-task.ps1` (new) -- the executable
  register/update-in-place script.
- `scripts/test-register-trivy-refresh-task.ps1` (new) -- its offline/
  static fixtures.
- `docs/PRODUCTION-RUNBOOK.md` -- one paragraph appended to the Trivy
  section.

**Not touched:** any Go/web/backend/agents source, `RELEASE-READINESS.md`,
`docs/IMAGE-SCAN-REVIEW.md`, `scripts/verify-release-image-artifacts.ps1`,
`scripts/refresh-trivy-cache.ps1`/`-lib.ps1` (Task A does not change the
refresh script; Task B only calls it by fully qualified path from a
Scheduled Task action, never inline), `scripts/run-detached*.ps1`, any
release/deploy/RC file, any migration, any tag, any production or server
connection, the real shared `invoice-release-gate-trivy-0-74-0` Docker
volume, and the real Windows Task Scheduler.

## Tests run

```
pwsh -NoProfile -Command "[scriptblock]::Create((Get-Content -Raw <file>))"
    # parses clean for all six touched/new .ps1 files:
    # scripts/release-image-gate-lib.ps1, scripts/release-image-gate.ps1,
    # scripts/test-release-image-gate.ps1,
    # scripts/register-trivy-refresh-task-lib.ps1,
    # scripts/register-trivy-refresh-task.ps1,
    # scripts/test-register-trivy-refresh-task.ps1

pwsh -NoProfile -File scripts\test-release-image-gate.ps1
    # exit 0, "Release image gate offline/static fixtures passed." --
    # includes every pre-existing fixture (unaffected) plus this branch's
    # new Task A fixtures. Rerun a second time after the
    # docs/PRODUCTION-RUNBOOK.md edit to confirm the runbook-scanning
    # checks in this file still pass; still exit 0.

pwsh -NoProfile -File scripts\test-refresh-trivy-cache.ps1
    # exit 0, "All refresh-trivy-cache fixtures passed." -- this file is
    # untouched by this branch; run anyway per the gate list. Includes a
    # real end-to-end trivy-db refresh against disposable/scratch volumes
    # (never the real shared volume) as part of its own existing fixture
    # set, per its own documented behavior.

pwsh -NoProfile -File scripts\test-register-trivy-refresh-task.ps1
    # exit 0, "All register-trivy-refresh-task fixtures passed."

pwsh -NoProfile -File scripts\register-trivy-refresh-task.ps1 -WhatIf
    # Run for real (side-effect-free by design): printed the full task
    # definition (Execute=<this machine's real pwsh.exe path>, Arguments=
    # `-NoProfile -ExecutionPolicy Bypass -File
    # "K:\发票\wt-XM-INV-GATE-TMPDIR\scripts\refresh-trivy-cache.ps1"`,
    # WorkingDirectory=the worktree root, daily at 05:30 local,
    # Principal=<this machine's real user>, LogonType=S4U, RunLevel=
    # Limited, StartWhenAvailable=True), then "What if: Performing the
    # operation ... " and exit 0. Confirmed directly afterward that
    # Get-ScheduledTask InvoiceTrivyCacheRefresh found nothing -- -WhatIf
    # registered nothing.

/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="4c72520..HEAD" .
    # see result appended below by the reporting agent
```

## Not run

- The real `scripts/release-image-gate.ps1` end to end (would build all
  nine images and touch the live Trivy cache volume; explicitly out of
  scope/forbidden for this branch).
- A real registration against this machine's actual Task Scheduler
  (`Register-ScheduledTask`/`Set-ScheduledTask` without `-WhatIf`) --
  explicitly out of scope; only the side-effect-free `-WhatIf` path was
  run for real, plus the fully offline static fixtures.
- Backend/web/agents test suites (no Go or web files touched).

## Risks / follow-ups

- **The Trivy image's shell is assumed, not re-verified by execution.**
  `New-TrivyCacheTmpDirectoryDockerArguments`/`-CleanupDockerArguments`
  override the pinned `ghcr.io/aquasecurity/trivy:0.74.0` image's
  entrypoint to `sh` to run `mkdir -p`/`rm -rf` against the live volume.
  This relies on that image being Alpine-based with a real `/bin/sh` --
  true of every publicly documented build of the official Trivy image,
  and the same kind of `--entrypoint` override the gate already uses
  elsewhere (Keycloak runtime pruning), but not re-confirmed here by
  actually running a container, since the real gate was explicitly out of
  scope for this branch. Worth a quick real check
  (`docker run --rm --entrypoint sh <pinned trivy image> -c 'echo ok'`)
  the next time this gate actually runs, or in a follow-up branch that is
  allowed to touch Docker.
- **S4U logon type requires "Log on as a batch job."** If a future
  operator finds `InvoiceTrivyCacheRefresh` never actually fires (check
  `Get-ScheduledTaskInfo InvoiceTrivyCacheRefresh`'s `LastTaskResult`),
  the first thing to check is whether the target account holds that user
  right (`secpol.msc` -> Local Policies -> User Rights Assignment, or
  `Get-ScheduledTask`'s own history). Not something this branch could
  verify without actually registering the task on a real machine, which
  was out of scope.
- This branch does not itself run `register-trivy-refresh-task.ps1`
  without `-WhatIf` anywhere; an operator (or a follow-up change request)
  still needs to actually register the task on whichever machine is meant
  to run the daily refresh.
- Per this session's own "flag deviations inline" practice: the dispatch
  said "highest available run level NOT required," which this branch
  read as "use the default, non-elevated `Limited` run level" (set
  explicitly rather than left to a cmdlet default, so a future change to
  that default cannot silently elevate this task) -- flagging the reading
  explicitly since "not required" could in principle also be read as "no
  opinion, pick either," which was not the interpretation used here.
