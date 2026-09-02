# RC71 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC71 — the readiness rule fix (XM-INV-READY-PENDING: a projection job that is legitimately waiting inside its `BALANCE_PROOF_PENDING` backoff window no longer counts as a stuck job for `/readyz`; stuck detection now uses the last attempt time; a rate-limited warning surfaces proof-pending jobs older than an hour).

**Architecture:** RC71 is a code-only roll-forward from RC70: no schema migrations, no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts (refresh with `scripts/refresh-trivy-cache.ps1` when its `NextUpdate` has passed). With the repair of 2026-09-02 applied and no dead events left, the roll-forward verify is expected to reach readyz 200 once the api restarts on RC71.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-READY-PENDING.md`; `docs/handoffs/XM-INV-PROOF-CONTENTION.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc70 (`b9d51f6`) stay fixed; RC70 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC71 uses only `v0.1.0-rc71-signed`, `releaseName=0.1.0-rc71`, nine exact `:0.1.0-rc71` references, and one new `release/0.1.0-rc71-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc71-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC71 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC71 directory, transfer only its nine manifest-bound images, reuse the RC70 pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (readyz 200 sustained while the whale account's projection waits for its balance proof, the admin freeze review completing without new dead events) binds RC71.
