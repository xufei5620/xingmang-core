# RC52 Release Recovery Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC52 is fixed at signed tag
> `v0.1.0-rc52-signed`; exact1 is retained host-NAT failure evidence. Continue
> only with `docs/superpowers/plans/2026-09-02-invoice-rc71-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC52 without moving or reusing failed RC51 identity or evidence.

**Architecture:** All release JSON uses the centralized duplicate-checking parser with `-DateKind String` under PowerShell 7.5+. The image gate's sole expected pending-canary result is exit `42`; ordinary and strict verifiers must then exit `0`.

**Tech Stack:** PowerShell 7.5+, Git signed commits/tags, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0, CycloneDX 1.7.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- `v0.1.0-rc51-signed` stays fixed at `229ca5bea04e9fa8384fa308e342fcf5f6b6332f`; RC51 exact1 must match `docs/RC51-FAILURE-EVIDENCE-SHA256SUMS.txt`.
- RC49 and RC50 tags/evidence remain fixed under their existing anchors.
- RC52 uses only `v0.1.0-rc52-signed`, `releaseName=0.1.0-rc52`, nine exact `:0.1.0-rc52` references, and a new `release/0.1.0-rc52-exactN` directory.
- No severity, `ignoreUnfixed`, exception tuple, raw finding, tag, or failed evidence may be weakened or rewritten.

### Task 1: Verify and tag the RC52 source

- [ ] From `K:\发票\wt-XM-INV-SEC-RC49`, require PowerShell 7.5+, then run `verify-rc49-failure-evidence.ps1`, `verify-rc50-failure-evidence.ps1`, `verify-rc51-failure-evidence.ps1`, `test-release-image-gate.ps1`, and `verify.ps1`; capture each `$LASTEXITCODE` immediately and require `0`.
- [ ] Run release-range gitleaks from `08aff147766c046b12e19221a6aabb675485d452..HEAD`, `git diff --check`, `git status --porcelain=v1`, and `git verify-commit HEAD`; capture every native exit immediately and require a clean worktree.
- [ ] Create `v0.1.0-rc52-signed` only if absent, verify its signature, and require fully qualified `refs/tags/v0.1.0-rc52-signed^{}` to equal `HEAD`. Never move an existing tag.

### Task 2: Produce one strict-ready RC52 exact directory

- [ ] In one PowerShell block, bind the exact worktree, signed tag and `HEAD`; choose the first unused `release/0.1.0-rc52-exactN` for `N=1..99`.
- [ ] Run `release-image-gate.ps1` with release/image tag `0.1.0-rc52`, Keycloak mode, and source-agent `0.3.0`; require exit `42` exactly.
- [ ] Immediately run ordinary artifact verification and then strict verification with `v0.1.0-rc52-signed`; require exits `0` and `0`. Do not parse or coerce manifest decisions in the plan.

### Task 3: Sign, transfer, canary, and deploy

- [ ] Re-identify exactly one strict-ready RC52 directory by rerunning the strict verifier, sign its `SHA256SUMS`, and verify the detached signature before transfer.
- [ ] Execute `docs/PRODUCTION-RUNBOOK.md` sections 4, 5, 6, 7, 8, 9, 10, 11 excluding 11.1, 12, and 13. Sections 3.1, 3.2, 11.1, and the conditional unactivated-v4 replacement path require separate approval.

Production stays blocked until the real canary and rollback evidence bind the RC52 tag, strict-ready directory, nine image IDs, backup, and rollback point.
