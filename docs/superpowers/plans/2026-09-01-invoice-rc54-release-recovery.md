# RC54 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC54 shipped at signed tag
> `v0.1.0-rc54-signed` (`854f3ff`) and is deployed, but its credentialed
> canary failed at session issuance (auth_sessions.roles NOT NULL vs nil
> platform-principal roles); its evidence is immutable. Continue only with
> `docs/superpowers/plans/2026-09-01-invoice-rc62-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC54 — the platform auto-detect login plus the projected-identity claim fix (production 403) and the non-active-user claim rejection — on top of the deployed RC53 baseline.

**Architecture:** RC54 is a code-only roll-forward from RC53: no schema migrations, no deploy-file changes. Keep the exit-42 pending-canary protocol unchanged. The Trivy cache volume may be pre-seeded from digest-verified OCI artifacts when the container network cannot reach the DB registries; the gate's own freshness assertions remain the authority.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- `v0.1.0-rc53-signed` stays fixed at `9bbae4a1868ce054f14381d32079254139f9ece9`; RC53 shipped and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC54 uses only `v0.1.0-rc54-signed`, `releaseName=0.1.0-rc54`, nine exact `:0.1.0-rc54` references, and one new `release/0.1.0-rc54-exactN`.
- Toolchain/environment failures (network, Trivy DB download) re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc54-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC54 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC54 directory, transfer only its nine manifest-bound images, take a fresh pre-deploy backup, roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc54`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC54.
