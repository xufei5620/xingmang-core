# RC55 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC55 shipped at signed tag
> `v0.1.0-rc55-signed` (`f36ed2b`) and is deployed, but its credentialed
> canary failed at second-user platform binding (UNIQUE NULLS NOT DISTINCT
> subject HMAC, SQLSTATE 23505); its evidence is immutable. Continue only
> with `docs/superpowers/plans/2026-09-01-invoice-rc56-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC55 — the platform-password session fix (auth_sessions.roles/amr NOT NULL vs nil slices, SQLSTATE 23502) found by the first real RC54 production canary login.

**Architecture:** RC55 is a code-only roll-forward from RC54: one two-line fix in `internal/auth/session.go` (Issue and Rotate build Roles/AMR on non-nil empty slices) plus a PostgreSQL integration regression test. No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- `v0.1.0-rc53-signed` (`9bbae4a`) and `v0.1.0-rc54-signed` (`854f3ff`) stay fixed; RC54 shipped to production, its canary failed at session issuance, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC55 uses only `v0.1.0-rc55-signed`, `releaseName=0.1.0-rc55`, nine exact `:0.1.0-rc55` references, and one new `release/0.1.0-rc55-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (including the new platform-session roles/amr regression test), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc55-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC55 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC55 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc55`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC55.
