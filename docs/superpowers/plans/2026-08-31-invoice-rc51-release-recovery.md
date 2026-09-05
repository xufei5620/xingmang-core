# RC51 Release Recovery Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC51 is fixed at signed tag
> `v0.1.0-rc51-signed`; exact1 is retained failed evidence. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc99-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to execute this plan task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC51 without moving or reusing the failed RC50 tag or exact1 directory.

**Architecture:** RC51 starts from a new clean signed commit in the fixed candidate worktree. The image gate uses exit `42` only for the sole expected pending-canary state; ordinary and strict artifact verifiers remain the transfer authority.

**Tech Stack:** PowerShell 7, Git signed commits/tags, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0, CycloneDX 1.7.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Global constraints

- `v0.1.0-rc50-signed` remains fixed at `d08b3a2e40e55f7f600759c250f45b16b82bd0e1`.
- `release/0.1.0-rc50-exact1` and all RC49 failed directories must match their tracked SHA-256 anchors and be treated as read-only.
- RC51 uses only `v0.1.0-rc51-signed`, `releaseName=0.1.0-rc51`, nine exact `:0.1.0-rc51` references, and an unused `release/0.1.0-rc51-exactN` directory.
- No severity reduction, `ignoreUnfixed` change, generic allowlist, raw-finding rewrite, tag move, or evidence overwrite is allowed.

### Task 1: Verify and sign the RC51 source identity

- [ ] **Step 1: Bind the exact worktree and run every local prerequisite with immediate native-exit checks**

```powershell
$expectedWorktree = (Resolve-Path 'K:\发票\wt-XM-INV-SEC-RC49').Path
$worktreeLines = @(git rev-parse --show-toplevel)
$worktreeExit = $LASTEXITCODE
if ($worktreeExit -ne 0 -or -not [string]::Equals(($worktreeLines -join '').Trim(), $expectedWorktree, [StringComparison]::OrdinalIgnoreCase)) { throw 'wrong RC51 candidate worktree' }

foreach ($gateScript in @(
  '.\scripts\verify-rc49-failure-evidence.ps1',
  '.\scripts\verify-rc50-failure-evidence.ps1',
  '.\scripts\test-release-image-gate.ps1',
  '.\scripts\verify.ps1'
)) {
  pwsh -NoProfile -File $gateScript
  $gateExit = $LASTEXITCODE
  if ($gateExit -ne 0) { throw "RC51 prerequisite failed with exit $gateExit: $gateScript" }
}
gitleaks git --redact --no-banner --log-opts="08aff147766c046b12e19221a6aabb675485d452..HEAD"
$gitleaksExit = $LASTEXITCODE
if ($gitleaksExit -ne 0) { throw "RC51 release-range gitleaks failed with exit $gitleaksExit" }
git diff --check
$diffExit = $LASTEXITCODE
if ($diffExit -ne 0) { throw "RC51 diff check failed with exit $diffExit" }
$statusLines = @(git status --porcelain=v1)
$statusExit = $LASTEXITCODE
if ($statusExit -ne 0) { throw "RC51 git status failed with exit $statusExit" }
if ($statusLines.Count -ne 0) { throw 'RC51 source worktree is dirty' }
git verify-commit HEAD
$commitVerifyExit = $LASTEXITCODE
if ($commitVerifyExit -ne 0) { throw "RC51 commit signature verification failed with exit $commitVerifyExit" }
```

- [ ] **Step 2: Create and verify the exact RC51 tag without moving prior tags**

```powershell
git show-ref --verify --quiet refs/tags/v0.1.0-rc51-signed
$existingTagExit = $LASTEXITCODE
if ($existingTagExit -eq 0) { throw 'RC51 tag already exists; never move it' }
if ($existingTagExit -ne 1) { throw "RC51 tag existence check failed with exit $existingTagExit" }
git tag -s -a v0.1.0-rc51-signed -m 'RC51 image-security release candidate'
$tagCreateExit = $LASTEXITCODE
if ($tagCreateExit -ne 0) { throw "RC51 tag creation failed with exit $tagCreateExit" }
git verify-tag refs/tags/v0.1.0-rc51-signed
$tagVerifyExit = $LASTEXITCODE
if ($tagVerifyExit -ne 0) { throw "RC51 tag signature verification failed with exit $tagVerifyExit" }
$tagHeadLines = @(git rev-parse --verify 'refs/tags/v0.1.0-rc51-signed^{}')
$tagHeadExit = $LASTEXITCODE
$headLines = @(git rev-parse --verify HEAD)
$headExit = $LASTEXITCODE
if ($tagHeadExit -ne 0 -or $headExit -ne 0 -or ($tagHeadLines -join '').Trim() -cne ($headLines -join '').Trim()) { throw 'RC51 tag does not peel to candidate HEAD' }
```

### Task 2: Build and strictly verify one RC51 exact directory

- [ ] **Step 1: Run gate exit 42, ordinary verifier 0, then strict verifier 0 in one block**

```powershell
$worktreeLines = @(git rev-parse --show-toplevel)
$worktreeExit = $LASTEXITCODE
$headLines = @(git rev-parse --verify HEAD)
$headExit = $LASTEXITCODE
$tagHeadLines = @(git rev-parse --verify 'refs/tags/v0.1.0-rc51-signed^{}')
$tagHeadExit = $LASTEXITCODE
if ($worktreeExit -ne 0 -or $headExit -ne 0 -or $tagHeadExit -ne 0 -or
    -not [string]::Equals(($worktreeLines -join '').Trim(), (Resolve-Path 'K:\发票\wt-XM-INV-SEC-RC49').Path, [StringComparison]::OrdinalIgnoreCase) -or
    ($headLines -join '').Trim() -cne ($tagHeadLines -join '').Trim()) { throw 'RC51 worktree/tag/HEAD binding failed' }
$rc51ReleaseDirectory = 1..99 | ForEach-Object { "release\0.1.0-rc51-exact$_" } | Where-Object { -not (Test-Path -LiteralPath $_) } | Select-Object -First 1
if ([string]::IsNullOrWhiteSpace($rc51ReleaseDirectory)) { throw 'no unused RC51 exact directory remains' }
pwsh -NoProfile -File .\scripts\release-image-gate.ps1 -ReleaseName 0.1.0-rc51 -ImageTag 0.1.0-rc51 -SourceAgentVersion 0.3.0 -ReleaseDirectory $rc51ReleaseDirectory -IdPMode keycloak
$imageGateExit = $LASTEXITCODE
if ($imageGateExit -ne 42) { throw "RC51 image gate expected exit 42, got $imageGateExit" }
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 -ReleaseDirectory $rc51ReleaseDirectory
$ordinaryVerifyExit = $LASTEXITCODE
if ($ordinaryVerifyExit -ne 0) { throw "ordinary RC51 artifact verification failed with exit $ordinaryVerifyExit" }
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 -ReleaseDirectory $rc51ReleaseDirectory -RequireTransferReady -SignedReleaseTag v0.1.0-rc51-signed
$strictVerifyExit = $LASTEXITCODE
if ($strictVerifyExit -ne 0) { throw "strict RC51 artifact verification failed with exit $strictVerifyExit" }
```

### Task 3: Sign, transfer, canary, deploy, and retain rollback evidence

- [ ] **Step 1: Re-identify exactly one strict-ready RC51 directory, then sign and transfer it**

Enumerate `release/0.1.0-rc51-exact*`; rerun the strict verifier above against every candidate and require exactly one exit `0`. Sign that directory's `SHA256SUMS` using section 3 of `docs/PRODUCTION-RUNBOOK.md`, checking every native exit immediately. Transfer only that signed directory and its nine manifest-bound images.

- [ ] **Step 2: Execute the mandatory production sections**

Execute `docs/PRODUCTION-RUNBOOK.md` sections 4, 5, 6, 7, 8, 9, 10, 11 (excluding 11.1), 12, and 13 in order. Sections 3.1 (RC39 one-off), 3.2 (balance cleanup), and 11.1 (quarterly deletion) are not part of RC51 and require separate explicit approval. The section 6 unactivated-v4 replacement path is conditional and must not run unless all of its predicates and a separate maintenance approval are present.

Expected: production remains blocked until the real canary passes and the deployment evidence binds the RC51 tag, strict-ready directory, nine image IDs, backup, and rollback point.
