# RC61 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC61 shipped at signed tag
> `v0.1.0-rc61-signed` (`0c92758`) and is deployed; it carries the
> embedded platform scoping and identity badge; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-02-invoice-rc70-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC61 — the embedded invoice view gains platform scoping (platform=sub2api|newapi narrows lots, eligibility cards, and bound-account guidance to one platform) and an account identity badge; user-requested UX for the per-platform embeds (XM-INV-EMBED-SCOPE, reviewed and merged).

**Architecture:** RC61 is a code-only roll-forward from RC54: a reviewed feature merge (web embedded-scope module + one additive session-response field) plus 17 new frontend tests. No schema migrations, no deploy-file changes. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc60 (`ae61282`) stay fixed; RC60 shipped to production with the claim-path binding wake, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC61 uses only `v0.1.0-rc61-signed`, `releaseName=0.1.0-rc61`, nine exact `:0.1.0-rc61` references, and one new `release/0.1.0-rc61-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (including the embedded-scope suite), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc61-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC61 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC61 directory, transfer only its nine manifest-bound images, reuse the fresh RC54 pre-deploy backup if it is under two hours old (otherwise take a new one), roll the three compose projects forward to `INVOICE_IMAGE_TAG=0.1.0-rc61`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (real platform login issuing a session and landing on the projected identity's invoice data, auto-detect with no platform picker) binds RC61.
