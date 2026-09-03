# RC66 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC66 shipped at signed tag
> `v0.1.0-rc66-signed` (`3d7e36d`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc84-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC66 — eligibility reprojection jobs get a bounded transaction-local 300s timeout budget so a heavy account's first-login full-history replay cannot wedge at the runtime role's 15s statement_timeout.

**Architecture:** RC66 is a code-only roll-forward from RC54: a set_config block in postgresstore.processEligibilityProjectionJob (retention-worker precedent). No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc65 (`13d7446`) stay fixed; RC65 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC66 uses only `v0.1.0-rc66-signed`, `releaseName=0.1.0-rc66`, nine exact `:0.1.0-rc66` references, and one new `release/0.1.0-rc66-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (web suite 35/35), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc66-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC66 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC66 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc66`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC66.
