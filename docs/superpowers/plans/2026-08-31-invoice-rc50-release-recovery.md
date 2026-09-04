# RC50 Release Recovery Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC50 failed and is fixed at signed tag
> `v0.1.0-rc50-signed`; exact1 is retained failed evidence. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc92-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce, sign, verify, canary, and deploy a new RC50 candidate without reusing any failed RC49 tag, image tag, or artifact directory.

**Architecture:** RC50 is a new immutable candidate built from a clean signed source commit. The source/static gate binds the exact RC50 Git tag, manifest release name, and nine image references; the image gate then creates a new evidence directory that remains blocked until the real production canary passes.

**Tech Stack:** PowerShell 7, Git signed commits/tags, gitleaks 8.30.1, Docker BuildKit/Compose, Trivy 0.74.0, CycloneDX 1.7, SSH artifact signatures.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Global Constraints

- The historical RC49 plan and design remain evidence only; do not execute `docs/superpowers/plans/2026-08-31-invoice-rc49-image-security.md`.
- Begin this plan only from the clean, signed RC50 source commit that contains
  this recovery plan and the RC50 strict-verifier changes.
- `v0.1.0-rc49-signed` stays fixed at `eb7b4365d3af30241debe7b1a054b7eed8b94dcd`.
- `release/0.1.0-rc49-exact1`, `release/0.1.0-rc49-exact2`, and `release/0.1.0-rc49-exact3` must be treated as read-only and must match `docs/RC49-FAILURE-EVIDENCE-SHA256SUMS.txt`.
- RC50 uses only `v0.1.0-rc50-signed`, `releaseName=0.1.0-rc50`, image tag `0.1.0-rc50`, and a new `release/0.1.0-rc50-exactN` directory.
- Keep Trivy severity `HIGH,CRITICAL`, `ignoreUnfixed=false`, no ignored vulnerabilities, PostgreSQL zero findings, and the one exact Keycloak tuple already recorded in the spec.
- Do not move a tag, overwrite an artifact directory, or rewrite a raw finding.
  Any non-zero prerequisite exit stops the flow except the image gate's
  documented pending-canary exit, and that exception applies only when the
  generated manifest has `applicationImageGate=passed` and exactly the sole
  reason `idp_self_hosted_pending_canary`. Even then, transfer requires the
  strict verifier and detached signature, and production traffic remains off
  until the real canary passes.

---

### Task 1: Freeze the RC50 source candidate

**Files:**
- Verify: `scripts/test-release-image-gate.ps1`
- Verify: `scripts/verify.ps1`
- Verify: `scripts/verify-rc49-failure-evidence.ps1`
- Verify: `docs/RC49-FAILURE-EVIDENCE-SHA256SUMS.txt`

**Interfaces:**
- Consumes: a clean signed RC50 source commit descended from the release-range base `08aff147766c046b12e19221a6aabb675485d452`.
- Produces: one verified RC50 source identity and exact signed tag for the image gate.

- [ ] **Step 1: Verify the retained RC49 failure evidence anchor**

```powershell
pwsh -NoProfile -File .\scripts\verify-rc49-failure-evidence.ps1
```

Expected: exit `0` and `RC49 failure evidence file set and SHA-256 anchor passed.`

- [ ] **Step 2: Run the focused RC50 source/static gate**

```powershell
pwsh -NoProfile -File .\scripts\test-release-image-gate.ps1
```

Expected: exit `0` and `Release image gate offline/static fixtures passed.`

- [ ] **Step 3: Run the full local gate**

```powershell
pwsh -NoProfile -File .\scripts\verify.ps1
```

Expected: exit `0`. Do not infer success from partial output.

- [ ] **Step 4: Run the exact release-range secret gate**

```powershell
gitleaks git --redact --no-banner --log-opts="08aff147766c046b12e19221a6aabb675485d452..HEAD"
```

Expected: exit `0`; do not add a generic allowlist.

- [ ] **Step 5: Verify and sign the source identity**

```powershell
git diff --check
if ($LASTEXITCODE -ne 0) { throw 'RC50 source diff check failed' }
if (git status --porcelain) { throw 'RC50 source worktree is dirty' }
git verify-commit HEAD
if ($LASTEXITCODE -ne 0) { throw 'RC50 source commit signature verification failed' }
git tag -s -a v0.1.0-rc50-signed -m 'RC50 image-security release candidate'
if ($LASTEXITCODE -ne 0) { throw 'RC50 signed tag creation failed' }
git verify-tag v0.1.0-rc50-signed
if ($LASTEXITCODE -ne 0) { throw 'RC50 tag signature verification failed' }
if ((git rev-parse 'refs/tags/v0.1.0-rc50-signed^{}') -ne (git rev-parse HEAD)) { throw 'RC50 tag/source mismatch' }
```

Expected: clean worktree, valid commit and tag signatures, and exact peeled commit equality.

### Task 2: Build and verify a new RC50 bundle

**Files:**
- Execute: `scripts/release-image-gate.ps1`
- Execute: `scripts/verify-release-image-artifacts.ps1`
- Create: one new `release/0.1.0-rc50-exactN/**` directory.

**Interfaces:**
- Consumes: exact signed RC50 source and nine pinned build definitions.
- Produces: nine `:0.1.0-rc50` images and one independently verified manifest-bound bundle.

- [ ] **Step 1: Run the image gate in a new directory**

```powershell
$rc50ReleaseDirectory = 1..99 |
  ForEach-Object { "release\0.1.0-rc50-exact$_" } |
  Where-Object { -not (Test-Path -LiteralPath $_) } |
  Select-Object -First 1
if ([string]::IsNullOrWhiteSpace($rc50ReleaseDirectory)) { throw 'no unused RC50 exact directory remains' }
pwsh -NoProfile -File .\scripts\release-image-gate.ps1 `
  -ReleaseName 0.1.0-rc50 `
  -ImageTag 0.1.0-rc50 `
  -SourceAgentVersion 0.3.0 `
  -ReleaseDirectory $rc50ReleaseDirectory `
  -IdPMode keycloak
$gateExit = $LASTEXITCODE
$rc50Manifest = Get-Content -Raw -LiteralPath (Join-Path $rc50ReleaseDirectory 'release-manifest.json') | ConvertFrom-Json
if ([string]$rc50Manifest.decisions.applicationImageGate -cne 'passed' -or
    @($rc50Manifest.decisions.reasons).Count -ne 1 -or
    [string]$rc50Manifest.decisions.reasons[0] -cne 'idp_self_hosted_pending_canary') {
  throw "RC50 image gate failed outside the sole pending-canary exception (exit $gateExit)"
}
```

Expected: the selected directory is printed/retained in the current PowerShell
session, all image-policy work completes, and production remains blocked only
by `idp_self_hosted_pending_canary`. Preserve any failed directory; a retry
re-runs the selection block and receives the next unused `exactN`.

- [ ] **Step 2: Run ordinary and strict artifact verification**

```powershell
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 `
  -ReleaseDirectory $rc50ReleaseDirectory
if ($LASTEXITCODE -ne 0) { throw 'ordinary RC50 artifact verification failed' }
pwsh -NoProfile -File .\scripts\verify-release-image-artifacts.ps1 `
  -ReleaseDirectory $rc50ReleaseDirectory `
  -RequireTransferReady `
  -SignedReleaseTag v0.1.0-rc50-signed
if ($LASTEXITCODE -ne 0) { throw 'strict RC50 transfer-ready verification failed' }
```

Expected: both commands exit `0`; strict mode binds the exact tag, source commit, release name, nine references, and sole pending-canary reason.

### Task 3: Sign, transfer, canary, and deploy

**Files:**
- Follow exactly: `docs/PRODUCTION-RUNBOOK.md` section 3 and later production sections.
- Retain: the selected `release/0.1.0-rc50-exactN/**` bundle and deployment evidence.

**Interfaces:**
- Consumes: strict-verifier-passed RC50 bundle plus reviewed offline signing/trust files.
- Produces: detached `SHA256SUMS.sig`, backup/restore proof, canary evidence, exact deployed image IDs, and rollback point.

- [ ] **Step 1: Sign and independently verify `SHA256SUMS`**

Run the exact detached-signature commands in `docs/PRODUCTION-RUNBOOK.md` section 3 with the reviewed offline release key and allowed-signers file.

Expected: signature creation and verification both exit `0` before transfer.

- [ ] **Step 2: Transfer only manifest-bound source, bundle, and nine images**

Follow the runbook's no-build/no-pull checks and compare every transferred image ID to `release-manifest.json`.

Expected: nine exact `:0.1.0-rc50` references and image IDs match; any missing image fails closed.

- [ ] **Step 3: Complete backup, isolated startup, migration, and real canary**

Follow the runbook in order. Keep `productionLaunch=blocked` until OIDC/MFA/logout, platform-login/2FA, authorization, readiness, and rollback proofs pass.

Expected: canary evidence names the RC50 tag, manifest, image IDs, backup, and rollback point. Only then may production traffic be enabled.
