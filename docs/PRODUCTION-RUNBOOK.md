# Production launch runbook

Target: `invoice.solov.cc` on the currently reachable `fiberstate` host.

This document is executable procedure, not authorization. Stop before every
step marked **production change approval** until the operator confirms the
exact hostname, files, CIDRs and credentials. Never patch, mount or vendor code
inside Sub2API or New API.

## 1. Required decisions and values

The following values must exist before a public launch:

- actual issuing entity name (administrator-only);
- minimum amount, initially at least CNY 200.00;
- dedicated QQ/enterprise QQ sender plus authorization code, or Gmail sender
  plus Google App Password;
- administrator public `/32` or `/128` CIDRs and a separate VPN/bastion
  break-glass CIDR;
- central OIDC provider. The supplied reference deployment is Keycloak 26.7.2
  at `auth.solov.cc`, with administration isolated at
  `auth-admin.solov.cc`; an existing standards-compliant IdP may replace it;
- TLS certificates for both Keycloak hostnames, plus one exact administrator
  `/32` or `/128` and an independent exact break-glass VPN/bastion route;
- one UUID `source_instances.id` for Sub2API and one for New API;
- New API payment-evidence procedure with at least three authorized finance
  operators (proposer, distinct approver, and independent issuer);
- age backup recipient and offline private identity holder.

Do not put any value above into Git except the non-secret CIDR/settings example
after replacing it with a host-local untracked file.

## 2. Frozen current upstream facts

Recheck immediately before launch; these observations are from 2026-08-20/21:

- FiberState Sub2API container: `sub2api-mig`, runtime `0.1.179`, commit
  `75f88be5f75c27771836b586f7de1503afa0e3bc` (rechecked 2026-08-21
  16:18 Asia/Shanghai; the container image label remains stale);
- Sub2API Compose directory: `/root/sub2api-mig`; PostgreSQL container:
  `sub2api-mig-postgres`;
- New API container: `new-api`, image `v1.0.0-rc.25`, commit
  `f116414284162ad15d8925f7bca494c109b83e93`;
- New API Compose directory: `/root/new-api`; PostgreSQL container: `postgres`;
- Sub2API OIDC is disabled; PKCE, ID-token validation and verified-email
  requirements are also currently false;
- Sub2API session binding is enabled. The invoice integration does not reuse
  its JWT, so this does not enter the invoice authentication chain;
- read-only aggregate currency audit on 2026-08-21: 2,930 payment orders, all
  provider snapshots at schema version 2 but without a currency key; all use
  provider key `easypay` with payment type `alipay`, which v0.1.179's audited
  currency function fixes to CNY. No order/user-level value was exported;
- host reverse proxy is BT/OpenResty Nginx at
  `/www/server/nginx/conf/nginx.conf`; vhosts are under
  `/www/server/panel/vhost/nginx`;
- current disk and memory capacity are ample, but capacity must still be
  rechecked before image pulls and ClamAV database initialization.

The integrity gate must show all four research trees at their recorded clean
commits before and after every release:

```powershell
pwsh -NoProfile -File .\scripts\check-upstream-integrity.ps1
$upstreamIntegrityExit = $LASTEXITCODE
if ($upstreamIntegrityExit -ne 0) { throw "upstream integrity failed with exit $upstreamIntegrityExit" }
```

## 3. Build and release gates

Run locally from the exact RC92 candidate worktree
`K:\发票\wt-XM-INV-AUTOLOGIN`:

```powershell
pwsh -NoProfile -File .\scripts\verify.ps1
$sourceGateExit = $LASTEXITCODE
if ($sourceGateExit -ne 0) { throw "RC92 full source gate failed with exit $sourceGateExit" }
```

The default gate includes Go race/vet tests, frontend production builds,
dependency audit, agent tests, isolated PostgreSQL migrations and concurrency
tests, runtime-role immutability checks, Compose validation and upstream
integrity.

RC49 is a failed historical candidate. The signed tag
`v0.1.0-rc49-signed` remains fixed at
`eb7b4365d3af30241debe7b1a054b7eed8b94dcd`; its
`release/0.1.0-rc49-exact1`, `release/0.1.0-rc49-exact2`, and
`release/0.1.0-rc49-exact3` are retained failure evidence and must be treated
as read-only. Their exact file set and hashes are anchored by
`docs/RC49-FAILURE-EVIDENCE-SHA256SUMS.txt`; never reuse, rename, edit, or
transfer them as RC92 evidence.

RC50 is also failed historical evidence. `v0.1.0-rc50-signed` remains fixed at
`d08b3a2e40e55f7f600759c250f45b16b82bd0e1`; the interrupted
`release/0.1.0-rc50-exact1` has no manifest and is anchored by
`docs/RC50-FAILURE-EVIDENCE-SHA256SUMS.txt`. Treat it as read-only and never
resume, reuse, rename, edit, transfer, or move its signed tag.

RC51 is also failed historical evidence. `v0.1.0-rc51-signed` remains fixed at
`229ca5bea04e9fa8384fa308e342fcf5f6b6332f`; exact1 completed all builds,
scans, SBOMs and manifest generation but failed final verification when
PowerShell converted the two reviewed ISO date strings to `DateTime`. Its
complete file set is anchored by `docs/RC51-FAILURE-EVIDENCE-SHA256SUMS.txt`.
Treat it as read-only and never resume, reuse, rename, edit, transfer, or move
its signed tag.

RC52 is also failed historical evidence. `v0.1.0-rc52-signed` remains fixed at
`adf152771e779e6a1bd7a95b3e628d5fa85c3b3f`; exact1 stopped in source
verification because the isolated PostgreSQL 15 host port could not become
reachable through local host NAT. Its one-file set is anchored by
`docs/RC52-FAILURE-EVIDENCE-SHA256SUMS.txt`. Treat it as read-only and never
resume, reuse, rename, edit, transfer, or move its signed tag.

Release tooling requires PowerShell 7.5 or newer because every production JSON
parse uses `ConvertFrom-Json -DateKind String` behind the centralized duplicate-
checking parser.

Before copying images to the server, begin from and verify the clean signed RC92
source commit and annotated tag.  The tag must peel to the signed source commit
used by the gate; do not create or move it after image evidence exists:

```powershell
$expectedWorktree = (Resolve-Path 'K:\发票\wt-XM-INV-AUTOLOGIN').Path
$worktreeLines = @(git rev-parse --show-toplevel)
$worktreeExit = $LASTEXITCODE
if ($worktreeExit -ne 0 -or -not [string]::Equals(($worktreeLines -join '').Trim(), $expectedWorktree, [StringComparison]::OrdinalIgnoreCase)) { throw 'wrong RC92 candidate worktree' }
pwsh -NoProfile -File .\scripts\verify-rc49-failure-evidence.ps1
$rc49AnchorExit = $LASTEXITCODE
if ($rc49AnchorExit -ne 0) { throw "RC49 failure evidence anchor verification failed with exit $rc49AnchorExit" }
pwsh -NoProfile -File .\scripts\verify-rc50-failure-evidence.ps1
$rc50AnchorExit = $LASTEXITCODE
if ($rc50AnchorExit -ne 0) { throw "RC50 failure evidence anchor verification failed with exit $rc50AnchorExit" }
pwsh -NoProfile -File .\scripts\verify-rc51-failure-evidence.ps1
$rc51AnchorExit = $LASTEXITCODE
if ($rc51AnchorExit -ne 0) { throw "RC51 failure evidence anchor verification failed with exit $rc51AnchorExit" }
pwsh -NoProfile -File .\scripts\verify-rc52-failure-evidence.ps1
$rc52AnchorExit = $LASTEXITCODE
if ($rc52AnchorExit -ne 0) { throw "RC52 failure evidence anchor verification failed with exit $rc52AnchorExit" }
gitleaks git --redact --no-banner --log-opts="08aff147766c046b12e19221a6aabb675485d452..HEAD"
$gitleaksExit = $LASTEXITCODE
if ($gitleaksExit -ne 0) { throw "RC92 release-range gitleaks failed with exit $gitleaksExit" }
git diff --check
$diffExit = $LASTEXITCODE
if ($diffExit -ne 0) { throw "RC92 source diff check failed with exit $diffExit" }
$statusLines = @(git status --porcelain=v1)
$statusExit = $LASTEXITCODE
if ($statusExit -ne 0) { throw "RC92 git status failed with exit $statusExit" }
if ($statusLines.Count -ne 0) { throw 'RC92 source worktree is dirty' }
git verify-commit HEAD
$commitVerifyExit = $LASTEXITCODE
if ($commitVerifyExit -ne 0) { throw "RC92 source commit signature verification failed with exit $commitVerifyExit" }
git tag -s -a v0.1.0-rc92-signed -m 'RC92 image-security release candidate'
$tagCreateExit = $LASTEXITCODE
if ($tagCreateExit -ne 0) { throw "RC92 signed tag creation failed with exit $tagCreateExit" }
git verify-tag refs/tags/v0.1.0-rc92-signed
$tagVerifyExit = $LASTEXITCODE
if ($tagVerifyExit -ne 0) { throw "RC92 tag signature verification failed with exit $tagVerifyExit" }
$tagHeadLines = @(git rev-parse --verify 'refs/tags/v0.1.0-rc92-signed^{}')
$tagHeadExit = $LASTEXITCODE
$headLines = @(git rev-parse --verify HEAD)
$headExit = $LASTEXITCODE
if ($tagHeadExit -ne 0 -or $headExit -ne 0 -or ($tagHeadLines -join '').Trim() -cne ($headLines -join '').Trim()) { throw 'RC92 signed tag does not peel to candidate HEAD' }
```

Then complete the RC92 image gate from that exact signed source.  It builds all
nine manifest-bound images: API, PDF scanner, tools, web, source agent, derived
PostgreSQL, derived ClamAV, derived ingest proxy, and Keycloak.  It updates the
exact Trivy 0.74.0 databases, scans serially, generates CycloneDX 1.7 SBOMs,
and binds every report to the immutable local image ID:

```powershell
# Creates new RC92 evidence; do not reuse or overwrite RC48 through RC52 evidence.
$worktreeLines = @(git rev-parse --show-toplevel)
$worktreeExit = $LASTEXITCODE
$headLines = @(git rev-parse --verify HEAD)
$headExit = $LASTEXITCODE
$tagHeadLines = @(git rev-parse --verify 'refs/tags/v0.1.0-rc92-signed^{}')
$tagHeadExit = $LASTEXITCODE
if ($worktreeExit -ne 0 -or $headExit -ne 0 -or $tagHeadExit -ne 0 -or
    -not [string]::Equals(($worktreeLines -join '').Trim(), (Resolve-Path 'K:\发票\wt-XM-INV-AUTOLOGIN').Path, [StringComparison]::OrdinalIgnoreCase) -or
    ($headLines -join '').Trim() -cne ($tagHeadLines -join '').Trim()) { throw 'RC92 worktree/tag/HEAD binding failed' }
$rc92ReleaseDirectory = 1..99 |
  ForEach-Object { "release\0.1.0-rc92-exact$_" } |
  Where-Object { -not (Test-Path -LiteralPath $_) } |
  Select-Object -First 1
if ([string]::IsNullOrWhiteSpace($rc92ReleaseDirectory)) { throw 'no unused RC92 exact directory remains' }
pwsh -NoProfile -File .\scripts\release-image-gate.ps1 `
  -ReleaseName 0.1.0-rc92 `
  -ImageTag 0.1.0-rc92 `
  -SourceAgentVersion 0.3.0 `
  -ReleaseDirectory $rc92ReleaseDirectory `
  -IdPMode keycloak
$imageGateExit = $LASTEXITCODE
if ($imageGateExit -ne 42) { throw "RC92 image gate expected exit 42, got $imageGateExit" }
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 -ReleaseDirectory $rc92ReleaseDirectory
$ordinaryVerifyExit = $LASTEXITCODE
if ($ordinaryVerifyExit -ne 0) { throw "ordinary RC92 artifact verification failed with exit $ordinaryVerifyExit" }
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 -ReleaseDirectory $rc92ReleaseDirectory -RequireTransferReady -SignedReleaseTag v0.1.0-rc92-signed
$strictVerifyExit = $LASTEXITCODE
if ($strictVerifyExit -ne 0) { throw "strict RC92 transfer-ready verification failed with exit $strictVerifyExit" }
```

**Running the gate detached, when the operator's own shell cannot stay
attached for the whole gate.** An interactive tool/agent sandbox commonly
kills whatever is still in the foreground of one shell invocation after
about ten minutes; the one-block RC68 gate above (nine image builds plus a
serial Trivy scan and SBOM per image) routinely runs longer than that. A
hidden/non-interactive console on a zh-CN Windows locale also defaults to
the GBK code page, which mangles UTF-8 byte output from git and docker --
including this repo's own `发票` path segment -- if the gate (or anything
that shells out to git/docker) is simply launched in the background without
addressing that separately. `scripts/run-detached.ps1` runs a target
PowerShell script in a genuinely detached, hidden process (not tied to the
launching shell's own lifetime) with `chcp 65001` plus UTF-8 console
encodings set first, a full-output transcript log, and an exit-code file,
then returns almost immediately -- so there is nothing left in the
foreground for a sandbox timeout to kill. Save the one-block gate command
above as a `.ps1` file (e.g. `release\run-rc68-gate.ps1`) and launch it
detached instead of running it directly:

```powershell
pwsh -NoProfile -File .\scripts\run-detached.ps1 `
  -ScriptPath .\release\run-rc68-gate.ps1 `
  -RunName rc68-gate
# Prints the run directory (under logs\detached-runs\) and returns in well
# under a second; the gate keeps running in the background.

# Check on it later -- each call is its own short, independent invocation:
pwsh -NoProfile -File .\scripts\run-detached.ps1 `
  -AttachRunDirectory 'logs\detached-runs\rc68-gate-...' -Wait -TimeoutSeconds 540
# Exit code 4 means "still running, poll again the same way"; any other
# exit code is the gate script's own, propagated through unchanged (so 42
# still means the same pending-canary result it always has). The run
# directory's transcript.log has the gate's complete merged output exactly
# as if it had run in the foreground; exitcode.txt has the same code once
# the run finishes.
```

See `scripts/run-detached.ps1`'s own doc comment (`Get-Help
.\scripts\run-detached.ps1 -Full`) for the rest of its options, and
`scripts/test-run-detached.ps1` for the mechanism proven end to end
(including the exact exit-42 shape this gate produces).

`ReleaseDirectory` must be a new or empty directory below `release/`; the
script never removes or overwrites an existing artifact directory. The default
IdP mode is deliberately `keycloak`: it must actually build the exact pinned
26.7.2 base and pass with zero unexcepted HIGH/CRITICAL findings. Even a clean
IdP image is recorded as `image_approved_pending_canary`, keeps production
launch blocked, and exits exactly `42` until real OIDC/MFA/RP-logout/back-channel
canaries are satisfied by the deployment runbook. Explicit `external-managed`
mode does not silently skip the IdP: its manifest records
`external_pending_canary` and remains blocked. Explicit `none` is likewise an
application-image-only blocked result.

The exact manifest-bound Keycloak image also runs the disposable
`provision-solov-realm.sh` integration: realm policy, LoA1/LoA2 executions,
roles/ACR/AMR mappers, four clients, no offline scope, fixed output, existing
realm refusal and the single root-owned invoice client-secret publication must
all pass. A result from a different local Keycloak tag or image ID is rejected.

The output includes raw HIGH/CRITICAL Trivy JSON, CycloneDX 1.7 SBOMs, build
logs, exact tool/database evidence, `release-manifest.json`, and a complete
`SHA256SUMS`. `release-image-gate.ps1` creates and verifies hashes; it does
**not** sign the artifact bundle. Recheck an artifact directory and its current
local image tags without rebuilding:

BuildKit provenance wrappers are disabled for the local candidate images so
an otherwise identical cached build is not assigned a fresh attestation
manifest-list ID. The gate's own machine manifest, CycloneDX SBOMs and hashes
are the retained provenance evidence.

Validation artifacts may still be generated from an unborn or dirty Git tree,
but their machine manifest keeps `productionLaunch` blocked with
`source_git_head_missing` and/or `source_worktree_dirty`. A production bundle
requires the reviewed source commit/tag to exist before the gate runs; the
gate never invents an author identity or commits files itself.

The one-block RC92 gate above already runs the ordinary verifier immediately
after exit `42`; do not split those commands across shells or processes.

The verifier rejects a report/SBOM whose embedded Trivy ImageID, manifest
hash, checksum or current local image ID has drifted. Trivy runs under an
exclusive release-cache lock and every scan is serial, avoiding shared-cache
lock races. No vulnerability is ignored. RC92 PostgreSQL has no exception and
must report zero HIGH/CRITICAL findings; the former fixed-version `gosu`
finding is not exception-eligible.

**Keeping the Trivy cache warm ahead of a gate run.** The gate above updates
its own `invoice-release-gate-trivy-0-74-0` cache volume synchronously, every
run, via `trivy image --download-db-only`/`--download-java-db-only` inside
its own container -- slow and occasionally unreliable through this machine's
local proxy, and wasted effort on a day the upstream database has not
changed. `scripts/refresh-trivy-cache.ps1` refreshes the same volume ahead of
time, independently of any gate run, by talking to the OCI registry
(`mirror.gcr.io/aquasec/trivy-db:2` and `.../trivy-java-db:1`) directly: a
24-way ranged, resumable, parallel `curl` download of each database's single
OCI layer, an OCI digest check against the manifest before anything is
trusted, and a throwaway `postgres:18.6-alpine` container (the same pinned
base this gate already builds from) to copy the verified files into the
volume. It takes the exact same `.trivy-0.74.release-gate.lock` this gate
does, so the two can never race the shared volume -- like the gate, it fails
fast rather than waiting if that lock is already held. It is idempotent and
safe to run daily (e.g. from Task Scheduler): each database records the OCI
digest it was seeded from, so a run against an unchanged upstream does no
network transfer beyond one manifest fetch and no reseed at all, and it
always prints each database's `UpdatedAt`/`NextUpdate` before exiting. It
refuses to replace an already-seeded database with a strictly older download
(`-Force` overrides that deliberately). Run it with no arguments for a
default daily refresh, or `-WhatIf` to see what it would do without changing
anything; see its own doc comment (`Get-Help
.\scripts\refresh-trivy-cache.ps1 -Full`) for every parameter.

**Registering the daily refresh as a Windows Scheduled Task.**
`scripts/register-trivy-refresh-task.ps1` registers, or updates in place, a
Scheduled Task named `InvoiceTrivyCacheRefresh` that runs the command above
once daily at 05:30 local time, so the cache stays warm without an operator
running the refresh by hand. It only schedules `refresh-trivy-cache.ps1`; it
never runs the refresh itself, and it embeds no secret -- the task's logon
type is S4U, which lets it run under the current user's identity whether or
not that user is interactively logged in at 05:30, without Task Scheduler
ever storing a password. Its run level is Limited (not Highest); the refresh
needs no elevation. Rerunning the script is idempotent: it registers the
task if `InvoiceTrivyCacheRefresh` does not exist yet, or updates its
action/trigger/settings/principal in place with `Set-ScheduledTask` if it
does, preserving Task Scheduler's own run history for the task rather than
recreating it. `-WhatIf` prints the full task definition (executable,
arguments, working directory, trigger, principal, settings) without
registering or changing anything:

```powershell
pwsh -NoProfile -File .\scripts\register-trivy-refresh-task.ps1 -WhatIf
# Prints the task definition only; registers/changes nothing.

pwsh -NoProfile -File .\scripts\register-trivy-refresh-task.ps1
# Registers InvoiceTrivyCacheRefresh if absent, or updates it in place.
```

Inspect the registered task with `Get-ScheduledTask InvoiceTrivyCacheRefresh
| Format-List *` (or `Get-ScheduledTaskInfo InvoiceTrivyCacheRefresh` for its
last/next run time and last result); its Task Scheduler history is under
Task Scheduler Library's root (`\`) in `taskschd.msc`. Remove it with
`Unregister-ScheduledTask -TaskName InvoiceTrivyCacheRefresh -Confirm:$false`.
The task also retries itself the same day if it exits non-zero: 3 times, 30
minutes apart by default (`-RestartCount`/`-RestartInterval`), which covers
both a skipped run (exit 75, below) and a genuine failure, on top of
`StartWhenAvailable` already covering the machine being off/asleep at
05:30. See `scripts/register-trivy-refresh-task.ps1`'s own doc comment
(`Get-Help .\scripts\register-trivy-refresh-task.ps1 -Full`) for its
parameters, including `-RepoRoot`, `-TaskName`, `-StartTime`, `-UserId`,
`-RestartCount` and `-RestartInterval`.

**Reading a refresh run's own log.** Every `refresh-trivy-cache.ps1`
invocation -- manual, via `scripts\run-detached.ps1`, or the Scheduled Task
above -- writes its own transcript-style log to
`logs\trivy-cache-refresh\runs\<UTC stamp>-<pid>.log` (stdout/stderr, the
console summary, start/end time, exit code, and on failure the exception
message and the failing script line), and keeps only the 30 most recent
(older ones are pruned automatically). It also updates
`logs\trivy-cache-refresh\runs\latest.json` after every run:

```json
{
  "started_at": "2026-09-03T05:30:00.1234567+00:00",
  "finished_at": "2026-09-03T05:30:04.7654321+00:00",
  "exit_code": 0,
  "action_db": "unchanged",
  "action_java_db": "unchanged",
  "error": null
}
```

`exit_code` is `0` on success, `1` on a genuine failure (`error` then holds
the exception message; the matching run log also has the failing script
line), or **75** when the release image gate (or a concurrently running
refresh) already held the shared `release\.trivy-0.74.release-gate.lock`
lock -- a skip, not a failure: the cache volume was never touched, `error`
reads `gate holds the cache volume; skipped`, and the Scheduled Task's own
`RestartCount`/`RestartInterval` above retries it later the same day.
`action_db`/`action_java_db` are each one of `unchanged`, `refreshed`,
`would-refresh (-WhatIf)`, or `null` (component skipped via `-SkipJavaDb`,
or the run never reached it, e.g. exit 75). This is what to check first
after an operator notices `Get-ScheduledTaskInfo
InvoiceTrivyCacheRefresh`'s `LastTaskResult` is non-zero: `latest.json`
gives the outcome at a glance, and the matching timestamped file under
`runs\` has the full detail.

The command above is the ordinary internal-consistency mode, so operators can
retain and diagnose failed or validation-only bundles. It is not transfer
authority. Immediately before signing `SHA256SUMS`, rerun the independent
verifier in strict transfer-ready mode against the signed RC92 tag. The
one-block gate above performs this immediately after ordinary verification;
both verifiers must exit `0`.

Strict mode verifies the annotated tag signature and peels it to a commit. It
then requires `source.gitDirty=false`, a 40-hex `source.gitHead` equal to that
commit, `releaseName=0.1.0-rc92`, all nine exact `:0.1.0-rc92` image
references, `applicationImageGate=passed`, and exactly one production block reason:
`idp_self_hosted_pending_canary`. Missing or additional reasons fail closed.
`productionLaunch` must remain `blocked`; transfer is preparation for the real
production canary, never approval to cut over traffic.

RC92 permits only the exact Keycloak vendor-rejected tuple documented in
`docs/IMAGE-SCAN-REVIEW.md`: `CVE-2026-22020`, `os-pkgs`/`redhat`,
`java-21-openjdk-headless@1:21.0.12.1.1-1.2.el9`, empty fixed version,
`HIGH`/`affected`, with the exact refreshed base digest and review deadline.
The raw finding remains visible.  The gate binds base and derived image IDs,
the tuple, pruning proof, isolated runtime/provisioning proofs, rationale and
review window; any drift or expiry fails closed.

After the artifact verifier passes, create and verify the detached artifact
signature before any transfer.  It signs the gate-produced `SHA256SUMS`, which
binds the manifest, reports, SBOMs and logs listed by that checksum file; retain
both files with the bundle.  Use only the reviewed offline release key and the
independent allowed-signers file:

```powershell
$strictReadyCandidates = [Collections.Generic.List[string]]::new()
foreach ($candidate in Get-ChildItem -LiteralPath release -Directory -Filter '0.1.0-rc92-exact*') {
  pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 -ReleaseDirectory $candidate.FullName -RequireTransferReady -SignedReleaseTag v0.1.0-rc92-signed
  $candidateVerifyExit = $LASTEXITCODE
  if ($candidateVerifyExit -eq 0) { $strictReadyCandidates.Add($candidate.FullName) }
}
if ($strictReadyCandidates.Count -ne 1) { throw "expected one strict-ready RC92 directory, found $($strictReadyCandidates.Count)" }
$releaseRoot = $strictReadyCandidates[0]
$checksumManifest = Join-Path $releaseRoot 'SHA256SUMS'
$releaseSignature = "$checksumManifest.sig"
$releaseSigningKey = '<offline RC92 release Ed25519 private key>'
$releaseAllowedSigners = '<reviewed release-tree allowed_signers file>'

& ssh-keygen -Y sign -q -f "$releaseSigningKey" -n solov-invoice-release-v1 "$checksumManifest"
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $releaseSignature -PathType Leaf)) {
  throw 'RC92 artifact SHA256SUMS signature was not created'
}
Get-Content -Raw -LiteralPath $checksumManifest | & ssh-keygen -Y verify `
  -f "$releaseAllowedSigners" -I invoice-release@solov.cc `
  -n solov-invoice-release-v1 -s "$releaseSignature"
if ($LASTEXITCODE -ne 0) { throw 'RC92 artifact SHA256SUMS signature verification failed' }
```

Only after this signature and its verification pass may the exact signed source
tag, `release-manifest.json`, `SHA256SUMS`, `SHA256SUMS.sig`, reports, SBOMs,
and the nine manifest-bound images be transferred. Image-gate exit `42` is the
sole expected pending-canary result and still requires both verifiers to exit
`0`; every other non-zero exit is a release block.

The RC92 source baselines are Go 1.25.13, pgx 5.9.2, x/text 0.39.0,
PostgreSQL 18.6, Keycloak 26.7.2 and ClamAV 1.4.5 LTS.  PostgreSQL, ClamAV and
ingest Nginx are locally built derivatives with fixed Alpine OpenSSL
`3.5.8-r0`; Keycloak is derived from
`sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067`.
Refresh any base only through the full image scan/SBOM/review flow.

> **Cutover ordering (learned 2026-09-01/02):** after the three Compose projects
> are rolled forward, restart `api` (its in-process workers can die on a
> transient DNS failure during container churn and do not self-heal), and
> **then** restart `ingest-proxy` — its Nginx resolves the `api` upstream at
> start, so an `api` restart afterwards leaves it pointing at a dead IP and
> every source agent sees HTTP 502 (a 3-hour balances-stream gap on 2026-09-01
> came from exactly this order mistake). The public path via the host Nginx to
> the published port is unaffected.

Set `INVOICE_IMAGE_TAG` in `deploy/.env.production` only after the exact RC92
manifest, signature and artifact verifier have passed. Production Compose has
no image-tag fallback: all nine images (`invoice-system-api`,
`invoice-system-pdf-scanner`, `invoice-system-tools`, `invoice-system-web`,
`invoice-source-agent`, `invoice-postgres`, `invoice-clamav`,
`invoice-ingest-proxy`, and `invoice-keycloak`) must resolve from that one
value. `invoice-postgres` is required for both the application and Keycloak
databases and for the `permissions` job. `SOURCE_AGENT_VERSION` remains the
independent binary/protocol version recorded inside the agent; it is not an
image tag. The production Compose files contain no `build:` directives and set
`pull_policy: never` for every locally built image. Use `--no-build` for `up`
and `--pull never` for one-shot `run` commands as second guards; a missing
transferred, reviewed image must fail closed.

This remains a two-stage hard gate.  Stage 1 is signed-source commit/tag
verification, then the serial build, scan, SBOM, manifest, independent artifact
verification, and detached artifact signature.
Stage 2 transfers only those manifest-bound images, then performs the backup,
isolated start, migration and real production canaries.  An
`image_approved_pending_canary` result is still blocked; it is never authority
to cut over traffic.
After any code or deployment change, the prior RC evidence is historical and a
new image gate must be generated before containers are recreated. Never mix an
older API container with a newer web/scanner container under one release.

For RC92, continue with sections 4, 5, 6, 7, 8, 9, 10, 11 (excluding 11.1),
12, and 13 in order. Section 3.1 (RC39 one-off), section 3.2 (balance cleanup),
and section 11.1 (quarterly deletion) are outside the RC92 release and require
separate explicit approval. The section 6 unactivated-v4 replacement path is
conditional and also requires its predicates plus separate maintenance approval.

### 3.1 RC39 populated readiness-index roll-forward

This is a **production change approval** procedure for the existing populated
`public.source_ingest_events` table. It does not authorize any change to
Sub2API or upstream New API, and it does not authorize restarting a deliberately
stopped source agent.

#### Stage 0: backlog freeze, recoverability and capacity hard gate

Stage 0 must pass before the immutable roll-forward order begins. Missing,
stale or unsigned evidence is NO-GO.

- Prove `sub2api-balances` and `newapi-balances` remain stopped with two
  timestamped `docker compose ... ps --status running -q` snapshots at least
  five minutes apart, the second immediately before the operator. Both outputs
  must be empty. Record `ps -a` state as well, and compare receiver counts and
  maximum `created_at` for `stream_id='balances'`; they must not advance between
  snapshots. Do not start either balance container during backup restoration or
  this roll-forward.
- Freeze account linking for the whole window: keep the public menu/edge closed,
  do not run an account-binding canary, and do not invoke any dependency batch
  wakeup. Record `external_accounts`, `external_account_binding_proofs`, and
  `source_ingest_events` status/dependency counts before backup and immediately
  before the operator. Any new external account, verified binding proof, or
  bulk transition out of `parked_identity` is NO-GO. This freeze remains until
  the separate parked-backlog cutover is approved.
- Create a fresh encrypted and Ed25519-signed full backup with the existing
  `deploy/backup/backup.sh` package using `BACKUP_SCHEMA_MODE=post-0011` and
  `BACKUP_LOCAL_KEYCLOAK=true`. It must contain the invoice PostgreSQL dump,
  documents, all ten source-state directories plus both cutover pairs,
  source/config metadata, and the local Keycloak dump. Independently verify the
  signed manifest and complete `deploy/backup/restore-drill.sh` into an isolated
  stack. The restore proof and off-host copy/ACK must be no older than two hours
  when the operator begins. This is the pre-RC39 database backup: its exact
  `schema_migrations` set must end at 0012, both
  `0013_source_readiness_active_index.sql` and
  `0014_balance_carry_forward_proof.sql` must be absent, and both new
  carry-forward tables must be absent in the restored copy. The production
  migration set must still match that backup evidence when the operator begins.
  A component omission, signature failure or partial restore is NO-GO.
- Persist a capacity TSV before approval containing
  `pg_database_size(current_database())`, `pg_table_size`,
  `pg_indexes_size` and `pg_total_relation_size` for
  `public.source_ingest_events`, the same sizes for any existing target index,
  total bytes from `pg_ls_waldir()`, `pg_stat_archiver`,
  `pg_replication_slots`, and `pg_stat_replication`. Also record byte and inode
  availability from `df` for the PostgreSQL volume mount, Docker data root,
  `RECORD_ROOT`, and `/tmp`; record the exact host and PostgreSQL timestamps.
- Capacity GO requires PostgreSQL-volume free bytes to be at least the greater
  of 20 GiB or `2 * pg_total_relation_size(source_ingest_events) +
  2 * pg_wal_bytes`; `/tmp` and `RECORD_ROOT` must each have at least 5 GiB free,
  and every recorded filesystem must have at least 10 percent free inodes. If
  archiving, slots or replicas are disabled, record that fact explicitly. If
  enabled, there may be no unresolved archiver failure, inactive slot, replay
  lag over 60 seconds, or retained/replay WAL lag over 512 MiB. Any unknown,
  NULL where evidence is expected, threshold failure, or capacity change before
  operator start is NO-GO.

The order is immutable: concurrent index operator, persistent evidence
signature and independent signature verification, exact migration 0013 and
0014 registration, owner-applied runtime-role hardening and privilege proof,
then roll forward to the new invoice API image. Do not combine or reorder these
stages. In populated production the migration's exact-index fast-path `DO`
block must find the already-valid index and return after catalog reads. Its
ordinary `CREATE INDEX` branch is restricted to a logically and physically
empty installation. A missing, wrong, invalid, not-ready or not-live index on
the populated table must fail before `schema_migrations` is updated.

Install the following files from the signed RC39 source tree beside the
reviewed production Compose file and set both shell files to `root:root` mode
`0700`:

- `deploy/postgres/apply-source-readiness-index-concurrently.sh`;
- `deploy/postgres/verify-source-readiness-index.sh`.

The release manifest must bind both hashes and the hash of
`scripts/verify-source-readiness-index-operator.ps1`. Set every mandatory value
below before invoking the operator. `EXPECTED_API_IMAGE_ID` is the exact image
ID of the currently running old invoice API, captured and approved before the
window; a tag is not acceptable. Script hashes must come from the signed RC39
manifest, not from an unreviewed server copy.

```bash
export SOURCE_READINESS_INDEX_CONFIRMED=YES
export PRODUCTION_ENV_FILE=/root/invoice-system/app/releases/<exact-current-release>/.env.production
export EXPECTED_API_IMAGE_ID='sha256:<approved-running-old-invoice-api-image-id>'
export EXPECTED_OPERATOR_SHA256='<signed-rc39-operator-sha256>'
export EXPECTED_VERIFIER_SHA256='<signed-rc39-verifier-sha256>'
export RECORD_ROOT=/root/invoice-system/deployment-records

deploy/postgres/apply-source-readiness-index-concurrently.sh
```

The operator accepts a root-owned `/run/lock` parent when it is either not
world-writable (for example `0755`/`0775`) or has the sticky bit set (the
standard `1777` layout). A world-writable parent without sticky protection,
including `0777`, fails before any database mutation. Under that parent the
operator uses a verified root-owned `0700` dedicated directory and root-owned
`0600` lock file, and rejects symlink, non-regular and owner/mode drift for its
lock path. It refuses
to run after migration 0013 is registered and never drops or repairs a
same-name index. After `CREATE INDEX CONCURRENTLY`, it runs `ANALYZE`, exact
catalog assertions, a read-only plan and bounded `EXPLAIN ANALYZE BUFFERS`.
PostgreSQL `Execution Time` must be at most 2000 ms and neither plan may contain
a sequential scan of `source_ingest_events`. The old API container
ID/image/start time must remain unchanged and `/healthz` must pass before and
after; `schema_migrations` must remain byte-for-byte unchanged.

Copy the exact manifest path printed by the operator into
`EVIDENCE_MANIFEST`; never select it using `ls -t`, a wildcard or an unreviewed
timestamp. Sign it with the approved namespace-bound evidence key and
independently verify the signature before changing `INVOICE_IMAGE_TAG` or
running the migrator:

```bash
export EVIDENCE_MANIFEST=/root/invoice-system/deployment-records/<exact-record>/RC39-SOURCE-READINESS-INDEX.sha256
export BACKUP_SIGNING_KEY_FILE=/root/invoice-system/incoming/rc22/backup-signing-key
export BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/incoming/rc22/backup-allowed-signers

ssh-keygen -Y sign -q -f "$BACKUP_SIGNING_KEY_FILE" \
  -n solov-invoice-backup-v1 "$EVIDENCE_MANIFEST"
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I invoice-backup \
  -n solov-invoice-backup-v1 -s "$EVIDENCE_MANIFEST.sig" <"$EVIDENCE_MANIFEST"
```

Retain the manifest, signature, catalog dump, plans, actual execution
milliseconds, script/config hashes and old API identity together. Missing
evidence is NO-GO. After the signature passes, update only the invoice release
tag in the reviewed production environment to the exact RC39 tag and run the
RC39 tools image. The exact-index fast path must succeed. The database migration
set must then exactly equal every `*.sql` file in the signed RC39 migration
directory, including exactly one checksum-bound row for each of 0013 and 0014:

```bash
docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml --profile tools \
  run --rm --pull never migrate

export RC39_POST_MIGRATION_EVIDENCE_DIR="$RECORD_ROOT/<exact-rc39-post-migration-record>"
install -d -m 0700 "$RC39_POST_MIGRATION_EVIDENCE_DIR"
(
  cd backend/migrations
  for migration in *.sql; do
    printf '%s|%s\n' "$migration" "$(sha256sum "$migration" | cut -d' ' -f1)"
  done | LC_ALL=C sort
) >"$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-expected.tsv"

docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT name,checksum FROM public.schema_migrations ORDER BY name" \
  >"$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-actual.tsv"

cmp -s "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-expected.tsv" \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-actual.tsv"
grep -Fx "0013_source_readiness_active_index.sql|$(sha256sum backend/migrations/0013_source_readiness_active_index.sql | cut -d' ' -f1)" \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-actual.tsv"
grep -Fx "0014_balance_carry_forward_proof.sql|$(sha256sum backend/migrations/0014_balance_carry_forward_proof.sql | cut -d' ' -f1)" \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-actual.tsv"
```

Before API startup, use `invoice_owner` to replay the signed RC39 runtime-role
policy. Then prove that `invoice_app` has exactly `SELECT, INSERT` on the two new
tables—no effective `UPDATE`, `DELETE` or `TRUNCATE` and no other direct table
grant. Also prove `schema_migrations` remains exact after the permission
transaction and that `invoice_app` retains SELECT-only access to it:

```bash
docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  <deploy/postgres/harden-runtime-role.sql

docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT table_name,
             has_table_privilege('invoice_app',format('public.%I',table_name),'SELECT'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'INSERT'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'UPDATE'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'DELETE'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'TRUNCATE'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'REFERENCES'),
             has_table_privilege('invoice_app',format('public.%I',table_name),'TRIGGER')
      FROM (VALUES ('balance_carry_forward_evaluations'),('balance_carry_forward_proofs')) AS checked(table_name)
      ORDER BY table_name" \
  >"$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-effective-privileges.tsv"

grep -Fx 'balance_carry_forward_evaluations|t|t|f|f|f|f|f' \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-effective-privileges.tsv"
grep -Fx 'balance_carry_forward_proofs|t|t|f|f|f|f|f' \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-effective-privileges.tsv"
test "$(wc -l <"$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-effective-privileges.tsv")" -eq 2

docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT table_name,string_agg(privilege_type,',' ORDER BY privilege_type)
      FROM information_schema.role_table_grants
      WHERE grantee='invoice_app' AND table_schema='public'
        AND table_name IN ('balance_carry_forward_proofs','balance_carry_forward_evaluations')
      GROUP BY table_name ORDER BY table_name" \
  >"$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-direct-grants.tsv"

grep -Fx 'balance_carry_forward_evaluations|INSERT,SELECT' \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-direct-grants.tsv"
grep -Fx 'balance_carry_forward_proofs|INSERT,SELECT' \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-direct-grants.tsv"
test "$(wc -l <"$RC39_POST_MIGRATION_EVIDENCE_DIR/carry-forward-direct-grants.tsv")" -eq 2

docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT has_table_privilege('invoice_app','public.schema_migrations','SELECT'),
             has_table_privilege('invoice_app','public.schema_migrations','INSERT'),
             has_table_privilege('invoice_app','public.schema_migrations','UPDATE'),
             has_table_privilege('invoice_app','public.schema_migrations','DELETE'),
             has_table_privilege('invoice_app','public.schema_migrations','TRUNCATE'),
             has_table_privilege('invoice_app','public.schema_migrations','REFERENCES'),
             has_table_privilege('invoice_app','public.schema_migrations','TRIGGER')" \
  | grep -Fx 't|f|f|f|f|f|f'

docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT name,checksum FROM public.schema_migrations ORDER BY name" \
  >"$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-after-permissions.tsv"
cmp -s "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-expected.tsv" \
  "$RC39_POST_MIGRATION_EVIDENCE_DIR/schema-migrations-after-permissions.tsv"
```

Require exactly two lines in each carry-forward privilege evidence file, hash
and sign the complete post-migration evidence directory, and independently
verify it. Any extra grant or migration, missing 0013/0014 checksum, permission
failure, or migration-set drift is NO-GO. Only then roll forward the invoice
API. This command does not refer to, restart or modify the upstream New API
service:

```bash
docker compose --env-file "$PRODUCTION_ENV_FILE" \
  -f deploy/docker-compose.prod.yml up -d --no-build --no-deps api
```

Verify the new container has the exact signed RC39 image ID and both
`/healthz` and the indexed `/readyz` path behave as expected. Once migration
0013 or 0014 is registered, the prior invoice API image is operationally forbidden:
never restart, recreate or roll back to it against the upgraded writable
database. If RC39 cannot become healthy, keep ingress closed and forward-fix,
or restore the complete matched pre-RC39 backup into an isolated stack.

### 3.2 Balance-history cleanup maintenance exception

This is a separately signed, one-time exception; it is not a business
migration and must never be generalized. The approved target contains only
pre-policy `balances/balance_checkpoint/upsert/parked_identity` rows with
`dependency_kind=source_external_account`, a non-null dependency key, an exact
published V3 cycle/batch mapping and `scan_ceiling_at` strictly before
`2026-09-01T00:00:00+08:00`. The user has explicitly rejected this historical
balance data. Invoice eligibility recognizes only recharge and consumption
facts occurring together at or after that boundary. At approval time all
external accounts, binding proofs, eligibility state, real/carry balance
evidence and projection jobs are empty. Therefore the signed exception keeps
the first technical anchor and latest pre-policy actual anchor for each
`(source_instance_id,dependency_key_hmac)` group and permanently removes only
the middle history. It must retain exactly `T=1879297`, `G=2747`, `K=5494`,
`D=1873803`, with exact group-size bounds `min=79` and `max=771`; any count,
bound or digest drift is NO-GO.
The first-anchor tuple is exact: 2738 rows match both the manifest baseline
snapshot and manifest cutover time, while 9 are the earliest post-cutover,
pre-policy technical anchors with a non-baseline snapshot. Baseline-snapshot
matches at a different time must remain zero.
The last-rank tuple is independently exact: all 2747 rows are non-baseline
snapshots strictly after the manifest cutover and are the maximum
`(scan_ceiling_at,batch.sequence,event.created_at,event_id)` tuple in their
group while still strictly before the policy cutoff; unexpected last rows must
remain zero.

First install and hash the tracked SQL and three root-only scripts:

- `deploy/postgres/balance-history-cleanup.sql`;
- `deploy/postgres/plan-balance-history-cleanup.sh`;
- `deploy/postgres/rehearse-balance-history-cleanup.sh`;
- `deploy/postgres/apply-balance-history-cleanup.sh`.

Stop every production service except PostgreSQL and stop all ten source-agent
services. Keep that write freeze through backup, restore rehearsal, off-site
ACK, production cleanup and post-cleanup evidence signing. Create a fresh
post-0014 encrypted/signed full backup. Its restore drill must run with the
fixed rehearsal path, not an arbitrary hook:

```bash
RESTORE_POSTGRES_TMPFS_SIZE=16g \
RESTORE_BALANCE_HISTORY_CLEANUP_REHEARSAL=YES \
BALANCE_HISTORY_REHEARSAL_RECORD_ROOT=/root/invoice-system/deployment-records \
EXPECTED_BALANCE_HISTORY_SQL_SHA256='<signed SQL sha256>' \
EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256='<signed rehearsal sha256>' \
EXPECTED_BALANCE_HISTORY_KEEP_SHA256='<planned keep sha256>' \
EXPECTED_BALANCE_HISTORY_PURGE_SHA256='<planned purge sha256>' \
  bash deploy/backup/restore-drill.sh
```

The rehearsal must reproduce T/G/K/D and both canonical SHA-256 sets, record
WAL bytes and duration, finish VACUUM, bind the backup manifest/signature and
produce `BALANCE-HISTORY-REHEARSAL.sha256`. Obtain the two expected digests only
from the fixed read-only `plan-balance-history-cleanup.sh`, which runs the same
tracked SQL with `apply_cleanup=false`; do not copy or edit the target query.
Sign and independently verify the plan, rehearsal and off-site ACK evidence.

Only after the supporting FK lookup index is exact valid/ready/live and every
capacity, replication, zero-business-state, schema, backup and evidence gate
passes may the signed operator run. It deletes mapping rows before event rows
in one SERIALIZABLE, synchronous transaction, records WAL/time, then vacuums
both affected tables. Keep and purge digests contain only
`source_instance_id|event_id`; HMACs, payload hashes and ciphertext must never
enter the evidence bundle. An exact poststate is reconciliation-only and still
requires all catalog, VACUUM and evidence gates. Never execute this procedure
against 9/1-or-later rows, another stream/entity/status, or a database with any
non-zero identity/eligibility/carry/job state.

## 4. Host directories and secrets

**Production change approval.** Create a new directory; do not reuse an
upstream deployment directory:

```bash
install -d -m 0700 /root/invoice-system/{secrets,config,backups}
```

Copy only this repository/release into `/root/invoice-system/app`. Required
untracked secret files are listed by `deploy/docker-compose.prod.yml` and
`deploy/docker-compose.idp.yml`.

Generate independent random database passwords and create matching one-line
DSN files. Use URL-safe/hex passwords so a DSN is not ambiguously encoded.
Generate the application field keyring without printing key material:

```bash
export INVOICE_IMAGE_TAG='<exact tag from the verified RC92 release manifest>'
: "${RC92_RELEASE_DIRECTORY:?set the exact strict-verified and signed RC92 exactN directory name from the release ticket}"
case "$RC92_RELEASE_DIRECTORY" in 0.1.0-rc92-exact[1-9]|0.1.0-rc92-exact[1-9][0-9]) ;; *) echo 'invalid RC92 release directory' >&2; exit 1 ;; esac
RELEASE_MANIFEST="/root/invoice-system/release/$RC92_RELEASE_DIRECTORY/release-manifest.json"
# The transferred manifest-bound images must already exist; production never builds or pulls them.
test "$(jq '[.images[] | .name] | length' "$RELEASE_MANIFEST")" -eq 9
for image_name in api pdf-scanner tools web source-agent postgres-runtime clamav-runtime ingest-proxy keycloak; do
  image_reference="$(jq -er --arg name "$image_name" '.images[] | select(.name == $name) | .reference' "$RELEASE_MANIFEST")"
  expected_image_id="$(jq -er --arg name "$image_name" '.images[] | select(.name == $name) | .imageId' "$RELEASE_MANIFEST")"
  actual_image_id="$(docker image inspect --format '{{.Id}}' "$image_reference")"
  test "$actual_image_id" = "$expected_image_id" || { echo "image ID mismatch: $image_name" >&2; exit 1; }
  printf '%s %s %s\n' "$image_name" "$image_reference" "$actual_image_id"
done
docker run --rm --pull=never --user "$(id -u):$(id -g)" \
  -v /root/invoice-system/secrets:/secrets \
  --entrypoint /usr/local/bin/invoice-keygen "invoice-system-tools:$INVOICE_IMAGE_TAG" \
  --out /secrets/invoice_field_keyring.json --key-id 2026-08
```

Generate `invoice_session_binding_key` from at least 32 random bytes. Store the
QQ authorization code only through the authenticated settings page after
startup; it is encrypted by the field keyring and never returned.

Generate the private source-agent PKI in a temporary 0700 directory:

```bash
docker run --rm --pull=never --user "$(id -u):$(id -g)" \
  -v /root/invoice-system/secrets:/secrets \
  --entrypoint /usr/local/bin/invoice-mtlsgen "invoice-system-tools:$INVOICE_IMAGE_TAG" \
  --out-dir /secrets/source-pki --server-name invoice-ingest.internal \
  --clients sub2api-agent,newapi-agent
```

The API target contains only `invoice-api` and the read-only migration files
required by startup verification; it has no qpdf binary or scanner server.
qpdf exists only in the separate low-UID `scanner` target. Migration/bootstrap/
key-generation and restore verification binaries exist only in the separately
scanned tools image; never deploy that image as the public API service. Record
and scan the API, scanner and tools image digests in the release manifest.

Move `source_agent_ca_key.pem` offline after issuing certificates. It must not
remain mounted to any production container. Set every secret directory to
0700, then apply the stricter per-consumer `0400`/`0444` file matrix below.

Compose `secrets.file` does not change host-file ownership or mode. Long-syntax
`uid/gid/mode` values are ignored for file-backed secrets, so prepare the host
files explicitly before any container starts:

| Consumer | Host owner | Mode | Examples |
| --- | ---: | ---: | --- |
| API and API tool containers | `10001:10001` | `0400` | field keyring, API/owner DSN, session key, OIDC client secret, break-glass CIDRs |
| API + PDF scanner shared capability | `root:10000` | `0440` | `invoice_pdf_scanner_capability`; mounted read-only, never stored in the socket volume |
| Each source-agent container | `65532:65532` | `0400` private, `0444` public cert | reader DSN, spool/signing/mTLS private keys; CA/client certificate may be public-read |
| Source state directory | `65532:65532` | directory `0700`, files `0600` | complete per-stream state, pending spool, inventory and lock metadata |
| Ingest Nginx | `root:root` | private key `0400`, cert/CA `0444` | ingest server key/certificate and source CA |
| One-time Keycloak bootstrap | `1000:1000` | `0400` | `keycloak_bootstrap_admin_password` only while using the override |
| PostgreSQL init/owner-only secrets | pinned image `postgres` UID (normally `70`, verify it) | `0400` | invoice init passwords and `keycloak_owner_db_password` |
| Keycloak DB app secret shared only by Postgres init and Keycloak | `root:root` | `0444` under the host parent directory `0700` | `keycloak_app_db_password`; owner password remains separate and never enters Keycloak |

First verify the pinned PostgreSQL UID instead of assuming it:

```bash
docker run --rm --pull=never --entrypoint id \
  "invoice-postgres:$INVOICE_IMAGE_TAG" postgres
stat -c '%u:%g %a %n' /root/invoice-system/secrets/*
SECRETS_DIR=/root/invoice-system/secrets POSTGRES_UID=70 \
  bash deploy/preflight-secret-permissions.sh
SECRETS_DIR=/root/invoice-system/secrets POSTGRES_UID=70 \
PRODUCTION_ENV_FILE=/root/invoice-system/app/deploy/.env.production \
CHECK_CONTAINER_READABILITY=true bash deploy/preflight-secret-permissions.sh
```

Generate the scanner capability once on the deployment host without putting it
in `.env`; both containers receive the same file through Compose, and no other
service does. Recreate API and scanner together to rotate it:

```bash
umask 027
openssl rand -hex 32 >/root/invoice-system/secrets/invoice_pdf_scanner_capability
chown root:10000 /root/invoice-system/secrets/invoice_pdf_scanner_capability
chmod 0440 /root/invoice-system/secrets/invoice_pdf_scanner_capability
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  up -d --no-build --force-recreate pdf-scanner api
```

Run a container-level readability preflight. For the API image, enter with its
normal UID and require every mounted private secret to be readable but not
group/world readable. Source agents perform the equivalent fail-closed checks
during `check-db-static`, full `check-db`, and startup because their scratch
image has no shell. Never assume
a successful `docker compose config` proves secret readability or mode.
The PostgreSQL-client `permissions` job also runs explicitly as UID/GID 10001;
with all capabilities dropped, leaving it at the image's default root user
would make the host-owned `0400` owner DSN unreadable rather than more
privileged.

### 4.1 Console-assertion keyring manifest (CR-0006, XM-INV-CONSOLE-ASSERT-DEPLOY)

`docker-compose.prod.yml`'s `api` service reads three new environment
variables and mounts one new read-only file:

- `OIDC_ADMIN_LOGIN_ENABLED` (default `true`) and `CONSOLE_ASSERTION_ENABLED`
  (default `false`) gate the two admin login paths; both ship dark by
  default, so this compose change alone does not alter production behavior.
- `CONSOLE_ASSERTION_ISSUER`/`CONSOLE_ASSERTION_AUDIENCE` are only read and
  validated when `CONSOLE_ASSERTION_ENABLED=true`
  (`backend/cmd/api/runtime.go`'s `loadConsoleAssertionRuntimeConfig`) --
  an operator who never turns the feature on needs no valid value for either.
- `CONSOLE_ASSERTION_KEYRING_FILE` (host path, required with no default, same
  convention as `SOURCE_TRUST_CONFIG_FILE`/`ADMIN_SETTINGS_BOOTSTRAP_FILE`
  above) binds read-only to the fixed container path
  `/config/console-assertion-keyring.json`, which `CONSOLE_ASSERTION_KEYS_
  FILE` also names as a literal -- the two must always match exactly.

**Before the first `docker compose ... up -d --no-build` that includes this
compose change**, the host file named by `CONSOLE_ASSERTION_KEYRING_FILE` must exist
as a plain file, even while the feature stays disabled: Docker creates an
empty *directory* at a bind-mount source that does not exist yet, and once a
container has been created against a directory-shaped mount, turning
`CONSOLE_ASSERTION_ENABLED` on later needs a full container recreate (not
merely a restart) to pick up a real file in its place. `deploy/roll-
forward.sh` checks for this itself and creates an empty placeholder file
only if none exists yet (it never overwrites a file that is already there);
this is purely mechanical and independent of whether the real reviewed
keyring has been generated -- see the enable order below for when the
placeholder is actually replaced with real key material. An empty file is
intentionally not a valid enabled-mode keyring: `CONSOLE_ASSERTION_ENABLED
=true` with zero trusted keys fails the process closed at startup rather
than silently accepting every assertion as invalid forever.

**Enable order** (CR-0006 phase 2; full detail and audit-evidence steps live
in the platform repo's `docs/superpowers/plans/2026-09-03-cr0006-phase2-
rollout.md`, steps 1/3/4/5):

1. Generate the signing keypair on the platform side, review the public key
   record, and copy the identical JSON entry into both repositories'
   `contracts/auth/console-assertion-keyring.v1.json`.
2. Install the reviewed manifest at the exact `CONSOLE_ASSERTION_KEYRING_
   FILE` host path (`install -m 0444 -o root -g root`), replacing the empty
   placeholder above -- confirm `sha256sum` matches the reviewed repository
   file before proceeding.
3. Enable the platform-side signer first (`XM_INVOICE_CONSOLE_ASSERTION_
   ENABLED=true` and its issuer/audience/key-ref variables on
   `platform-api`), confirm `console_assertion_signer_loaded` in its startup
   log.
4. Only then set `CONSOLE_ASSERTION_ENABLED=true` on the invoice side and
   restart `api`; keep `OIDC_ADMIN_LOGIN_ENABLED=true` throughout this step
   -- OIDC stays the working fallback during the canary.
5. Canary a real console login end to end (see `docs/handoffs/XM-INV-
   CONSOLE-ASSERT.md`'s "Production rollout" for the exact confirmation
   checklist) before running the identity migration (XM-INV-IDENTITY-
   MIGRATE) and, only after that and a clean full release cycle, setting
   `OIDC_ADMIN_LOGIN_ENABLED=false` (a separate, later slice -- see the
   platform plan's steps 4/5).

**Runtime-role grants.** Migration 0018 creates `console_assertion_nonces`
after the `permissions` job last ran, so the runtime role's grants on it come
only from that job's blanket `GRANT ... ON ALL TABLES`. Replay the job once
after the migration; the service sits behind the `tools` profile, so neither
an ordinary Compose bring-up nor `deploy/roll-forward.sh` ever starts it and
nothing replays it for you. Skipping it leaves `invoice_app` able to `UPDATE` and `TRUNCATE` the
table that decides whether an assertion has already been redeemed.

```bash
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml run --rm --pull never permissions
```

Confirm afterwards:

```bash
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c "SELECT has_table_privilege('invoice_app','console_assertion_nonces','INSERT'),
             has_table_privilege('invoice_app','console_assertion_nonces','DELETE'),
             has_table_privilege('invoice_app','console_assertion_nonces','UPDATE'),
             has_table_privilege('invoice_app','console_assertion_nonces','TRUNCATE')"
```

Expect `t|t|f|f`: the store claims a nonce with `INSERT ... ON CONFLICT DO
NOTHING` and sweeps expired rows with `DELETE`, and neither rewriting a
claimed nonce nor clearing every claim at once is ever a runtime operation.

**Rollback:** flip the flag that was most recently changed back to its prior
value and restart `api` -- `CONSOLE_ASSERTION_ENABLED=false` alone fully
restores today's OIDC-only behavior with zero effect on Keycloak; if
`OIDC_ADMIN_LOGIN_ENABLED` was already set to `false` in some later release,
set it back to `true` and restart instead. Neither direction requires a
migration or a schema change; see section 12 below.

## 5. Central Keycloak reference deployment

This section is optional if an existing IdP meets the same contract.

The pinned Keycloak 26.7.2 exception logic and isolated runtime smoke passed in
the superseded validation bundle
`release/0.1.0-rc1-gate-validation6/release-manifest.json`. Later source changes
correctly make that bundle stale; do not deploy it. Generate a fresh bundle
only after the full PostgreSQL verifier is green. The production realm, MFA
`acr`/`amr`, ID token, RP-initiated logout and back-channel logout canaries must
then pass on the real host. `docs/IMAGE-SCAN-REVIEW.md` is the image-exception
source of truth.

Reserve an unused Keycloak edge network. `KEYCLOAK_EDGE_GATEWAY` must be a
usable address inside `KEYCLOAK_EDGE_SUBNET`, and
`KC_PROXY_TRUSTED_ADDRESSES` must be exactly that one canonical IPv4 address
plus `/32`; Compose passes the same gateway into the startup validator, so a
mismatch, list or subnet is rejected before Keycloak starts. The `keycloak_edge`
attachment has the only positive `gw_priority`, which pins the published-port
peer/default gateway to that trusted `/32` for this multi-network container.
Every other Invoice/Keycloak bridge also has an explicit small CIDR. Docker's
automatic `/16` allocation is forbidden because it can silently cover the
proxy, ingestion and upstream projection ranges before those networks exist.
The reference values are:

```text
KEYCLOAK_HTTP_PORT=58180
KEYCLOAK_ADMIN_HTTP_PORT=58181
KEYCLOAK_DB_SUBNET=172.30.244.0/28
KEYCLOAK_EDGE_SUBNET=172.30.254.0/29
KEYCLOAK_EDGE_GATEWAY=172.30.254.1
KC_PROXY_TRUSTED_ADDRESSES=172.30.254.1/32
```

Run `scripts/preflight-production.ps1` before creation. It checks both loopback
ports, all explicitly planned Docker ranges/internal modes, and equality
between the gateway and trusted `/32`. After creation, compare the actual
gateway without changing it:

```bash
docker network inspect invoice-keycloak-prod_keycloak_edge \
  --format '{{(index .IPAM.Config 0).Gateway}}'
```

Stop if it does not exactly match the configured gateway. The two published
host ports both target Keycloak's private `8080`; their separation exists so
host Nginx can enforce different route and source policies. They must remain
bound to `127.0.0.1` only.

**Production change approval.** The base `deploy/docker-compose.idp.yml` has no
bootstrap administrator environment variable or secret. For first initialization
only, start it with `deploy/docker-compose.idp.bootstrap.yml`:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml up -d --no-build keycloak-postgres
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml \
  -f deploy/docker-compose.idp.bootstrap.yml up -d --no-build keycloak
```

After the loopback listener is healthy, provision the immutable initial realm
contract exactly once. The password is read with `password@FILE`; bearer tokens
and response bodies live only under root-owned `/dev/shm`. Stdout is one fixed
JSON line, and the only published client secret is `invoice-web` at UID/GID
10001 mode `0400`:

```bash
KEYCLOAK_BOOTSTRAP_PASSWORD_FILE=/root/invoice-system/secrets/keycloak_bootstrap_admin_password \
INVOICE_OIDC_CLIENT_SECRET_FILE=/root/invoice-system/secrets/invoice_oidc_client_secret \
  bash deploy/keycloak/provision-solov-realm.sh
```

Expected output is exactly
`{"status":"ok","realm":"solov","clients":4,"desktop_enabled":false}`.
Re-running or finding an existing output secret is an error; inspect a partial
realm rather than deleting or merging it automatically. Sub2API and New API
client secrets remain inside the restricted Keycloak administration flow and
are never printed by this provisioner.

Before using the bootstrap account, create DNS/TLS for both hostnames and
install the split edge policy. Render
`auth-admin.solov.cc.allow.conf.example` into a host-local, root-owned file by
replacing both TEST-NET routes with the exact normal administrator and
independent break-glass egress addresses. Every IPv4 entry must be `/32`, every
IPv6 entry `/128`, and `deny all;` must remain last. Never allow a LAN, Docker,
Cloudflare or ISP subnet. If Cloudflare proxies either hostname, the global
Cloudflare real-IP configuration must pass the authoritative CIDR check in the
preflight; otherwise the allowlist sees the proxy rather than the operator.

Validate the rendered file before installation:

```bash
bash deploy/validate-keycloak-admin-allowlist.sh \
  /root/invoice-system/config/auth-admin.solov.cc.allow.conf
```

Install:

```text
deploy/nginx/keycloak-proxy-headers.conf
  -> /www/server/panel/vhost/nginx/proxy/keycloak-proxy-headers.conf
rendered admin allowlist
  -> /www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf
deploy/nginx/auth.solov.cc.conf.template
  -> /www/server/panel/vhost/nginx/auth.solov.cc.conf
deploy/nginx/auth-admin.solov.cc.conf.template
  -> /www/server/panel/vhost/nginx/auth-admin.solov.cc.conf
```

Create the `access` and `proxy` directories first if absent. Keep the rendered
allowlist `root:root 0600`; the Nginx master reads it during configuration load.
The shared proxy-header include and vhosts may be `root:root 0644`.

Run `/www/server/nginx/sbin/nginx -t` and reload only after it succeeds. From
an allowed address, `https://auth-admin.solov.cc/admin/` must load/redirect,
while `https://auth.solov.cc/admin/` must return 404. From an unlisted address,
the admin hostname and `https://auth.solov.cc/realms/master/` must return 403.
Public discovery under `https://auth.solov.cc/realms/solov/` must still work.
Do not expose Keycloak port `9000`.

The optimized image uses a 2 GiB memory limit. Create the permanent master
administrator with the create-only operator; do not type or pass a target email
or SMTP value to it:

```bash
AGE_RECIPIENT_FILE=/root/invoice-system/config/backup-recipients.txt \
AGE_IDENTITY_FILE=/root/invoice-system/offline-mount/backup-age-identity \
BACKUP_SIGNING_KEY_FILE=/root/invoice-system/offline-mount/backup-signing-key \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
KEYCLOAK_BOOTSTRAP_PASSWORD_FILE=/root/invoice-system/secrets/keycloak_bootstrap_admin_password \
  bash deploy/keycloak/run-permanent-master-admin-maintenance.sh
```

The fixed `/root/invoice-system/keycloak-backups` directory must already be
`root:root 0700` on a non-ephemeral filesystem. The age identity and
Ed25519 signing key are temporarily mounted offline material and must not live
under the backup directory. The RC92 installation must create root-only
`SOURCE_COMMIT`, `SOURCE_TAG`, `KEYCLOAK_IMAGE`, `SMTP_TRANSPORT` and an exact
six-entry `RELEASE-TREE.sha256`, then sign it with the release key under the
dedicated release-tree namespace. The production operator re-verifies this
attestation using the independent root-only release-tree trust file. This is a
signed installed-tree attestation; the deployment wrapper must separately
verify the Git tag signature and peeled tag commit before generating it.

Before the mail window, rebuild/recreate the exact RC92 Keycloak container and
prove its immutable image ID plus the explicit default TLS hostname verifier
and disabled Kubernetes truststore environment. Run the negative wrong-hostname
SMTP canary when available; do not use this operator to send from an older
container or an unverified truststore-provider state.

The maintenance wrapper encrypts and signs the original administrator
allowlist, atomically installs a loopback-only allowlist, tests/reloads Nginx
and proves a public non-loopback request is denied. Its exit trap atomically
restores the byte-identical original file and reloads Nginx on both success and
failure. It never widens the approved list. The operator also takes a fixed
global lock and independently verifies the active loopback-only file.

Because the approved in-app browser egress cannot be reproduced from SSH, the
wrapper pauses on three explicit sentinels. From that same browser, verify the
admin route is reachable and reply `confirm-preflight-auth-admin-reachable`;
after the freeze verify both admin/master routes return 403 and reply
`confirm-frozen-auth-admin-master-403`; after restoration verify they are no
longer 403 and reply `confirm-restored-auth-admin-master-non403`. Timeout, EOF
or any other reply restores the exact original allowlist and exits failed.

The operator then performs these fail-closed gates in order:

1. create a complete encrypted Keycloak PostgreSQL dump, restore it into the
   exact pinned PostgreSQL image with `--network none`, verify the restored
   `master`, `solov` and source SMTP records, and sign the backup manifest;
2. `sync -f` every encrypted backup/proof/manifest/signature and both containing
   directories. It prints `backup_ready` and blocks without an Admin token.
   Download the four-file encrypted set to an off-host persistent location,
   run `scripts/create-keycloak-offsite-ack.ps1` to verify the backup signature
   and component hashes, upload its independently signed `OFFSITE-ACK` pair,
   then send the exact stdin word `continue`. Timeout, EOF, bad hash or bad
   off-site signature exits before mutation;
3. obtain a short-lived Admin REST token using the bootstrap secret through
   `password@FILE`; token, target identity and SMTP responses exist only in a
   root-owned `0700` directory under `/dev/shm`, with files mode `0600`;
4. require exactly one enabled, email-verified `solov` user holding the
   `invoice-admin` realm role. Its verified address is the only invitation
   target. No environment/argument target override exists;
5. require `master` to have no pre-existing SMTP map and accept only the
   signed-host SMTP contract: authenticated implicit TLS on port 465, SSL true,
   STARTTLS false, basic authentication and an exact field allowlist;
6. fresh-GET and canonicalize the complete `master` representation before each
   PUT, inject the source SMTP map into that fresh full representation, and
   verify after PUT that only SMTP changed, with exact database comparison. It
   then creates a differently named master user, grants only the
   `realm-management/admin` client role, and dispatches one 15-minute
   execute-actions message for `VERIFY_EMAIL`, `UPDATE_PASSWORD` and
   `CONFIGURE_TOTP`;
7. restore from a fresh current representation by clearing only SMTP, full-PUT
   that current object, then fresh-GET/canonical-compare and verify empty SMTP
   directly in PostgreSQL. It never replays a stale old realm snapshot. Any
   concurrent drift is preserved where safe and converts the run to fail-closed.

Operator-generated plaintext proof contains no email, domain, user identifier,
SMTP field, token or secret. The encrypted database backup necessarily contains
the full identity/configuration database, and the new master user necessarily
persists the invitation email. Keycloak admin events may also record the user
operation. Do not claim that Keycloak itself contains no PII.

The execute-actions link uses Keycloak's frontend realm path. The public auth
vhost deliberately applies the administrator/break-glass allowlist to every
`/realms/master/` request, even when Keycloak generates the frontend hostname.
The recipient must therefore open the invitation while using an approved
administrator or VPN/bastion egress. Do not weaken the allowlist to make the
link easier to open. A realm-level frontend URL override is rejected by the
operator because it could redirect the invitation outside this reviewed path
policy.

After the wrapper restores the allowlist, independently confirm the approved
administrator source can reach the admin route. Only then show the 15-minute
invitation to the recipient. The wrapper deliberately reports this probe as
required rather than claiming remote reachability it cannot observe locally.

The operator is create-only. A matching existing permanent user is identified
and refused; any mismatch is also refused for manual inspection. It never
repairs, re-invites, merges or adds privileges to an existing account.

After the invitation is consumed, enroll and test TOTP, confirm in a fresh
browser that the permanent account can administer the required realms, and
record the browser canary separately without identity data. The invitation
result alone is not permission to retire bootstrap. Then delete (preferred) or
disable the temporary bootstrap account. Stop Keycloak, delete the bootstrap
password file, remove its username from the host environment, and restart with
the base file only:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml \
  -f deploy/docker-compose.idp.bootstrap.yml stop keycloak
rm -f -- /root/invoice-system/secrets/keycloak_bootstrap_admin_password
unset KEYCLOAK_BOOTSTRAP_ADMIN_USERNAME
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.idp.yml up -d --no-build --force-recreate keycloak
```

Inspect the recreated container and prove neither
`KC_BOOTSTRAP_ADMIN_USERNAME` nor `keycloak_bootstrap_admin_password` is present.
Verify the deleted account cannot log in and the permanent LoA2 administrator
still can. Never use the bootstrap override for an ordinary restart.

The PostgreSQL container is initialized with owner-only
`keycloak_owner_db_password`; `010-keycloak-app-role.sh` creates
`keycloak_app` as `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION
NOBYPASSRLS` and transfers only the Keycloak database/schema ownership. The
running Keycloak container mounts only `keycloak_app_db_password`. Confirm with
`\du+ keycloak_app` and container inspection that it has no owner secret or
cluster-superuser credential before exposing the IdP.

Create realm `solov` with:

- HTTPS required externally, email verification enabled and brute-force
  protection enabled;
- self-registration only during the migration window;
- TOTP required for finance administrators; recovery codes stored offline;
- realm roles `invoice-user` and `invoice-admin` (admin is never a default
  role);
- `acr` and `amr` protocol mappers in ID and access tokens;
- an ACR-to-LoA mapping `urn:solov:loa:2 -> 2`; the level-2 authentication
  flow must require password plus OTP;
- a top-level string-array `roles` claim mapper;
- short access/ID token lifetime (about 5 minutes), normal SSO idle/max limits,
  and no offline access scope for these clients.

Before enabling required email verification, configure realm SMTP and use the
Keycloak **Test connection** action. Use a dedicated QQ/enterprise mailbox with
an authorization code, or Gmail with a Google App Password, different from the
invoice notification mailbox; do not put it in Compose or a plaintext environment variable. Enter it only in
the restricted Keycloak admin console, enable Keycloak admin events, document
the authorized operators and rotate it through the same console. Register a
canary account, receive and consume the verification message, then confirm its
ID token contains `email_verified=true`. If SMTP testing or this canary fails,
keep registration/email verification rollout disabled: invoice profile saving
correctly fails closed without a verified address.

Create distinct clients and secrets:

| Client | Type | Exact redirect |
| --- | --- | --- |
| `invoice-web` | confidential, code + PKCE | `https://invoice.solov.cc/api/v1/auth/callback` |
| `sub2api` | confidential, code + PKCE | `https://api.solov.cc/api/v1/auth/oauth/oidc/callback` |
| `newapi` | confidential generic OAuth/OIDC | `https://xm.solov.cc/oauth/solov-sso` |
| `invoice-desktop` | public, code + PKCE | the registered desktop loopback/deep-link callback only |

For `invoice-web`, also configure:

- attach Keycloak's built-in `basic` scope as a default client scope and verify
  its `auth_time` mapper writes the `AUTH_TIME` user-session note into the ID
  token; Keycloak 25 and later no longer emit this claim automatically without
  that mapper, and administrator step-up intentionally fails closed when the
  claim is absent or stale;
- valid post-logout redirect: exactly `https://invoice.solov.cc/`;
- back-channel logout URL: exactly
  `https://invoice.solov.cc/api/v1/auth/backchannel-logout`;
- Backchannel Logout Session Required = ON and Front Channel Logout = OFF.

For an already-provisioned realm that predates the `basic` requirement, use
`deploy/keycloak/attach-invoice-basic-scope.sh` only from a verified signed
release tag. Pass the exact tag commit, tag name and SHA-256 of that tag's blob;
the operator refuses a working-tree or transfer mismatch. It first publishes a
signed encrypted full-database backup and completes a no-network restore drill,
then changes only the `invoice-web` default-scope association through the Admin
REST API. Its success record deliberately leaves the real administrator
step-up canary and bootstrap-administrator retirement as pending gates. Do not
mark the identity rollout complete until the rotated invoice session contains
the required ACR, both `pwd` and `otp` AMR values, and a fresh `auth_time`, and
the temporary Keycloak bootstrap account has been retired as described above.

The invoice API derives RP-initiated logout only from the discovery
`end_session_endpoint`. Startup fails unless discovery advertises back-channel
logout. Logout tokens are accepted only as a bounded form POST and must pass
signature, exact issuer/audience, `iat`/`exp`, `jti`, empty logout event and
no-`nonce` checks. The first valid `jti` atomically revokes by `sid` (or by
issuer+subject when `sid` is absent), appends audit and creates an immutable
replay record; later deliveries are idempotent.

Do not share client secrets. Add exact web origins only where the IdP requires
them. The invoice backend validates issuer, discovery endpoints, JWKS,
signature algorithm, audience, authorized party, nonce, ACR and AMR.
Every client above uses `https://auth.solov.cc/realms/solov` as its issuer.
Never place `auth-admin.solov.cc` in an issuer, discovery URL, redirect URI,
allowed endpoint host or application web-origin list. Raw authorization query
strings are not written to either Keycloak edge access log; Keycloak user/admin
events provide the audit source.

After DNS/TLS and the provider metadata are live, but before starting the
invoice API, run the provider contract gate:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools \
  run --rm --pull never --no-deps oidc-preflight
```

The command performs discovery and JWKS reads only. It intentionally receives
no client secret and no database credential. A passing JSON report must show
`status=ok`, `issuer_https=true`, all five endpoint checks true,
`endpoint_address_policy=true`,
`allowed_signing_algorithms=["RS256"]`, and both back-channel logout fields
true. It never prints the issuer, endpoint URLs or authorization material.

Any non-zero exit blocks API startup. In particular, do not waive missing
`userinfo_endpoint`, `end_session_endpoint`,
`backchannel_logout_supported`, or
`backchannel_logout_session_supported`; do not add an endpoint host merely to
make an unexpected provider URL pass. Re-run this gate after every IdP upgrade,
hostname/certificate change, signing-policy change, or discovery metadata
change. The provider must also continue to pass the real login, MFA step-up,
RP-initiated logout and signed back-channel logout canary flows; discovery-only
success is necessary but not sufficient for launch.

For the normal public `auth.solov.cc` path, keep
`OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS` empty. If an approved private IdP is used,
list each exact canonical RFC1918/ULA address in that variable and record the
DNS/address review in the deployment change. The client pins each connection
to its validated DNS answer; it never permits loopback, link-local or metadata
addresses as exceptions.

## 6. Invoice database, migrations and initial settings

**Production change approval.** Verify every explicit network in
`deploy/.env.production` (`INVOICE_EDGE/DB/APP`, ClamAV/OIDC egress,
proxy/ingest, both projection networks, and Keycloak DB/edge) against every
existing Docker network. Never let Compose auto-allocate one of these bridges.
Set `INVOICE_PROXY_GATEWAY_IP` to one usable address inside the proxy subnet
and set `TRUSTED_PROXY_CIDRS` to exactly that address plus `/32`. Compose pins
the bridge gateway to the same value and assigns `invoice_proxy` the only
positive `gw_priority`; static verification compares all three, and the API
rejects subnets, multiple trusted proxies and every non-host prefix.

Put the immutable release value exactly once in the root-owned
`deploy/.env.production` (do not merely assign a non-exported shell variable),
keep that file mode `0600`, then prove Compose reads the same value:

```bash
# deploy/.env.production contains this exact line:
# ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00
test "$(stat -c '%a' deploy/.env.production)" = 600
test "$(grep -Fxc 'ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00' deploy/.env.production)" -eq 1
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  config --environment | grep -Fx 'ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00'
```

Migrations `0011_invoice_eligibility_policy.sql` and
`0012_economic_projection_contract_v4.sql` are a deliberate one-way
application/DB switch. Migration 0012 changes the production manifest allowlist
to normalized v4 financial-semantics contracts and refuses any accepted
manifest, batch or financial-ledger row. The old image does not know these
migrations and will
refuse startup; an already-running old API also cannot write the new required
snapshot columns. Before applying it:

1. verify signed tag `v0.1.0-rc17-signed` peels to commit
   `b17dbe4ba2d1a2c4926d0156abf80c9207a74a54` and retain the RC17 release
   manifest's exact rollback image IDs; verify the exact RC92 candidate images,
   then resolve the existing-pair/first-install path below without starting
   invoice ingestion;
2. stop the old `api`, `ingest-proxy`, and all source-agent containers and prove
   there are no invoice writer sessions;
3. while they remain stopped, run
   the reviewed RC92 `deploy/backup/backup.sh` with
   `BACKUP_SCHEMA_MODE=pre-0011`; it records initial service state and must not
   start a service that was stopped. Restore it with the RC92 drill and
   `RESTORE_SCHEMA_MODE=pre-0011` plus the exact RC17
   `PRE_0011_TOOLS_IMAGE`, which proves migration 0011/policy table are absent
   while the source-agent image bound to the archived state generation validates
   its cutover/state contracts. An old V3 pair requires the exact RC24 source
   agent; the RC92 V4 agent must not be used to reinterpret it. Do not use the
   older RC17 backup script here because it resumes every service unconditionally;
4. verify `funding_lots`, `source_usage_events`, `source_credit_events`,
   `consumption_allocations`, `invoice_requests`, and
   `source_cutover_manifests` are all empty (the migration independently locks
   and rechecks them);
5. stage the new Web image first, then run migration, permissions, settings and
   the new API as one maintenance-window change.

The pre-0011 rollback package requires all ten source-state directories and two
create-only cutover pairs, so resolve one of these paths during item 1:

- Existing V3 pair: do not recapture or reinterpret it. With the exact RC24
  source-agent image bound to that state generation, run offline
  `check-cutover` and all V3 `check-state` commands using
  `ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00`; require the exact source
  V3 contract, both clocks strictly before the boundary, and record the
  encrypted file hashes in the pre-0011 backup ticket. This proves the rollback
  generation only; it is not authorization to start the RC92 receiver.
- First installation with no pair: before applying 0011, stop one upstream
  application, pass the explicit-container quiescence gate, and use the exact
  RC92 source-agent image to capture that source once and immediately run
  `check-cutover`; restart it, repeat for the other source, then initialize the
  ten empty durable state directories without starting ingestion. Now create
  and restore-test the full backup in explicit RC92 pre-0011 mode while the
  services remain stopped. These same
  encrypted pairs are registered after migration; they are never captured
  again.

### One-time unactivated v4 candidate replacement

This is the only exception to reusing an existing pair, and it is not an
in-place recapture. It applies only to the prelaunch RC24 state in which
migration 0011 is present but the receiver has accepted no manifest or batch,
all ten receiver and local sequences are zero, every pending spool is absent,
all eligibility/watermark/financial tables are empty, and the current time is
strictly before the immutable eligibility boundary. Migration 0012 independently
locks and rechecks these conditions.

1. Keep the verified pre-0011 package. Create, sign and restore-test a separate
   post-0011 recovery point containing the unused RC24 state generation and
   both old pairs. Its source-state check must use the exact RC24 source-agent
   image recorded for that recovery generation, never RC92.
2. Stop all source agents. Install the RC92 v4 semantic-fingerprint functions
   through the reviewed wrapper and re-prove exact function hashes, roles,
   ACLs and `pg_depend=0`. Run all ten `check-db-static` commands; full
   `check-db` cannot pass against the old V3 pair and is forbidden at this step.
3. Move the whole unused RC24 generation—ten state directories and its
   `cutover` directory—to a read-only retired location and record ciphertext
   hashes. Do not delete, overwrite or mount it. Point `SOURCE_STATE_ROOT` and
   `SOURCE_CUTOVER_ROOT` at a new empty versioned generation.
4. Stop New API alone, prove database quiescence and capture/check its new
   create-only pair; run its five full `check-db` commands, then restart and
   prove health. Repeat for Sub2API. Never stop both public upstream
   applications at once. After both pairs exist, all ten full checks must have
   passed against the new V4 generation.
5. Initialize ten new state files and the two identity reconciliation files,
   then repeat the sequence-zero/no-pending and cutover checks.
6. Only after both pairs pass, apply migration 0012 and permissions while API,
   ingest and agents remain stopped. Start balances first. The first accepted
   batch permanently closes this replacement path.

Do not roll the invoice database back merely to replace an unused file trust
root. Never edit an old manifest, restore source-setting timestamps, reuse a
partially written directory or apply this procedure after any ACK.

If incident discovery finds `pending.enc`, this unused-candidate exception is
not available. Before any separately approved incident migration, follow
`SOURCE-AGENT-RUNBOOK.md` section 8.1: stop only the affected stream, prove its
Compose running set is empty and its state directory lock-free, retain the exact
image/state/spool/key generation, and capture `inspect-pending` JSON plus
unchanged ciphertext hashes with `run --rm --no-deps --pull never`. `pending=true`, any
consistency failure, or unknown receiver commit state blocks deletion, key
rotation, sequence reset and creation of a replacement state generation.

The schema-mode controls are explicit and signed into metadata; all other
backup/restore variables are the ones in section 11:

```bash
BACKUP_SCHEMA_MODE=pre-0011 BACKUP_QUIESCE_CONFIRMED=YES \
  bash deploy/backup/backup.sh

RESTORE_SCHEMA_MODE=pre-0011 \
RESTORE_POSTGRES_TMPFS_SIZE=16g \
PRE_0011_TOOLS_IMAGE='<exact RC17 tools image from its release manifest>' \
INVOICE_TOOLS_IMAGE='<exact RC92 tools image>' \
SOURCE_AGENT_IMAGE='<exact RC24 source-agent image bound to this old V3 backup>' \
  bash deploy/backup/restore-drill.sh
```

Do not call switching only the application tag a rollback. After 0011, rollback
means stopping all new writers and restoring the matching signed pre-0011
database backup, source state and RC17 images together. If any post-0011
financial fact exists, prefer a reviewed forward fix; a database restore would
discard that fact and requires explicit financial approval.

Run in this exact order while the old API/ingest remain stopped:

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d --no-build postgres clamav pdf-scanner

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm --pull never migrate

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm --pull never permissions

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm --pull never bootstrap-settings

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml --profile tools run --rm --pull never bootstrap-sources

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml exec -T postgres psql -X -v ON_ERROR_STOP=1 \
  -U invoice_owner -d invoice -At -F '|' -c \
  "SELECT to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy WHERE singleton_id=1"
# Required exact output:
# 2026-08-31T16:00:00Z|Asia/Shanghai|t|t|1
```

`bootstrap-settings` is create-only. It accepts either a real issuer or the
exact reserved `待配置开票主体` value; other `待配置...` and legacy
`请替换...` values are rejected. With the reserved value, the API process must
remain running, `/healthz` must return 200, and the authenticated/IP-restricted
administrator settings route must remain usable so the issuer can be entered.
`/readyz` must return 503 until that real issuer is saved. This intentional
not-ready state must not be treated as a crash or bypassed by routing public
invoice traffic early.

ClamAV has no published port. It joins the internal API network plus a dedicated
egress-only bridge so FreshClam can update the persistent signature volume. The
API mounts that volume read-only and rejects startup/uploads when `main.*` or
`daily.*` is missing, or the actual `daily.*` database file is older than
`CLAMAV_MAX_SIGNATURE_AGE` (default `48h`), even if clamd still answers PING.
The healthcheck does not use `freshclam.dat` mtime: that updater/rate-limit
state may legitimately remain unchanged after a successful no-op check. Use a
positive integer with one `h`, `m` or `s` suffix. The upload chain then streams the file
over an authenticated Unix socket to the pinned qpdf 12.3.2 structural scanner.
The scanner has `network_mode: none`, no API/DB/document mounts or application
secrets, and fixed 256 MiB/0.5 CPU/32 PID/two-scan limits; its only credential
is the root-owned read-only scanner capability. Encrypted PDFs, JavaScript/actions,
launch/remote URI behavior, forms/XFA, embedded files and rich media are
rejected before encrypted promotion.

The bootstrap command refuses to overwrite an existing setting row. All later
changes use OIDC/MFA/admin-IP-protected typed APIs. The application runtime role
can append but cannot update/delete audit records and cannot mutate migration
checksums. It can only read the eligibility policy; the policy row rejects
UPDATE, DELETE and TRUNCATE even for normal owner commands. Any policy change
requires a new audited migration.

## 7. Source-instance provisioning and read-only agents

**Production change approval.** Create two `source_instances` rows with UUIDs
and provision receiver streams `payments`, `identities`, `usage`, `credits`,
and `balances`. The UUID itself is
the source agent `SOURCE_ID`; slugs such as `sub2api-primary` are invalid for
the production receiver database foreign key.

Create two database-only internal bridges and attach only the existing database
containers. Do not put a source agent on either upstream application's normal
network. The reviewed helper reads only the four named values from the Compose
env file, verifies any pre-existing network before reuse, and refuses a wrong
alias:

```bash
PRODUCTION_ENV_FILE=deploy/.env.production \
  bash deploy/provision-projection-networks.sh
```

The ten source DSN secret files must use only
`sub2api-projection-db:5432` or `newapi-projection-db:5432`. Confirm both
networks have `Internal=true` and exactly the expected database plus five source
agents. If a named network already exists, inspect it and reuse it only when
its subnet, internal flag and members match; do not silently accept a different
object.

Bridge V4 replaces every source projection view. Before legacy cleanup, export
each source's exact six reader definitions/SCRAM verifiers with the root-only
`scripts/preserve-source-reader-roles.sh --mode export`. Never display that
mode-0600 file. After cleanup, restore it, then run maintenance wrapper modes
`install-source` and `install-economic` as the cluster superuser that owns the
current source database. Bare psql is forbidden because the wrapper supplies
`-X --no-password -v ON_ERROR_STOP=1`.

Production has ten active LOGIN roles/DSN secret files: one
identity plus four V3 economic streams per source. The two legacy V2 payment
compatibility holders (`invoice_sub2api_payments_reader` and
`invoice_newapi_payments_reader`) must exist as `NOLOGIN`, connection-limit-0
roles with no password; the templates enforce that state and no container uses
them.

- `contracts/sub2api-source-projection-grants.postgresql.sql`;
- `contracts/newapi-source-projection-grants.postgresql.sql`;
- `contracts/sub2api-economic-projection-grants.postgresql.sql`;
- `contracts/newapi-economic-projection-grants.postgresql.sql`.

The read roles must have `default_transaction_read_only=on`, a short statement
timeout, no role inheritance and no raw SELECT/write/schema privilege. Each
LOGIN role receives only `invoice_bridge` USAGE and its exact V4 function
EXECUTE. The NOLOGIN owner alone holds exact source-column SELECT grants. Run
the `bridge-v4` upgrade preflight and all five `check-db-static` commands per
source before cutover; after the create-only pair exists, run all five full
`check-db` commands;
then immediately encrypt/archive or securely delete the plaintext preserved-role
file. The gate rejects function-body drift, RLS, unexpected ownership, raw
caller ACL, schema CREATE and nonzero upstream relation dependencies.

The Sub2API payments reader calls only
`invoice_bridge.sub2api_payments_v4(text,jsonb)`; its `legacy_health` operation
exposes four aggregate counts. It never patches Sub2API source/tables and the
caller cannot read `payment_orders` or `provider_snapshot`. Before starting the
agent, call the aggregate operation through the reviewed reader and prove no
row is blocked:

```sql
SELECT total_rows,exposed_cny_rows,unsupported_known_non_cny_rows,
       blocked_unknown_currency_rows
FROM invoice_bridge.sub2api_payments_v4('legacy_health','{}'::jsonb) bridge(payload)
CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(
  total_rows bigint,exposed_cny_rows bigint,
  unsupported_known_non_cny_rows bigint,blocked_unknown_currency_rows bigint);
```

Any non-zero `blocked_unknown_currency_rows` blocks launch pending explicit
finance/DBA review. Known non-CNY rows are intentionally excluded from V1 but
do not block reconciliation. After launch, blocked-unknown evidence raises a
source-health alert and suspends missing-row tombstones while exposed CNY rows
continue syncing. The only reviewed
missing-snapshot CNY derivation is for v0.1.179 provider keys `easypay`,
`alipay`, and `wxpay`; Stripe/Airwallex with explicit non-CNY are known but
unsupported, while missing/contradictory/invalid evidence remains blocked.
Do not grant the reader direct `provider_snapshot` or `payment_orders` access.
PostgreSQL TEMP is granted through PUBLIC by default;
the templates fail until the source database owner has removed that effective
privilege for these isolated readers.

The New API identities reader executes only
`invoice_bridge.newapi_identities_v4(text,jsonb)`. Before starting identities sync,
verify exactly one `solov-sso` provider row has `contract_ok=true`; the reader
must fail on both raw OAuth tables and client-secret/policy/mapping fields. The
agent independently validates all four endpoints against the configured exact
HTTPS issuer on every scan.

Each production stream uses its own source-agent container and state/spool
files. Required variables are defined in `docs/CONFIGURATION.md` section 5.
The only cross-system path is outbound HTTPS with mTLS plus Ed25519 signatures.
The agent never receives an invoice PostgreSQL credential.

- Sub2API payments V3: post-cutover `(updated_at,id)` scans with exact wallet
  unit/config/currency proof;
- Sub2API usage/credits/balances: wallet events, non-cash sources and atomic
  encrypted balance snapshots;
- negative `admin_balance`/`admin_concurrency` redeem records are not positive
  credit facts and therefore do not poison the credits contract. A
  non-positive used `balance` credit still fails closed; unexplained
  administrator deductions remain visible to the signed balance checkpoint
  and freeze eligibility instead of creating invoiceable cash;
- Sub2API identities: only the configured central OIDC provider/issuer;
- New API payments: every row remains `pending_manual`; every publishable cycle
  is a full post-cutover scan because there is no `updated_at`;
- New API usage/credits/balances: consume logs, mutable-code full rescans and
  atomic balance snapshots; logging/configuration drift blocks watermarks;
- New API identities: only the reviewed identities bridge function. The provider
  contract must prove well-known, authorization, token and user-info endpoints
  belong to the exact central issuer and slug `solov-sso`; the reader cannot
  select either raw OAuth table or any client secret.

ACK loss must replay the byte-identical encrypted-spool batch. Never delete an
agent state/spool file to "fix" a cursor; use the documented recovery check.

Build the disposable `keygen` Docker target, generate one distinct Ed25519 key
per source/stream, delete that tools image, and copy
only each `.pub.b64` value into `source-trust.json`:

```bash
docker build --target keygen -f agents/Dockerfile.production \
  -t invoice-source-keygen:one-time agents
install -d -m 0700 -o 65532 -g 65532 /root/invoice-system/source-keygen-work
docker run --rm --user "65532:65532" \
  -v /root/invoice-system/source-keygen-work:/work \
  invoice-source-keygen:one-time \
  -key-id 2026-08-payments \
  -private-out /work/sub2api_payments_signing_key.pem \
  -public-out /work/sub2api_payments_signing_key.pub.b64
# Repeat for the other nine source/stream tuples, then review and promote:
install -d -m 0755 -o root -g root /root/invoice-system/config/source-public-keys
install -m 0400 -o 65532 -g 65532 /root/invoice-system/source-keygen-work/*_signing_key.pem /root/invoice-system/secrets/
install -m 0444 -o root -g root /root/invoice-system/source-keygen-work/*.pub.b64 /root/invoice-system/config/source-public-keys/
rm -rf /root/invoice-system/source-keygen-work
docker image rm invoice-source-keygen:one-time
```

Create ten empty state directories and
`$SOURCE_STATE_ROOT/cutover/{sub2api,newapi}` mode 0700, owned by UID/GID 65532.
Generate distinct spool/signing keys per stream plus one cutover and one balance
snapshot AES key per source. Do not
start or initialize the agents yet: even `docker compose run` must attach the
external ingestion network, which is created with the API in section 9.

Every signed batch must match the audited `source_instances.runtime_version`
and carry `projection_status=healthy`. A source upgrade is a controlled CAS:
stop/drain its five agents (including pending spools), set
`expected_previous_runtime_version` in the source bootstrap file, rerun
`bootstrap-sources`, then restart and canary. The API requires fresh payments
and four economic plus identity heartbeats and zero queued/dead events before readiness, user
submission or final manual issue confirmation. OIDC/account dependency waits
remain visible but do not make unrelated users unhealthy. They use exact HMAC
wakeups plus a 12-hour fallback; alert when the parked count approaches
100,000 rather than shortening that interval or deleting paid-user evidence.

### 7.1 Reading agent restart counts, and 409 vs 503 SOURCE_SCAN_CYCLE_BUSY

Each of the ten `deploy/docker-compose.sources.yml` services (project
`invoice-source-agents-prod`) is `restart: unless-stopped`; Docker restarts
the container every time `source-agent-prod run` exits. Check restart counts:

```bash
docker compose -f deploy/docker-compose.sources.yml ps -a
docker inspect -f '{{.Name}}: {{.RestartCount}}' \
  $(docker compose -f deploy/docker-compose.sources.yml ps -aq)
```

A restart count that keeps climbing on one agent, together with
`source stream stopped fail-closed` in `docker compose logs <service>`, is
real: the runner hit something it treats as permanent and the process exited.
Since XM-INV-AGENT-RESTART-GRACE (agent 0.3.1+) the fresh process resumes the
persisted reconcile/full-scan schedule from the same state file instead of
unconditionally forcing a new cycle, and `sub2api-usage`'s periodic
`ScanReconcile` itself only re-verifies the window since the previous
completed reconcile, not the full history since cutover -- so a climbing
restart count no longer implies a growing-without-bound rescan every time.
Before this fix, every `ScanReconcile` rewound to the cutover manifest's
position, a fixed point captured once and never advanced (2026-08-25 for this
source), so the re-verified window grew every day since cutover; a
restart-triggered cycle observed 2026-09-03 (before this fix reached
production) ran 06:31Z-06:56Z, 3,327 batches, roughly 330k rows -- about 25
minutes, not the full source table, but already large and getting larger
day over day. A climbing restart count still forces one cycle on the very
first restart after upgrading to 0.3.1 (no schedule/baseline recorded yet
under the old binary) and, if the state volume itself was lost or replaced,
from a genuinely fresh state file; watch `OnCycle`'s log line for
`mode="reconcile"` cycles' `records` count, and for
`legacy_reconcile_cycle_abandoned` in its `warnings` field, which marks that
one-time abandonment of a pre-upgrade in-flight cycle explicitly. The receiver
(`backend/internal/sourceingest/receiver.go`) returns two rejections
for a failed commit that look similar from the outside but are not the same
problem:

- **409 `SOURCE_BATCH_COMMIT_REJECTED`** -- a real commit conflict (stale
  sequence/hash chain, append to an already-finalized cycle, a duplicate
  event with a different hash). The agent treats 409 as permanent and exits
  immediately without retrying. A rising restart count with this code is an
  incident: find the specific `stream_id`/`batch_id`/`sequence` in the
  receiver log line and the matching `source_ingest_batches`/
  `source_economic_scan_cycles` rows before letting it restart again.
- **503 `SOURCE_SCAN_CYCLE_BUSY`** (with `Retry-After: 30`) -- expected,
  temporary backpressure from the one-active-cycle constraint
  (`source_economic_one_active_scan_cycle`,
  `backend/internal/domain.ErrScanCycleBusy`): the stream already has a
  `receiving`/`processing` scan cycle, which can legitimately run for up to
  ~30 minutes on a large snapshot. Since XM-INV-CYCLE-BACKOFF the agent
  treats this as transient, honors `Retry-After`, and keeps retrying the
  *same* batch/sequence inside the *same* process -- it does not spend the
  `SOURCE_MAX_CONSECUTIVE_FAILURES` (default `10`) budget and does not exit
  or trigger a reconcile sweep. A healthy agent riding out a long cycle logs
  repeated `transient sync failure ... error="ingestion rejected batch with
  status 503"` with its restart count unchanged, not climbing. This is why
  only the four v3 economic streams (`payments`/`usage`/`credits`/
  `balances`) can show it; `identities` runs schema 2.0 and never opens a
  scan cycle.

A rising restart count paired with repeated `SOURCE_SCAN_CYCLE_BUSY` entries
(rather than `SOURCE_BATCH_COMMIT_REJECTED`) means the deployed image
predates XM-INV-CYCLE-BACKOFF (busy was still mapped to 409 and treated as
permanent) -- redeploy the fixed build rather than investigating it as a
commit conflict.

**Self-healing a stale cycle left by an agent restart (XM-INV-SCAN-CYCLE-SUPERSEDE).**
Production incident 2026-09-03: a pre-deploy backup quiesce restarted the
0.3.0 Sub2API usage agent, opening scan cycle `c272b5a4` (`receiving`,
sequences 106054-106231, last update 08:58:21Z); the roll-forward then
started the 0.3.1 agent, whose rolling-window logic abandoned that legacy
in-flight cycle client-side (see `legacy_reconcile_cycle_abandoned` above)
and opened a fresh cycle id. Every batch of the new cycle was rejected as
`SOURCE_SCAN_CYCLE_BUSY` -- correctly, per the one-active-cycle constraint --
but nothing server-side ever closed the orphaned `c272b5a4` row, so the
stream stayed wedged behind it indefinitely; before this fix, the only way
out was an operator manually marking that row `blocked`.
`CommitSourceBatch` now does this itself: when a v3 batch arrives for a
stream whose one active cycle has a *different* `scan_cycle_id` and that
cycle's `updated_at` has stopped advancing for longer than the same activity
window the readiness grace above already tolerates (`economicRescanActivityPollWindows *
SOURCE_POLL_INTERVAL + SOURCE_ECONOMIC_SAFETY_DELAY +
economicRescanProcessingTailAllowance`, so no separate configuration), it
marks the stale row `cycle_status='blocked'` with
`superseded_by_scan_cycle_id`/`supersede_reason` set (migration 0022) and
records a `source.scan_cycle.superseded` audit event (old/new cycle ids and
sequences), then accepts the new cycle in the same transaction. A still-fresh
active cycle with a different id is unaffected and keeps returning
`SOURCE_SCAN_CYCLE_BUSY` exactly as before -- this only ever fires once the
old cycle has genuinely gone stale, never as a race against a cycle that is
still legitimately running. What an operator sees now: the same transient
`SOURCE_SCAN_CYCLE_BUSY` retries during the grace window, then the stream
recovers on its own once that window elapses -- no manual `UPDATE
source_economic_scan_cycles SET cycle_status='blocked'` required. A stream
still wedged on `SOURCE_SCAN_CYCLE_BUSY` well past that window (or a deployed
image predating this fix) is a real incident: check
`source_economic_scan_cycles.updated_at`/`superseded_by_scan_cycle_id` for
the stream's active row and the `audit_events` table for
`source.scan_cycle.superseded` before assuming a manual fix is needed.

## 8. Configure upstream OIDC without source changes

**Production change approval.** Take an upstream database/config backup first.

### Sub2API

Use its administrator settings page. Set:

- issuer/discovery to the `solov` realm;
- backend redirect to
  `https://api.solov.cc/api/v1/auth/oauth/oidc/callback`;
- frontend redirect to `/auth/oidc/callback`;
- scopes `openid profile email`;
- signing algorithms `RS256`;
- PKCE = true;
- validate ID token = true;
- require verified email = true;
- enable OIDC only after a canary admin and user both bind successfully.

Existing users must log in with their existing account and explicitly bind the
new IdP. Never merge by email. Preserve password login during migration.

Add only the user custom menu:

```text
开票中心 -> https://invoice.solov.cc/embed/sub2api
```

The Nginx entry disables access logging and returns a clean 303, so the Sub2API
JWT automatically appended to the iframe URL is neither consumed nor retained.
Do not add the administrator page as an iframe; administrators use the
top-level `https://invoice.solov.cc/admin` URL.

### New API

Prefer a custom OAuth provider with stable slug `solov-sso`, not the older
single `users.oidc_id` path. Configure discovery/endpoints from the same IdP,
map `sub`, `preferred_username`/`name` and `email`, and require
`email_verified=true` in its access policy.

Existing users first log in with their original credential and bind the
provider. Disable open OAuth registration until the migration policy is
approved. Never auto-merge duplicate-email accounts.

If using the Chats iframe feature, configure only:

```text
https://invoice.solov.cc/embed/newapi
```

Never use `{key}` or append a New API model/API token.

## 9. Public invoice service and Nginx

Start the API process and internal mTLS ingress after migrations, permissions,
settings, ten agent state directories and both encrypted cutover pairs are ready. The ingest proxy waits
for the API process, not its readiness: readiness itself requires the first
ten signed heartbeats, so a health dependency would deadlock cold start.

Keep the API and every source-agent service on the same reviewed timing
contract: `SOURCE_ECONOMIC_HEARTBEAT_MAX_STALENESS=5m`,
`SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS=15m`,
`SOURCE_ECONOMIC_SAFETY_DELAY=5m`, and `SOURCE_POLL_INTERVAL=1m`. The API fails
startup if the economic watermark threshold is less than the safety delay plus
the receiver's five-minute maximum clock skew plus two poll intervals. A missed
non-identity heartbeat still fails after five minutes independently of the
watermark budget. After startup, an idle Sub2API payment stream must continue
publishing a watermark near source time minus five minutes; a watermark pinned
to the timestamp of the last payment is an RC92 rollback condition.

**Active-rescan readiness grace (XM-INV-AGENT-RESTART-GRACE, agent 0.3.1+).**
`SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS` alone would keep `/readyz` (and the
funding-lot five-stream freshness gate) failed for the full duration of any
Sub2API usage reconcile, including the one-time forced cycle on the first
restart after an agent upgrade. The API derives an activity window -- `economicRescanActivityPollWindows`
(2) `* SOURCE_POLL_INTERVAL` + `SOURCE_ECONOMIC_SAFETY_DELAY` +
`economicRescanProcessingTailAllowance` (30m, a separate margin for the gap
between the agent finishing a cycle's last page and the backend finishing
processing/publishing it, since nothing touches the row's `updated_at` during
that gap -- see its doc comment in backend/cmd/api/runtime.go for the
production reconcile this is calibrated against) -- and, while a stream's
`source_economic_scan_cycles` row proves the rescan's `updated_at` is still
advancing within that window, downgrades `ECONOMIC_WATERMARK_STALE` to the
non-fatal `ECONOMIC_RESCAN_ACTIVE` reason (see the admin source-health
report) instead of failing readiness. It fails closed the moment that row
stops updating -- a stalled or crashed rescan gets no grace and the endpoint
degrades exactly as it did before this change. This grace needs no operator
action; it is derived from the same timing contract in the paragraph above,
not independently configured.

**Eligibility-projection failure grading (XM-INV-PROJECTION-FAILURE-GRADING).**
A per-account error from the eligibility-projection worker no longer trips
`/readyz` on the first occurrence. `EligibilityProjectionHealth.Dead` --
`eligibility_projection_jobs.status='dead'` -- is the only per-account
condition that makes readiness unhealthy; it is reached only after 8
consecutive processing errors for the same account, with exponential backoff
(30s doubling, capped at 30 minutes) between attempts. A job merely retrying
inside that backoff (`Retrying` in the admin source-health report,
`GET /api/v1/admin/source-health`'s `eligibility_projection` object) never
affects readiness, and is excluded from the stuck-job budget
(`OldestPending`, 15 minutes) for as long as its own backoff has not
elapsed -- the same treatment `BALANCE_PROOF_PENDING` jobs already got. A
dead job never revives on its own (a new fact for that account advances its
pending work but leaves it dead); recover it with:

```bash
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=projection-requeue-dead   # add --apply --operator-id=<admin-uuid> once the dry-run report looks right
```

See `docs/ELIGIBILITY-OPERATIONS.md` for the full grading/dead/requeue
contract.

```bash
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d --no-build api web ingest-proxy

# Before any fresh V4 generation starts: prove all ten static and live checks.
all_sources=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
for service in "${all_sources[@]}"; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm --pull never "$service" check-db
done

# The create-only pairs were already captured/verified before migration 0011.
# Never run cutover-init here. Re-check the exact same encrypted files, source
# contracts and strict pre-policy clocks before registering trust.
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm --pull never sub2api-cutover-init check-cutover
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml \
  --profile cutover run --rm --pull never newapi-cutover-init check-cutover
# Verify persisted contracts are exactly sub2api-economic-v4 and
# newapi-economic-rc25-v4; fixture-v3 is forbidden in production.

# Initialize every independent cursor/sequence. Only V2 identities have a
# deletion-reconciliation state file.
for service in "${all_sources[@]}"; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm --pull never "$service" init-state
done
for service in sub2api-identities newapi-identities; do
  docker compose --env-file deploy/.env.production \
    -f deploy/docker-compose.sources.yml run --rm --pull never "$service" init-reconcile
done

# Register trust first, then let manifest-only balances sequence 1 commit.
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --no-build --wait --wait-timeout 300 \
  sub2api-balances newapi-balances
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --no-build --wait --wait-timeout 300 \
  sub2api-payments sub2api-usage sub2api-credits newapi-payments newapi-usage newapi-credits
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml up -d --no-build --wait --wait-timeout 300 \
  sub2api-identities newapi-identities

# Now wait for the source heartbeats, event drain, ClamAV/scanner and API health.
docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.prod.yml up -d --no-build --wait --wait-timeout 300 \
  api web ingest-proxy

docker compose --env-file deploy/.env.production \
  -f deploy/docker-compose.sources.yml ps
```

All ten source services must remain `running` without a restart-count increase;
the API must become healthy only after all five streams for both enabled sources
are fresh. Keep the public vhost/menu disabled if the five-minute wait expires.

Install these files in the BT Nginx locations shown in their templates:

- `invoice-http-context.conf` in the `http {}` context;
- `invoice-common-headers.conf` as
  `/www/server/panel/vhost/nginx/proxy/invoice-common-headers.conf` and
  `invoice-security-headers.conf` as
  `/www/server/panel/vhost/nginx/proxy/invoice-security-headers.conf` (the BT
  vhost-root `*.conf` glob is the `http {}` context and must not load this
  server-only snippet globally);
- `invoice.solov.cc.conf.template` as the enabled vhost.

Issue TLS, run `/www/server/nginx/sbin/nginx -t`, then reload. The vhost:

- publishes no database, ClamAV or ingestion port;
- overwrites forwarded/mock/mTLS headers;
- blocks `/internal/` publicly;
- disables logging on token-bearing embed and OIDC paths;
- streams the exact PDF upload and download routes without host-disk request or
  response buffering;
- disallows administrator framing;
- routes only `/api/` to the API and all other application paths to the web
  container.

`INVOICE_INGEST_PROXY_IP` must be inside `INVOICE_INGEST_SUBNET`, and
`INVOICE_INGEST_PROXY_CIDR` must be the exact `/32` for that address. Do not
expand it to the bridge subnet: the API trusts mTLS assertion headers only from
this one container address, while each source agent independently pins the
same destination `/32`. Keep the fixed proxy address outside
`INVOICE_INGEST_DYNAMIC_RANGE` so the API or an agent cannot receive it before
the proxy starts. The dynamic range must have room for the API plus all ten
source agents. The supplied `/27` subnet uses a `/28` dynamic pool and reserves
`.30` for the proxy; the old `/28` subnet with a `/29` pool is too small and is
rejected by the production capacity review.

## 10. Canary acceptance

Use one finance admin, one Sub2API user and one New API user.

1. For the Sub2API user, select Sub2API and log in with the platform email and
   password; exercise the two-step 2FA response when the account requires it.
   For the New API user, select New API and log in with the platform username
   and password. For both platforms, prove wrong-password and unknown-account
   responses are indistinguishable, the password is absent from logs/storage,
   cookie flags and CSRF rejection hold, idle/absolute session limits are
   8h/24h, logout revokes only the local opaque session, and login again works.
   On first success, prove exactly one enabled source instance is selected,
   the verified `platform_password_login` binding is created, and a session
   for one platform/account cannot read the other platform/account (404).
2. For the finance administrator, retain the central OIDC flow unchanged:
   login, logout and login again. Logout must first revoke the local opaque
   session, then navigate the top window through Keycloak's discovered
   end-session endpoint and return only to `https://invoice.solov.cc/`.
   Terminate a canary session in Keycloak and prove the signed back-channel
   callback immediately invalidates its invoice session; replay the same
   logout token and confirm one immutable replay row and one audit event only.
   Perform administrator LoA2/OTP step-up from an allowed IP. Confirm denial
   from another IP and successful break-glass only through the approved VPN.
   Also confirm public `/admin` is 404 for every source, the admin hostname is
   403 outside the exact allowlist, the allowed Admin Console completes its
   `master`-realm login, and public `solov` discovery/login remains available.
3. Confirm OIDC administrators still use the explicit challenge-based upstream
   binding flow; only a successful platform-password user login may auto-bind,
   and it must never merge identities by email alone.
4. Confirm source agent identity events produce the two connected-source rows.
5. Confirm completed Sub2API balance orders use exact CNY `pay_amount`; test a
   recharge multiplier and subscription conversion. For refunds, confirm the
   cap is `pay_amount-round_CNY(pay_amount*refund_amount/amount)`, including
   full, partial and half-cent cases.
6. Confirm New API `success` appears only in the finance payment-candidate
   queue; an amount above signed `money` fails, one admin leaves it proposed,
   the same admin cannot self-approve, a second admin must enter the identical
   amount/evidence, and neither reviewer can issue the invoice.
   Also confirm an administrator cannot review/adjust/issue/upload or resolve a
   refund case belonging to their own invoice identity.
7. On a verified New API test lot, record a partial manual refund with evidence.
   Confirm unissued reservations are rejected/released, issued exposure enters
   `refund_attention`, the old dual approval is invalidated, source rescan does
   not restore the cap, and only a fresh two-person review of the lower net
   amount can unfreeze it. Repeat with remaining cap `0.00` for full freeze.
8. Save personal and enterprise invoice profiles. A forged browser
   `email_verified=true` must still fail.
9. Submit CNY 199.99 (reject), CNY 200.00 (accept), partial allocation and two
   same-source orders; reject cross-source mixing and duplicate use.
10. Review, begin manual issue, confirm issue, upload a clean static PDF, verify
   ClamAV rejection with EICAR, and verify qpdf rejection of encrypted,
   JavaScript, Launch, external URI and embedded-attachment samples in a
   non-production request. Make the signature volume stale in a canary stack
   and prove upload fails closed even while clamd still responds. Inspect the
   scanner container and prove `network_mode: none`, only the socket volume and
   scanner capability are mounted, and no API/database/document secret or
   ciphertext volume is present; stopping it must make readiness and upload
   fail closed.
11. Confirm email contains no attachment/bearer URL and the authenticated user
    can download only their own encrypted-at-rest PDF.
12. Lose an ingestion ACK, restart the agent and prove byte-identical replay.
13. Apply a Sub2API refund: unissued reservation is invalidated; an issued
    request creates an open refund case; resolve it with red-letter evidence.
14. Run the signed encrypted backup and isolated restore drill. Tamper with a
    copy of the manifest and confirm restore exits before the first `age`
    decrypt; also confirm a symlink/FIFO/PAX archive fixture is rejected.
15. Confirm no secret, OIDC code, upstream JWT, tax ID or email appears in
    Nginx/application logs.

## 11. Backup and restore

Install `age` and OpenSSH, and keep the private age identity offline. Backup
authenticity uses a different Ed25519 signing key and the fixed
`solov-invoice-backup-v1` namespace. Generate it on an offline encrypted volume
(not the production host and never under `BACKUP_DIR`); the dedicated key may
be unencrypted only because the volume itself is encrypted and is mounted for
the approved backup window alone:

```bash
umask 077
ssh-keygen -q -t ed25519 -N '' \
  -C invoice-backup-2026 -f /offline/invoice-backup-signing-2026
printf 'invoice-backup namespaces="solov-invoice-backup-v1" %s\n' \
  "$(cat /offline/invoice-backup-signing-2026.pub)" \
  >/offline/backup-allowed-signers
chmod 0400 /offline/invoice-backup-signing-2026
chmod 0444 /offline/backup-allowed-signers
```

Copy only `backup-allowed-signers` to the production configuration directory
and review it offline. Every non-comment line must use principal
`invoice-backup`, the exact namespace and `ssh-ed25519`; broad namespaces,
wildcard principals and RSA/ECDSA keys are rejected. Temporarily mount the
encrypted signing volume read-only for the command below, then unmount it
immediately after the script self-verifies and publishes the signature.

An API crash after encrypted promotion but before database attach can leave an
unreferenced object. Never remove it manually. In the maintenance window, stop
API/ingest and all ten agents, run the tools-image GC in dry-run mode, review
the random object keys/ages, then execute only with an audited reason. It
rechecks the database immediately before each deletion, accepts only old
`issued/*.pdf.enc` files and fsyncs the directory:

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml stop api ingest-proxy
docker compose --env-file deploy/.env.production -f deploy/docker-compose.sources.yml stop \
  sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances \
  newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm --pull never document-gc \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --document-root /data/documents --minimum-age 24h
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm --pull never document-gc \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --document-root /data/documents --minimum-age 24h --execute \
  --maintenance-confirmed --reason 'scheduled pre-backup orphan cleanup'
```

The backup command below owns the final write freeze and waits for the API and
all source-agent health checks before returning them to service:

```bash
BACKUP_DIR=/root/invoice-system/backups \
AGE_RECIPIENT_FILE=/root/invoice-system/config/backup-recipients.txt \
SOURCE_STATE_ROOT=/root/invoice-system/source-state \
SOURCE_CUTOVER_ROOT=/root/invoice-system/source-state/cutover \
BACKUP_SIGNING_KEY_FILE=/mnt/offline-signing/invoice-backup-signing-2026 \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
PRODUCTION_ENV_FILE=/root/invoice-system/app/deploy/.env.production \
BACKUP_QUIESCE_CONFIRMED=YES \
BACKUP_LOCAL_KEYCLOAK=true \
  bash deploy/backup/backup.sh
```

The script first stops API/ingest and all ten agents, then captures PostgreSQL,
the encrypted document volume, every complete source state directory (including
pending spool, reconciliation inventory and lock metadata), receiver/document
indexes and optional local Keycloak data within one write-freeze window. Failure
cleanup attempts to restart all quiesced services. It intentionally does not
copy the field keyring, age private identity, ten spool keys, two cutover keys
or two balance-snapshot keys; back those up separately/offline and keep each
key paired with its state archive.
The checksum manifest is not a valid backup package until the script has made
an OpenSSH Ed25519 `.sha256.sig`, verified it against the reviewed signer file,
and atomically published both. Unsigned/orphaned components are incomplete and
must never enter retention as a successful backup.

Run `deploy/backup/restore-drill.sh` against every release backup with the
matching manifest and signature, source UUIDs, offline age identity and field
keyring. Signature verification and exact component-name validation occur
before any checksum or `age` decryption. The drill's streaming archive parser
allows only bounded directories/regular files and rejects duplicate/traversal
paths, links, devices, FIFO/socket entries, PAX/xattr metadata and resource
overruns before extraction. It then restores PostgreSQL, compares migration,
receiver-state, document indexes and `invoice-eligibility-policy.csv`
byte-for-byte. The policy comparison covers UTC start, display timezone, both
payment/usage booleans and version, and independently requires the fixed
`2026-08-31T16:00:00Z|Asia/Shanghai|t|t|1` value. It validates all ten source
state envelopes; each of the eight V3 economic states must load the exact
source contract and a cutover/database clock strictly before the same policy.
Finally `invoice-backup-verify` authenticates/decrypts a bounded document
sample and compares plaintext size/SHA-256 to the restored database. A backup
is invalid until all checks pass.

The isolated restore PostgreSQL uses a non-persistent tmpfs with a 16 GiB
default, sized for the current approximately 4.3 GiB restored database plus
indexes and restore-time headroom. `RESTORE_POSTGRES_TMPFS_SIZE` accepts only a
lowercase integer `8g` through `32g`; do not reduce it to fit a constrained
host. Before creating a Docker network or container, the drill requires both
Linux `MemAvailable` and the Docker daemon memory total to be at least the
chosen tmpfs ceiling plus a fixed 4 GiB reserve. An unreadable memory value or
insufficient capacity fails closed. The detached PostgreSQL container uses
`--rm`, and the exit trap force-removes its exact container and network names,
so success, restore failure, or capacity failure leaves no restore database,
container, network, or volume behind. An inspect error counts as “absent” only
while an independent `docker info` still proves the daemon is reachable;
otherwise cleanup itself fails critically instead of hiding unknown state. Run the drill on a host with more
available memory rather than substituting persistent storage without a
separately reviewed encrypted-at-rest design.

```bash
DATABASE_BACKUP=/root/invoice-system/backups/invoice-TS.postgres.dump.age \
DOCUMENT_BACKUP=/root/invoice-system/backups/invoice-TS.documents.tar.age \
SOURCE_STATE_BACKUP=/root/invoice-system/backups/invoice-TS.source-state.tar.age \
METADATA_BACKUP=/root/invoice-system/backups/invoice-TS.metadata.tar.age \
KEYCLOAK_BACKUP=/root/invoice-system/backups/invoice-TS.keycloak.dump.age \
BACKUP_MANIFEST=/root/invoice-system/backups/invoice-TS.sha256 \
BACKUP_SIGNATURE=/root/invoice-system/backups/invoice-TS.sha256.sig \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
AGE_IDENTITY_FILE=/offline/backup-age-identity.txt \
FIELD_KEYRING_FILE=/offline/invoice_field_keyring.json \
SOURCE_SPOOL_KEY_ROOT=/offline/source-spool-keys \
SUB2API_SOURCE_ID="$SUB2API_SOURCE_ID" NEWAPI_SOURCE_ID="$NEWAPI_SOURCE_ID" \
SUB2API_RUNTIME_VERSION=0.1.179 NEWAPI_RUNTIME_VERSION=v1.0.0-rc.25 \
SUB2API_BALANCES_SIGNING_KEY_ID=2026-08-balances \
NEWAPI_BALANCES_SIGNING_KEY_ID=2026-08-balances \
RESTORE_POSTGRES_TMPFS_SIZE=16g \
INVOICE_TOOLS_IMAGE="invoice-system-tools:$INVOICE_IMAGE_TAG" \
SOURCE_AGENT_IMAGE="invoice-source-agent:$INVOICE_IMAGE_TAG" \
  bash deploy/backup/restore-drill.sh
```

### 11.1 OIDC back-channel logout replay retention

`oidc_backchannel_logout_events` is security state, not an ordinary transient
job table. Keep it in every PostgreSQL backup and run retention only after the
new signed/encrypted backup and its isolated restore drill above have both
succeeded. Retain that preceding backup under the normal backup-retention
policy; never restore only this table into a live database because its replay
records and immutable audit trail are one security history.

The maintenance tool is owner-only even if another role is accidentally
granted `DELETE`; the `invoice_app` runtime role remains limited to
`SELECT/INSERT`. It defaults to a read-only preview, uses the PostgreSQL clock,
holds a dedicated advisory lock, applies bounded statement/lock timeouts and
deletes in batches. A row is eligible only when **both** `received_at` and
`token_expires_at` are strictly older than the retention cutoff. The compiled
minimum is 180 days (`4320h`) and cannot be lowered, so a logout token still in
any replay window cannot be removed by this command. Every delete batch and
the completion marker are inserted into `audit_events` in the same transaction;
an audit failure rolls that batch back.

Quarterly, first preview with the default 365-day retention. Review the count
and cutoff without copying hashes or row contents out of PostgreSQL. For
execution, open an approved maintenance window, stop the API (which is the only
writer of these events), repeat the preview, then provide all three explicit
write controls: `--execute`, `--maintenance-confirmed` and a bounded reason.
The source agents do not write this table, but stop `ingest-proxy` as well so
they spool safely while the API is down:

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm --pull never oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml stop api ingest-proxy

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm --pull never oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  --profile tools run --rm --pull never oidc-logout-retention \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --retention 8760h --batch-size 500 --execute --maintenance-confirmed \
  --reason 'quarterly OIDC logout replay retention after verified backup'

docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml up -d --no-build api ingest-proxy
```

Record the emitted `request_id`, starting eligible count, deleted count, batch
count and cutoff in the maintenance ticket. Confirm the matching
`auth.backchannel_logout.retention.batch` and
`auth.backchannel_logout.retention.complete` audit rows before closing the
window. A busy-lock, timeout, owner mismatch or audit error is a failed
maintenance run; investigate it instead of adding grants, lowering retention
or bypassing the tool with manual SQL.

Omit `KEYCLOAK_BACKUP` only when the matching signed manifest was created with
`BACKUP_LOCAL_KEYCLOAK=false`; the restore rejects a missing or extra component.
To rotate signing keys, first append the new namespace-bound Ed25519 public key
to the reviewed allowed-signers file, run and restore-test one backup with the
new private key, and retain the old public line until every backup signed by it
has expired. Removing an old line early makes those retained backups
intentionally unverifiable. Never reuse the age identity as the signing key or
change the namespace/principal during a rotation.

For a real recovery, keep ingress/API/agents stopped. Restore the matched
database, document archive, all ten source directories and both cutover pairs first; restore the
matching field/spool keys and ownership (`65532`, directories `0700`, state
files `0600`); run the drill and migration verify; only then start the API and
receiver, followed by the ten agents. Never start an agent against restored DB
state while its cursor/pending spool is from a different snapshot.

### 11.2 影子评估 / shadow evaluation (release rehearsal, XM-INV-SHADOW-EVAL)

`deploy/rehearsal/shadow-eval.sh` restores the database component of a signed
production backup into a throwaway, isolated PostgreSQL container, brings its
schema up to the **candidate** tools image's own migration set (running that
image's `invoice-migrate` entrypoint -- the restored backup is normally still
at the *running* release's migration set, one or more steps behind the
candidate under rehearsal, exactly like `deploy/roll-forward.sh` always runs
`invoice-migrate` first against real production), and then runs the
candidate's eligibility-projection worker
(`ProcessEligibilityProjectionJobs`/`EligibilityProjectionHealth`, via the
tools image's `invoice-eligibility-shadow` entrypoint) against that
restored-and-migrated copy, reporting the delta in open freezes and
projection errors versus the state immediately after restore-and-migrate.
This makes the verdict explicitly "candidate schema + candidate evaluator
against production data", not merely "candidate evaluator against whatever
schema the backup happened to be taken at". It never starts, stops, restarts
or connects
to any container of the running `invoice-system-prod`, `invoice-system-prod`
source-agent, or `invoice-system-idp` Compose projects; every container and
network it creates is unnamed-project, freshly created, and torn down (with
its cleanup trap, mirroring `deploy/backup/restore-drill.sh`) before the
script exits, success or failure.

**When:** run this once per release candidate whose diff touches the
eligibility evaluator or projection worker --
`backend/internal/postgresstore/consumption.go`'s
`ProcessEligibilityProjectionJobs`/`processEligibilityProjectionJob`/
`buildEligibilityProjectionTx`/`ensureBalanceCarryForwardProofTx` family, any
of the `balance_anchor_repair.go`/`balance_blip_repair.go`/
`eligibility_repair.go` repair logic, or a migration that changes
`eligibility_freezes`, `balance_checkpoint_evaluations`,
`balance_carry_forward_evaluations` or `eligibility_projection_jobs`. This
incident class (XM-INV-BLIP-SOFTFAIL: a projection batch's evaluator error
looping a whole account's retries instead of failing that account alone) is
exactly what this rehearsal exists to catch before it reaches production,
against real production data instead of a synthetic fixture.

**How:** run it on the server, after the candidate's nine images are loaded
(the tag must already be `docker image inspect`-able) and before
`deploy/roll-forward.sh` rolls that candidate forward. It needs the same
signed-backup authenticity inputs as the restore drill above
(`BACKUP_ALLOWED_SIGNERS_FILE`, `AGE_IDENTITY_FILE`) but, unlike the restore
drill, not the field keyring or the ten source spool/cutover keys --
eligibility projection never touches documents or source-state archives, so
this rehearsal checks only the database backup component's line in the
signed manifest, not the full backup-integrity set:

```bash
BACKUP_DIR=/root/invoice-system/backups \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
AGE_IDENTITY_FILE=/offline/backup-age-identity.txt \
  bash deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rcNN
```

Add `--backup invoice-TIMESTAMP` to pin a specific backup instead of the
newest signed one under `BACKUP_DIR`, `--max-rounds N` (default 200) to bound
how many `ProcessEligibilityProjectionJobs` rounds it will run before giving
up on draining the queue, and `--batch-limit N` (default 25, same cap the
worker itself enforces) to change accounts claimed per round.

**`--reproject-all` is mandatory for any release that changes the evaluator,
the projection, or a migration that feeds either.** Without it this rehearsal
proves nothing about such a release, and it will not tell you so: the tool
drains whatever is already in `eligibility_projection_jobs`, and a healthy
production's queue is empty by definition, so a backup restored from one gives
the candidate evaluator no account to run against. RC78, RC79 and RC88 each
shipped an evaluator or migration change past a "ready" verdict produced that
way, with `before` and `after` byte-identical (XM-INV-SHADOW-EVAL-VACUOUS).

With the flag, the run queues one job per account at that account's own
`finalized_through` before taking its baseline, and the report carries
`accounts_enqueued` and `accounts_projected`. If `accounts_projected` is zero
the verdict is `not_ready` (exit 3) — a run that was asked to reproject
everything and reprojected nothing must never read as a pass.

Do not read `rounds_run` as evidence that the rehearsal did anything.
Production has eight accounts and the batch limit is twenty-five, so a
complete, genuine run reports `rounds_run: 1` and `queue_drained: true` —
exactly what a vacuous run reports. `accounts_projected` is the only field
that separates them.

The `invoice-migrate` step needs no additional inputs: it runs inside the
same isolated network, reuses the identical read-only database-url secret
bind-mount and non-root-uid handling already prepared for
`invoice-eligibility-shadow`, and reads its own connection from the
`DATABASE_URL_FILE` environment variable (it takes no command-line flags at
all, unlike `invoice-eligibility-shadow`'s `--database-url-file`). It
deliberately runs with `APP_ENV` unset (skipping its production-only
`ELIGIBILITY_START_AT` precondition -- this is a throwaway rehearsal
database, never production) and with `MIGRATION_MODE` unset (selecting its
default apply mode, not the read-only verify-only mode). If this step fails
-- a migration that cannot apply cleanly against real production data is
exactly the kind of defect this rehearsal exists to catch before it reaches
production too -- the rehearsal stops immediately with exit `1` and an
explicit `tooling_failure` marker (see below), never a `not_ready` verdict: a
broken migration is a defect in the candidate release itself, to be
root-caused and fixed directly, not a projection-worker regression this
rehearsal's verdict machinery is designed to characterize.

**How to read the report:** it writes
`/root/invoice-system/rehearsals/<stamp>/shadow-eval.json` (the full
machine-readable report: per-account eligibility status and open freezes
before/after, open freezes by reason before/after, balance-evidence
evaluations by status before/after, every account left `failed` and every
round's captured error) and `shadow-eval-summary.txt` (the same script's
human-readable rendering, also printed to stdout). The important fields:

- `verdict` / `verdict_reason`: `"ready"` or `"not_ready"`, computed by
  comparing the `after` snapshot's open freeze-reason categories against the
  `before` snapshot taken immediately after restore -- a **new** category, or
  any `round_errors`/`failed_accounts` entry, means `not_ready`. A
  pre-existing reason's open count merely growing does not.
- `queue_drained`: `true` when nothing was left immediately claimable at the
  end (whether because the queue emptied or every remaining row is on a
  future backoff), `false` when it hit `--max-rounds` still finding claimable
  work -- treat `false` as inconclusive and re-run with a higher
  `--max-rounds`, not as a pass.
- `new_freeze_reasons`: the exact set of newly appearing categories, if any.
  `shadow-eval-summary.txt`'s "freeze reason deltas" line additionally shows
  every reason's before/after count and delta (e.g. `SOURCE_GAP 79 -> 85
  (+6)`) -- purely informational, never itself a factor in the verdict; a
  pre-existing reason's count growing (ordinary source-stream lag opening
  more of an already-known freeze type) is expected operation, not a
  regression signal.
- `migrations_applied`: the migration files the `invoice-migrate` step newly
  applied to the restored backup before the projection worker ran (`null`/
  "none" when the backup was already at the candidate's migration set). A
  populated list here is expected and healthy for any candidate that ships a
  migration; it is what makes the verdict explicitly about the candidate's
  *combined* schema-plus-evaluator change, not the evaluator alone.

An empty or unparseable `shadow-eval.json` (the tools container never got far
enough to print a real report -- a real production run hit this twice: once
from a secret-permission error, once from a migration-set mismatch before the
`invoice-migrate` step existed) is overwritten with an explicit
`{"tooling_failure": true, "reason": ..., "tool_exit_code": ..., "log_file":
...}` marker instead of being left empty or missing. Both the human summary
and the verdict computation recognize this marker and refuse to compute
`ready`/`not_ready` from it -- they report a distinct execution failure
instead, so an infrastructure or tooling problem can never be silently read
as either a passing or a regressing verdict.

The script's own exit code is release-blocking and is **independently
recomputed from the published JSON** in bash
(`deploy/rehearsal/shadow-eval-lib.sh`), not merely propagated from the
`invoice-eligibility-shadow` process -- the two are compared and a
disagreement is itself treated as a rehearsal-tooling failure (exit `1`),
matching this runbook's usual double-checked gates. Exit `0` means ready to
proceed; exit `3` means the candidate regressed and must not be rolled
forward as-is; exit `2` is a usage error (bad flag, image not loaded); any
other non-zero exit -- including a failed `invoice-migrate` step or the
`tooling_failure` marker case above -- is an execution failure
(decrypt/restore/signature/migration/Docker problem) with no verdict at all.

**RC plan template step:** for any RC plan whose Task 1 scope matches the
"when" list above, add this bullet to Task 1, after the full test suite and
before the tag is created:

```
- [ ] Run deploy/rehearsal/shadow-eval.sh against the newest signed backup with
      this RC's candidate image tag, adding --reproject-all whenever this RC
      changes the evaluator, the projection, or a migration feeding either
      (without it the rehearsal cannot exercise the change at all, and will
      still say "ready"); require verdict "ready" (exit 0). A
      "not_ready" verdict (exit 3) blocks the tag until the report's
      new_freeze_reasons/round_errors/failed_accounts are root-caused and
      fixed, not silently re-run past. A failed invoice-migrate step inside
      the rehearsal (exit 1, no verdict) blocks the tag the same way -- fix
      the migration itself before retrying.
```

## 12. Rollback

Before first public traffic, an image-only rollback is allowed only when the
previous image explicitly declares the current migration set compatible.
Migration 0011 is not compatible with RC17. Its rollback requires the exact
signed pre-0011 package: RC17 checkout/images, invoice database, documents, all
ten source-state directories, both create-only cutover pairs and their matching
keys. After traffic is accepted:

- never point DNS at an older writable database;
- stop user/source ingress before restoring a database;
- preserve current database/documents/audit as incident evidence;
- application images may be rolled back only if their declared migration
  compatibility includes the current schema;
- after migration 0013 or 0014 is registered, never roll back to the pre-RC39 invoice
  API image: its readiness path scans the parked-event backlog. Keep ingress
  closed and forward-fix, or restore the complete matched pre-RC39 snapshot in
  an isolated stack;
- otherwise restore the matched database + document backup together into a new
  isolated stack, validate, then switch the vhost;
- after all ten source agents are stopped, disconnect the two upstream
  database containers from their invoice projection networks and remove those
  empty networks; never disconnect either database from its original upstream
  network;
- before disconnecting, use maintenance wrapper mode `rollback-bridge` with
  stop/backup acknowledgements as the cluster superuser/current DB owner. It
  refuses active readers or inventory/ACL drift, drops only the exact five V4
  functions/schema with `RESTRICT`, explicitly revokes reviewed grants, then
  directly drops seven roles. Unknown ACL/type/ownership makes `DROP ROLE` fail
  and rolls back everything; `DROP OWNED` is forbidden. It restores the
  deployment-before `PUBLIC TEMPORARY` baseline. Use legacy reconcile only when
  Bridge V4 was never installed;
- OIDC configuration rollback re-enables prior login methods but must not
  delete IdP bindings or silently merge accounts;
- console-assertion rollback (CR-0006, section 4.1): flip whichever of
  `CONSOLE_ASSERTION_ENABLED`/`OIDC_ADMIN_LOGIN_ENABLED` was most recently
  changed back to its prior value and restart `api`; zero migration,
  zero schema change, Keycloak unaffected either direction.

## 13. Final go/no-go

Go only when every canary item passes, backup restore is proven, no P0/P1
security issue remains, the finance/legal issuer values are approved, and the
operator has recorded exact image/source/database/config versions. The chosen
OIDC provider itself must also have an approved immutable-image scan or a
reviewed managed-service assurance record; the rejected Keycloak/ZITADEL
images in `docs/IMAGE-SCAN-REVIEW.md` are explicit NO-GO inputs. Otherwise keep
the public menu disabled and the source agents stopped.
