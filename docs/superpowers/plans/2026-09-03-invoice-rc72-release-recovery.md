# RC72 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC72 shipped at signed tag
> `v0.1.0-rc72-signed` (`cadf009`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-04-invoice-rc88-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC72 — CR-0006 phase 1 on the invoice side: the console-assertion administrator login (XM-INV-CONSOLE-ASSERT: `POST /api/v1/auth/console-assertion`, Ed25519 JWS verified against a static reviewed key manifest, single-use nonces, `OIDC_ADMIN_LOGIN_ENABLED`/`CONSOLE_ASSERTION_*` configuration, the embedded admin page accepting `admin-assertion` postMessages) and the administrator identity migration tool (XM-INV-IDENTITY-MIGRATE: `invoice-identity-migrate` in the tools image, dry-run/apply, re-encrypting the email ciphertext under the new identity). Both features ship dark: `CONSOLE_ASSERTION_ENABLED` defaults to false and OIDC stays enabled.

**Architecture:** RC72 is a code-only roll-forward from RC71: no schema migrations, no deploy-file changes (the new env vars are documented with safe defaults; the key manifest file is mounted only when the feature is switched on). `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; refresh the Trivy cache with `scripts/refresh-trivy-cache.ps1` when its `NextUpdate` has passed.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** platform repo `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md` and `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`; `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`; `docs/handoffs/XM-INV-IDENTITY-MIGRATE.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc71 (`ca4c1c0`) stay fixed; RC71 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC72 uses only `v0.1.0-rc72-signed`, `releaseName=0.1.0-rc72`, nine exact `:0.1.0-rc72` references, and one new `release/0.1.0-rc72-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- Production ordering for CR-0006 (acceptance-line ruling, 2026-09-03): enable `CONSOLE_ASSERTION_ENABLED` with the console's key manifest first → run `invoice-identity-migrate` (dry-run, then apply with the approved operator) → only after the console path is proven, turn `OIDC_ADMIN_LOGIN_ENABLED` off. Keycloak containers are not stopped by RC72.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc72-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC72 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production

- [x] Sign exactly one strict-ready RC72 directory, transfer only its nine manifest-bound images, reuse the latest pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release. Do not switch `CONSOLE_ASSERTION_ENABLED` on in the same step.

Production remains blocked until the credentialed human canary (OIDC login still working with the feature dark; then, after the platform side XM-INVCON1 is live, one console-issued assertion exchanged for an admin session on the embedded page, the identity migration applied, and the owner logging in through the console only) binds RC72.

## Execution record (2026-09-02/03)

- Task 1: console-assertion login and identity-migrate merged (`2139408`), identity bump (`cadf009`). Backend suite green, web typecheck/tests/build green, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc72-signed` -> `cadf009`.
- Task 2: `release/0.1.0-rc72-exact1` retained as failed evidence (Trivy cache volume rejected as corrupted: the refresh script had written a non-native layout with a zero `DownloadedAt`; the volume was reseeded with Trivy's own downloader and validated by an offline scan). `release/0.1.0-rc72-exact2`: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified.
- Task 3: transfer verified on the host; staging loaded nine images and verified tag and evidence signatures; fresh backup `invoice-20260902T185730Z` (the RC70 backup was older than two hours; signing key on tmpfs, shredded after); `deploy/roll-forward.sh cadf009…` ROLL FORWARD PASS with 18 containers on `0.1.0-rc72`, healthz 200, readyz 200. `CONSOLE_ASSERTION_ENABLED` stays unset; OIDC admin login unchanged. Deployment record `deployment-records/rc72-deploy-*`.
