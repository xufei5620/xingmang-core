# RC71 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC71 shipped at signed tag
> `v0.1.0-rc71-signed` (`ca4c1c0`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc97-release-recovery.md`.

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

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc71-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC71 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production

- [x] Sign exactly one strict-ready RC71 directory, transfer only its nine manifest-bound images, reuse the RC70 pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (readyz 200 sustained while the whale account's projection waits for its balance proof, the admin freeze review completing without new dead events) binds RC71.

## Execution record (2026-09-02)

- Task 1: readiness fix merged (`b9dff67`), identity bump (`ca4c1c0`). Backend 26/26, web 79/79, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc71-signed` -> `ca4c1c0`.
- Task 2: `release/0.1.0-rc71-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified.
- Task 3: transfer verified on the host; staging loaded nine images and verified tag and evidence signatures; the RC70 backup `invoice-20260902T041727Z` was 83 minutes old and reused; `deploy/roll-forward.sh ca4c1c0…` ROLL FORWARD PASS with 18 containers on `0.1.0-rc71`, healthz 200, readyz 200 — production readiness restored for the first time since 02:25Z. Deployment record `deployment-records/rc71-deploy-20260902T054012Z`.
