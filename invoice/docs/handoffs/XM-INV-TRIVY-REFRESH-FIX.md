# XM-INV-TRIVY-REFRESH-FIX: Trivy cache refresh script no longer seeds a cache Trivy itself distrusts

- **status:** fixed and self-tested end to end, including one full real
  db+java-db refresh against a disposable volume with both mandatory self-
  checks passing. Not deployed against the real shared cache volume, no
  release ceremony run, no production access.
- **branch:** `ai/claude/XM-INV-TRIVY-REFRESH-FIX`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `cadf009` (tag `v0.1.0-rc72-signed`),
  worktree `K:/发票/wt-XM-INV-TRIVYFIX`.
- **commits:** see `git log ai/claude/XM-INV-TRIVY-REFRESH-FIX` (this
  handoff file lands last).

## Summary

`scripts/refresh-trivy-cache.ps1` seeded a Trivy cache volume that looked
correct (valid `trivy.db`/`trivy-java.db`, parseable `metadata.json`, and a
real vulnerability scan against it succeeded) but that Trivy's own
`--download-db-only`/`--download-java-db-only` -- exactly what
`release-image-gate.ps1` runs, unconditionally, at the start of every
release -- rejected as corrupted and tried to redownload over the network.
Reproduced verbatim, matching the incident: `Trivy DB may be corrupted and
will be re-downloaded`. Inside the gate's own container, with no proxy
configured there, that unwanted redownload hung until timeout, which is
what lost release RC72's first attempt and left `java-db/` empty on the
real shared volume.

## Root cause

Trivy's `trivy-db`/`trivy-java-db` OCI artifacts (`mirror.gcr.io/aquasec/
trivy-db:2`, `.../trivy-java-db:1`) ship `metadata.json` with a
`DownloadedAt` field set to Go's zero time value, `"0001-01-01T00:00:00Z"`
-- confirmed directly by extracting the real, currently-published layer.
That field only has meaning to whichever client performs a real download;
Trivy's own downloader (`pkg/db.(*Client).updateDownloadedAt`, confirmed by
`strings`-ing the pinned 0.74.0 binary) stamps it with the real completion
time immediately after a successful download. `refresh-trivy-cache.ps1`
copied the upstream file byte-for-byte, so every cache it seeded kept the
zero `DownloadedAt`.

That zero value is invisible to a scan run under `--skip-db-update
--skip-java-db-update` (confirmed directly: a full vulnerability scan
against a zero-`DownloadedAt` cache succeeds normally), which is exactly
why the dispatch's own specified self-check recipe (`--skip-db-update
--skip-java-db-update --offline-scan`) does **not** by itself catch this
defect -- proven directly in the new test fixture and worth flagging
explicitly since it means that check alone would not have caught this
incident. It is very much *not* invisible to `--download-db-only` (no skip
flags), which is the literal command `release-image-gate.ps1` runs every
release before ever reaching a skip-mode scan: it treats a zero
`DownloadedAt` as "may be corrupted" and redownloads unconditionally.

## Fix

1. **Stamp a real `DownloadedAt`.** New `Set-TrivyDbMetadataDownloadedAt`
   (`refresh-trivy-cache-lib.ps1`) overwrites `DownloadedAt` with the actual
   seed time, exactly as Trivy's own downloader does, before anything is
   ever written to a volume. Confirmed directly: `--download-db-only`
   against a correctly-stamped cache is a silent, sub-second no-op (0.72s,
   zero log output, exit 0) -- the "gate normally finds an already-fresh
   cache" contract the script's own doc comment already promised.
2. **Stage before touching the live cache.** `refresh-trivy-cache.ps1` no
   longer publishes a refreshed component straight into the live volume.
   It now clones the live volume into a disposable staging volume
   (`Copy-TrivyCacheVolumeToVolume`), layers this run's refreshed
   component(s) on top, and only promotes to the live volume after both
   self-checks below pass. A failed self-check leaves the live volume
   completely untouched (verified directly: an induced self-check failure
   left the live test volume empty).
3. **Two mandatory self-checks against staging**, both new library
   functions:
   - `Invoke-TrivyCacheOfflineScanSelfCheck` -- the check specified in the
     dispatch: `trivy image --skip-db-update --skip-java-db-update
     --offline-scan` against a small already-local image (reuses `-SeedImage`
     rather than pin a second image). Proves the cache is actually usable.
   - `Invoke-TrivyCacheFreshnessSelfCheck` -- **added beyond the dispatch's
     literal spec**, flagging this explicitly per this session's own
     "flag deviations inline" practice: since the offline-scan check alone
     does not catch a regressed/missing `DownloadedAt` (previous
     paragraph), this runs the literal `--download-db-only`/`--download-
     java-db-only` commands `release-image-gate.ps1` itself runs and fails
     if Trivy ever prints "may be corrupted". Proven directly, both ways:
     passes (fast, no-op) against a correctly-stamped cache, and correctly
     fails when reseeded with the raw pre-fix (zero-`DownloadedAt`) content
     -- reproducing, and this time catching, the incident's actual root
     cause. Trivy 0.74 rejects `--download-db-only` and `--download-java-
     db-only` in one invocation ("options can not be specified both" --
     found only by running this end to end with java-db included), so this
     runs them as two separate calls, matching how `release-image-gate.ps1`
     itself already does it.

## Other bugs found and fixed only by actually running this end to end

Per this session's own working practice, real reproduction over guessing:

1. **GNU tar misparses a Windows drive-letter path as a remote host.** Git
   for Windows' own `tar.exe` (resolves ahead of Windows' bundled `bsdtar`
   on `PATH` in this shell) treats `C:\Users\...` or `K:\...` as a
   `host:file` remote-archive reference and tries to `rsh` to a host
   literally named `C`/`K` -- reproduced directly: `tar (child): Cannot
   connect to C: resolve failed`, `gzip: stdin: unexpected end of file`.
   `Expand-TrivyCacheTarGz` now detects which `tar` actually resolved (via
   its own `--version` banner -- Windows' `bsdtar` has no such remote-
   archive feature at all and errors out immediately if given the GNU-only
   `--force-local` flag) and passes `--force-local` plus forward-slash-
   normalized paths (GNU tar's argument handling separately mangles a
   literal backslash path, the same class of bug `New-TrivyCacheCurlConfig`
   already worked around for curl) only for the GNU build.
2. **Trivy 0.74's own downloader can silently produce an empty database
   directory.** Found by the team lead while reseeding the real shared
   volume during this same investigation: Trivy downloads into `$TMPDIR`
   (defaulting to the container's own `/tmp`) then moves the result into
   `--cache-dir`; when `--cache-dir` is a mounted volume the move crosses
   filesystems and can fail silently, leaving no `db`/`java-db` directory
   behind while still logging success. `Invoke-TrivyCacheFreshnessSelfCheck`
   points `TMPDIR` at a directory on the same volume (`Copy-
   TrivyCacheVolumeToVolume` always creates `tmp/` at the destination root)
   so a real redownload triggered by that self-check does not hit this.
   **Not fixed in `release-image-gate.ps1` itself** -- out of scope for this
   branch (only `refresh-trivy-cache*` and its test were in scope), flagged
   here since it is a latent risk in the real gate's own `--download-db-
   only`/`--download-java-db-only` steps independent of this fix.
3. **Docker-from-Git-Bash MSYS path conversion.** Also found by the team
   lead: `docker run ... --cache-dir /root/.cache/trivy ...` invoked from
   Git Bash without `MSYS_NO_PATHCONV=1` gets its POSIX-looking arguments
   silently rewritten to Windows paths, producing a false "first run cannot
   skip downloading DB" that looks like cache corruption but is actually a
   shell-quoting artifact. Only affects ad hoc verification from Git Bash;
   `refresh-trivy-cache.ps1`'s own docker calls run from PowerShell and are
   unaffected. Documented here since it cost real time misdiagnosing this
   fix's own verification steps.

## Files changed

- `scripts/refresh-trivy-cache-lib.ps1` -- `Set-TrivyDbMetadataDownloadedAt`
  (new), `Copy-TrivyCacheVolumeToVolume` (new), `Invoke-
  TrivyCacheOfflineScanSelfCheck` (new), `Invoke-TrivyCacheFreshnessSelfCheck`
  (new), `Expand-TrivyCacheTarGz` (tar-flavor detection + `--force-local` +
  forward-slash paths).
- `scripts/refresh-trivy-cache.ps1` -- new `-TrivyImage`/
  `-SelfCheckImageReference` parameters; per-component loop now stamps
  `DownloadedAt` and defers publishing (collects `$pendingComponents`
  instead of seeding immediately); new post-loop staging + two-self-check +
  promote sequence, guarded by the same `-WhatIf`/`ShouldProcess` contract
  as before; staging volume always cleaned up in a `finally`, live volume
  untouched on any self-check failure.
- `scripts/test-refresh-trivy-cache.ps1` -- pure fixture for `Set-
  TrivyDbMetadataDownloadedAt` (overwrite-existing and add-when-absent);
  live fixtures for `Copy-TrivyCacheVolumeToVolume`, both self-check
  functions (happy path against a corrected cache, and the regression case
  proving the freshness check catches what the offline-scan check misses).

**Not touched:** `RELEASE-READINESS.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`scripts/release-image-gate.ps1`, `scripts/release-image-gate-lib.ps1`,
`scripts/test-release-image-gate.ps1`, `scripts/verify-release-image-
artifacts.ps1`, `scripts/run-detached*.ps1`, any release/deploy/RC file, any
migration, any tag, any production or server connection, the real shared
`invoice-release-gate-trivy-0-74-0` volume.

## Tests run

All against disposable `trivy-refresh-test-*` Docker volumes (deleted
before this report; none left over -- confirmed via `docker volume ls`) or
this test file's own throwaway `invoice-release-gate-trivy-0-74-0-testonly-
*` volume (self-cleaned in its own `finally`). The real shared
`invoice-release-gate-trivy-0-74-0` volume and its lock were never touched
by any of this.

```
pwsh -NoProfile -Command "[scriptblock]::Create((Get-Content -Raw <script>))"
    # parses clean for all three touched .ps1 files

pwsh -NoProfile -File scripts\test-refresh-trivy-cache.ps1
    # exit 0, "All refresh-trivy-cache fixtures passed." Includes every
    # existing fixture (unaffected) plus: Set-TrivyDbMetadataDownloadedAt
    # pure fixtures; a live confirmation that the real trivy-db:2 upstream
    # artifact still ships DownloadedAt as Go's zero value; Copy-
    # TrivyCacheVolumeToVolume (digest sidecar + metadata.json clone
    # correctly, tmp/ created); Invoke-TrivyCacheOfflineScanSelfCheck and
    # Invoke-TrivyCacheFreshnessSelfCheck both passing against a correctly-
    # staged volume; the regression pair proving the offline-scan check
    # alone passes against a raw zero-DownloadedAt cache while the
    # freshness check correctly fails on it; the pre-existing CLI -WhatIf
    # and idempotent (Action=unchanged) end-to-end checks, unaffected.

pwsh -NoProfile -File scripts\test-run-detached.ps1
    # exit 0, "All run-detached fixtures passed." -- unaffected by this
    # branch, run anyway per the gate list.

pwsh -NoProfile -File scripts\test-release-image-gate.ps1
    # exit 0, "Release image gate offline/static fixtures passed." --
    # release-image-gate.ps1 itself is unmodified.

gitleaks detect (zricethezav/gitleaks:latest) against this branch's commits
    # no findings

Manual end-to-end verification beyond the test file, against disposable
volumes (trivy-refresh-test-1/2/3, all deleted after):
  - Reproduced the incident verbatim: seeding via the pre-fix script, then
    running the real gate's exact `trivy image --download-db-only --no-
    progress` command against it, printed "Trivy DB may be corrupted and
    will be re-downloaded" (docker exit 0 after a live redownload that
    completed in ~2m23s here; inside the gate's actual container, with no
    proxy, this is what hangs to timeout).
  - One full real end-to-end refresh with BOTH databases (db 110.3 MiB,
    java-db 915 MiB, the one java-db download this task's bandwidth budget
    allowed): downloaded, digest-verified, staged, both self-checks passed,
    both components promoted to the (disposable) live volume. Final layout
    confirmed Trivy-native: `db/trivy.db` + `db/metadata.json`
    (DownloadedAt `2026-09-02T19:30:03Z`), `java-db/trivy-java.db` +
    `java-db/metadata.json` (DownloadedAt `2026-09-02T19:30:11Z`). A real
    `trivy image --skip-db-update --skip-java-db-update --offline-scan`
    against that final volume succeeded (real CVE findings against
    `postgres:18.6-alpine`, including Go-stdlib CVEs, exercising both
    databases).
  - One real self-check failure induced during development (the `--
    download-db-only`/`--download-java-db-only`-combined-in-one-call bug,
    caught by this same full end-to-end run before the fix): confirmed the
    live volume was left completely empty afterward, proving the "no
    promotion on self-check failure" guarantee holds under a real failure,
    not just in a synthetic test.

## Not run

- Anything against the real shared `invoice-release-gate-trivy-0-74-0`
  volume or its lock (explicitly out of scope; team lead is operating on
  that volume directly).
- A real release-image-gate.ps1 ceremony run (that file is unmodified and
  its own test suite already covers it; out of scope for this branch).
- Backend/web test suites (no Go or web files touched).

## Risks / follow-ups

- **`release-image-gate.ps1`'s own `--download-db-only`/`--download-java-
  db-only` steps do not set `TMPDIR` onto the cache volume** (see "Other
  bugs found" #2 above) -- a latent risk independent of this fix, in a file
  out of this branch's scope. Worth a follow-up there.
- `Invoke-TrivyCacheFreshnessSelfCheck`'s default 45s timeout is a
  judgment call (bounds a regression's redownload attempt without letting
  the self-check itself hang on the same flaky proxy that caused the
  original incident); tune with its `-TimeoutSeconds` if a future
  known-slow environment needs longer.
- This fix does not change `release-image-gate.ps1` itself in any way;
  the improvement only takes effect once this refresh script is what last
  seeded the shared cache volume ahead of a release.
