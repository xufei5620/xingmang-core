# RC62 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC62 shipped at signed tag
> `v0.1.0-rc62-signed` (`2ceade0`) and is deployed; the stream wedge
> fully recovered behind it (public readyz 200); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc76-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC62 — eligibility-cutover dependency waits now park as parked_identity instead of holding the active scan cycle: one customer's half-provisioned login must never freeze a whole stream behind the one-active-cycle constraint.

**Architecture:** RC62 is a code-only roll-forward from RC54: a one-line status-classification change in postgresstore.MarkSourceEventWaitingDependency plus the updated baseline-member integration invariant. No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc61 (`0c92758`) stay fixed; RC61 shipped to production with the embedded platform scoping, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC62 uses only `v0.1.0-rc62-signed`, `releaseName=0.1.0-rc62`, nine exact `:0.1.0-rc62` references, and one new `release/0.1.0-rc62-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (including the parked-baseline-member cycle invariant), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc62-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC62 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC62 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc62`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC62.
