# RC60 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC60 shipped at signed tag
> `v0.1.0-rc60-signed` (`ae61282`) and is deployed; it carries the
> claim-path binding wake; its evidence is immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc79-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC60 — claim logins now fire the idempotent source_external_account wake, releasing signed-baseline accounts whose parked cutover rows wedged eligibility bootstrap, scan cycles, and the live agent streams; diagnosed from the RC59-era production stream wedge after the operator requeue re-parked.

**Architecture:** RC60 is a code-only roll-forward from RC54: a contained fix across `application.Service`/`cmd/api/runtime.go` (WakeSourceAccountFacts fired on the claim path) plus fake-backed wake assertions. No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc59 (`7975339`) stay fixed; RC59 shipped to production and restored the lot list rendering, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC60 uses only `v0.1.0-rc60-signed`, `releaseName=0.1.0-rc60`, nine exact `:0.1.0-rc60` references, and one new `release/0.1.0-rc60-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (including the claim-path wake assertion), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc60-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC60 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC60 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc60`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC60.
