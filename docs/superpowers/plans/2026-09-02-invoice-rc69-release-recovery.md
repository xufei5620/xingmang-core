# RC69 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC69 — the settlement lock-contention fix (XM-INV-PROOF-CONTENTION: exponential BALANCE_PROOF_PENDING backoff preserved across requeues, non-blocking per-account advisory locks in the source-projection worker, the projection job locking only its write phase, worker fault isolation, set-based carry-forward proof evaluation, EVENT_DEAD attribution from the application layer) plus the toolchain tails (XM-INV-TOOLCHAIN0: per-worktree integration databases, Trivy cache refresh script, detached ceremony runner).

**Architecture:** RC69 is a code-only roll-forward from RC68: no schema migrations, no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts (refresh with `scripts/refresh-trivy-cache.ps1` when its `NextUpdate` has passed).

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`; `docs/handoffs/XM-INV-PROOF-CONTENTION.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc68 (`ca6fa80`) stay fixed; RC68 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC69 uses only `v0.1.0-rc69-signed`, `releaseName=0.1.0-rc69`, nine exact `:0.1.0-rc69` references, and one new `release/0.1.0-rc69-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, where the rc49–rc52 evidence directories live), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc69-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` so the tool sandbox timeout and the hidden-console code page cannot interfere), bind worktree/tag/HEAD, select the first unused RC69 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC69 directory, transfer only its nine manifest-bound images, reuse the RC68 pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only), run `bash deploy/roll-forward.sh <sha>`, and record deployment evidence beside the release. No host nginx change is carried by RC69.

Production remains blocked until the credentialed human canary (a heavy account's projection completing without BALANCE_PROOF_PENDING hammering, source agents with zero restarts across a busy cycle) binds RC69.
