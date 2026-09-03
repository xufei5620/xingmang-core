# RC74 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC74 shipped at signed tag
> `v0.1.0-rc74-signed` (`e38d7c0`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc77-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC74 — CR-0006 phase 2 step 2 on the invoice side (XM-INV-CONSOLE-ASSERT-DEPLOY): `docker-compose.prod.yml` mounts the console-assertion public-key manifest read-only into the api container and declares the `OIDC_ADMIN_LOGIN_ENABLED` / `CONSOLE_ASSERTION_ENABLED` / `CONSOLE_ASSERTION_ISSUER` / `CONSOLE_ASSERTION_AUDIENCE` / `CONSOLE_ASSERTION_KEYS_FILE` keys with dark defaults; `deploy/roll-forward.sh` pre-creates an empty placeholder so Compose never binds a directory; the api reads the keyring only when the feature flag is on.

**Architecture:** RC74 is a deploy-file-changing roll-forward from RC73: no schema migrations, but `docker-compose.prod.yml` changes, so the host's release env must carry `CONSOLE_ASSERTION_KEYRING_FILE=/root/invoice-system/config/console-assertion-keyring.json` (the staging script appends it) and that host path must exist as a file before `up -d` (an empty placeholder was created on 2026-09-02; the real reviewed manifest replaces it in phase 2 step 1). `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy); the api container is recreated because its environment changes. The exit-42 pending-canary protocol is unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** platform repo `docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md` (步骤 2); `docs/handoffs/XM-INV-CONSOLE-ASSERT-DEPLOY.md`; `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc73 (`7061d81`) stay fixed; RC73 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC74 uses only `v0.1.0-rc74-signed`, `releaseName=0.1.0-rc74`, nine exact `:0.1.0-rc74` references, and one new `release/0.1.0-rc74-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- `CONSOLE_ASSERTION_ENABLED` and `OIDC_ADMIN_LOGIN_ENABLED` are NOT changed by RC74; the enable order lives in the phase 2 rollout plan and needs the owner's decisions recorded there.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc74-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC74 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production

- [x] Sign exactly one strict-ready RC74 directory, transfer only its nine manifest-bound images, stage with the RC74 staging script (which appends `CONSOLE_ASSERTION_KEYRING_FILE` to the new release env), confirm the host placeholder file exists, reuse the latest pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm the api container shows the read-only `/config/console-assertion-keyring.json` mount and `CONSOLE_ASSERTION_ENABLED=false`, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (OIDC admin login still working with the feature dark; the api's keyring mount present as a file) binds RC74.

## Execution record (2026-09-03)

- Task 1: XM-INV-CONSOLE-ASSERT-DEPLOY merged (`a6ca8df`), identity bump (`e38d7c0`). Backend full suite green, web typecheck/94 tests/build green, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc74-signed` -> `e38d7c0`.
- Task 2: `release/0.1.0-rc74-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified.
- Task 3: transfer verified on the host; staging loaded nine images, verified tag and evidence signatures, and appended `CONSOLE_ASSERTION_KEYRING_FILE` to the new release env; the RC73 backup `invoice-20260902T213458Z` was 55 minutes old and reused; `deploy/roll-forward.sh e38d7c0…` ROLL FORWARD PASS with 18 containers on `0.1.0-rc74`, healthz 200, readyz 200; the api container mounts `/config/console-assertion-keyring.json` read-only from the host placeholder and reports `CONSOLE_ASSERTION_ENABLED=false`, `OIDC_ADMIN_LOGIN_ENABLED=true`. Deployment record `deployment-records/rc74-deploy-*`.
