# RC53 Release Recovery Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC53 shipped at signed tag
> `v0.1.0-rc53-signed` (`9bbae4a`) and is deployed; its evidence is immutable.
> Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc83-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC53 without reusing the host-NAT-failed RC52 identity or exact1.

**Architecture:** Keep the PowerShell 7.5+ centralized DateKind String parser and exit-42 pending-canary protocol unchanged. Run all source and image gates only after the local PostgreSQL 15 host-port NAT path is healthy.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- `v0.1.0-rc52-signed` stays fixed at `adf152771e779e6a1bd7a95b3e628d5fa85c3b3f`; RC52 exact1 must match its tracked anchor.
- RC49, RC50, and RC51 anchors/tags remain immutable.
- RC53 uses only `v0.1.0-rc53-signed`, `releaseName=0.1.0-rc53`, nine exact `:0.1.0-rc53` references, and one new `release/0.1.0-rc53-exactN`.

### Task 1: Source identity

- [ ] From the exact candidate worktree, verify PowerShell 7.5+, all four failure-evidence scripts, targeted tests, full `verify.ps1`, release-range gitleaks, diff, clean status, and signed `HEAD`; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc53-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC53 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC53 directory, transfer only its nine manifest-bound images, and execute runbook sections 4–13 with the recorded exclusions and separate approvals intact.

Production remains blocked until real canary and rollback evidence bind RC53.
