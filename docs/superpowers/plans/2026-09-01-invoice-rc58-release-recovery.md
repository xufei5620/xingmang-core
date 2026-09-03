# RC58 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC58 shipped at signed tag
> `v0.1.0-rc58-signed` (`a5dbc78`) and is deployed; its canary proved
> multi-platform login and live funding-lot materialization and exposed
> the lot reason-code precedence defect; its evidence is immutable.
> Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc75-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC58 — the claim path now accepts multi-platform identities: a login whose external account is bound to an SSO invoice_user already claimed by its sibling platform lands on that user instead of being rejected as a takeover; found by the RC57 production canary (a NewAPI login onto the Sub2API-claimed SSO identity).

**Architecture:** RC58 is a code-only roll-forward from RC54: a contained fix in `cmd/api/runtime.go` (the claim path drops the platform-columns mismatch rejection; the binding row is the ownership proof) plus a rewritten fake-backed acceptance test. No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- `v0.1.0-rc53-signed` (`9bbae4a`), `v0.1.0-rc54-signed` (`854f3ff`), `v0.1.0-rc55-signed` (`f36ed2b`), `v0.1.0-rc56-signed` (`74a726b`) and `v0.1.0-rc57-signed` (`b29eb83`) stay fixed; RC57 shipped to production, its canary proved the create path end-to-end and exposed the multi-platform claim rejection, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC58 uses only `v0.1.0-rc58-signed`, `releaseName=0.1.0-rc58`, nine exact `:0.1.0-rc58` references, and one new `release/0.1.0-rc58-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (including the rewritten multi-platform claim acceptance test), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc58-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC58 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC58 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc58`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC58.
