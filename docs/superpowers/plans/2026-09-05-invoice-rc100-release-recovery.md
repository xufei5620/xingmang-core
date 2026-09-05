# RC100 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and roll forward RC100, which makes the source runtime pin movable without touching the immutable cutover manifest (XM-INV-SOURCE-RUNTIME-PIN):

- The agent reads `SOURCE_CUTOVER_RUNTIME_VERSION` (unset = same as `SOURCE_RUNTIME_VERSION`, every deployment before RC100) and checks the sealed cutover manifest against it, while batches keep declaring the approved pin; `cutover-init` refuses to seal under a bumped pin.
- The compose file passes `SUB2API_CUTOVER_RUNTIME_VERSION` / `NEWAPI_CUTOVER_RUNTIME_VERSION` through to all ten source agents; the release env carries `0.1.179` and `v1.0.0-rc.25`, copied from `source_cutover_manifests.source_runtime_version`.
- The sync status reports `cutover_runtime_version` beside the approved pin; the console shows 代理声明 / 契约审计 / 切换时.
- The runbook's source-upgrade CAS names the three places and the order; the 2026-09-06 incident (pin bumped to 0.2.1, four agents refusing their manifests) is recorded in the handoff.

**Architecture:** RC100 is a source-agent, api and console roll-forward from RC99, the release in production. **No migration, no evaluator change.** With the cutover variables set to the manifests' own values the manifest checks pass exactly as before. The API side already carries the approved Sub2API pin `0.2.1` (`source_instances` revision 2, the CAS interrupted on 2026-09-06 when the agents refused their manifests), so this roll-forward **completes** that CAS instead of rolling it back: the release env sets `SUB2API_RUNTIME_VERSION=0.2.1` and the recreated agents declare it from their first batch. The bridge contract was re-audited against v0.2.1 beforehand (`usage_logs` and `users` gained columns only). Rollback is the RC99 release directory.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` ("Differential rehearsal, take three") and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc99 (`555613e`) stay fixed; RC99 is the release in production and its evidence is immutable; RC95-RC98 are retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC100 uses only `v0.1.0-rc100-signed`, `releaseName=0.1.0-rc100`, nine exact `:0.1.0-rc100` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc100-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against the disposable PostgreSQL, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks — every exit 0.
- [ ] Create `v0.1.0-rc100-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC100 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions. Record whether the npm audit reached the service.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC100 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC99, the live release, with `INVOICE_IMAGE_TAG=0.1.0-rc100`, `SOURCE_AGENT_VERSION=0.3.2`, `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25`, `SUB2API_CUTOVER_RUNTIME_VERSION=0.1.179`, `NEWAPI_CUTOVER_RUNTIME_VERSION=v1.0.0-rc.25` and `SUB2API_RUNTIME_VERSION=0.2.1`, the last copied from `source_instances.runtime_version`). Check the stage script's `PREV_SHA` against the running release first.
- [ ] Shadow evaluation: skipped by rule (no evaluator or migration change); record the skip.
- [ ] Take a fresh signed pre-deploy backup, check the roll-forward script's own `SHA=`, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch with the RC93 counters (lock timeouts, cycle deferrals, late balances cycles, busy events) plus all ten source agents healthy with zero restarts, the sync status showing 切换时 `0.1.179` / `v1.0.0-rc.25` beside the approved pins, and the api image at `0.1.0-rc100`. Any non-200 readiness, agent restart or new error line: roll back to the RC99 release directory and report.
- [ ] The canary must also show the five Sub2API agents' batches accepted at the declared `0.2.1` (no `ErrVersionConflict` rejections in the api log) and the sync status reading 契约审计 `0.2.1` / 切换时 `0.1.179` for every Sub2API stream. Rollback: the RC99 release directory plus the runbook's CAS back to `0.1.179` (the RC99 agents declare that value).

Production remains blocked until a 30-minute readiness watch binds RC100.

## Known and deliberately not in this release

- Observing the real upstream version (the platform connector's job) and a bridge schema fingerprint (deferred; SQL errors already fail closed).
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-06)

- Task 1: identity bump `04805bb` over `edf65d8` (XM-INV-SOURCE-RUNTIME-PIN: `SOURCE_CUTOVER_RUNTIME_VERSION` in the agent, compose passthrough, `cutover_runtime_version` in the sync status, console labels, runbook section 7, handoff) and `e46eaa8` (RC99's execution record). Every gate 0 on `04805bb`: backend build/vet, full unit and integration suites, agents build/vet/tests, web typecheck/tests/build, release-range gitleaks, gate self-test, shadow static test, the four failure-evidence scripts 0/0/0/0. One docs-only commit followed, `31e5f4c`, rewriting the plan's Task 3 so this roll-forward completes the Sub2API pin CAS interrupted on 2026-09-06 (the API side already at `0.2.1`, revision 2) instead of rolling it back; self-test 0 and release-range gitleaks 0 re-run on it. Tag `v0.1.0-rc100-signed` created on `31e5f4c`, SSH signature verified, peels to `HEAD`; the derived roll-forward script's `SHA=` points at it; the stage script's `PREV_SHA` is RC99 (`555613e…`), the running release, and it writes `SUB2API_CUTOVER_RUNTIME_VERSION=0.1.179`, `NEWAPI_CUTOVER_RUNTIME_VERSION=v1.0.0-rc.25` (both copied from `source_cutover_manifests.source_runtime_version`) and `SUB2API_RUNTIME_VERSION=0.2.1` (copied from `source_instances.runtime_version`) into the release env.
- Task 2, first attempt: `release\0.1.0-rc100-exact1` bound `31e5f4c`, preflight 0, but the image gate exited 1 at the verifier: `required zero-finding image is not approved: web`. Trivy reported seven HIGH findings — CVE-2026-53612, -53613, -53614, -76642, -78408, -78409, -78410 — against `libuuid 2.42.1-r0` in `invoice-system-web`, `invoice-ingest-proxy` and `invoice-postgres`; the other six images were at zero and RC99's exact1 had been clean for all nine, so this is Alpine advisory drift between the two builds, not a code change. Remediation `784885f`: `libuuid=2.42.3-r1` (the version Alpine 3.24 `main` offers, read from `apk policy`) pinned in the three Dockerfiles the same way as `libcrypto3`/`libssl3`, recorded in `docs/IMAGE-SCAN-REVIEW.md`; a local build of the web image showed the upgrade `2.42.1-r0 -> 2.42.3-r1`. Self-test 0 and gitleaks 0 on `784885f`; the signed tag moved to it (nothing had been transferred); the roll-forward script's `SHA=` follows. `exact1` is retained as failed evidence; the second attempt writes `exact2`.
- Task 2, second attempt: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc100-task2-gate-20260905T194829Z-a858`, pid 113008): binding `784885f…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc100-exact2`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`; Trivy HIGH/CRITICAL 0 for eight images and 1 for `invoice-keycloak` (the reviewed `java-21-openjdk-headless` finding, unchanged). Wall time 03:48 → 04:00 local.
- Task 3: transfer complete on the first attempt, all three remote checksums matched. Stage: `RC100-STAGED sha=784885f16c39f4e5a0240a7087f0a4ba52f78e41`; the stage script's `PREV_SHA` (RC99 `555613e…`) matched the running release; the release env carries `INVOICE_IMAGE_TAG=0.1.0-rc100`, `SOURCE_AGENT_VERSION=0.3.2`, `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25`, `SUB2API_RUNTIME_VERSION=0.2.1`, `NEWAPI_RUNTIME_VERSION=v1.0.0-rc.25`, `SUB2API_CUTOVER_RUNTIME_VERSION=0.1.179` and `NEWAPI_CUTOVER_RUNTIME_VERSION=v1.0.0-rc.25`. Shadow evaluation skipped by rule (no evaluator or migration change). Fresh signed pre-deploy backup `invoice-20260905T201230Z` of the running RC99, exit 0. Roll-forward launched at 20:16:10Z from the derived script whose `SHA=` had been checked against the tag commit.
- Task 3, roll-forward outcome: the derived script reported `RC100-ROLLFORWARD-NONZERO=1` at step [4/6] (`api did not report listening`) and skipped steps 5/6; `rc100-watch.sh` aborted on the missing PASS marker. readyz was 200 afterwards with 18 containers on `0.1.0-rc100`, but batch ingestion was failing: the ingest-proxy had been recreated at 20:16:41Z, 26 s before the api container (20:17:07Z), and nginx's static `proxy_pass http://api:8088` had cached a dead upstream address, so every `POST /internal/v1/source-batches` returned 502 (connection refused). The identities agent meanwhile fail-closed on a pending spool batch (sequence 15109, sealed at the old pin `0.1.179`) that the api now rejected with `409 request version conflict`; rolling the pin back to `0.1.179` (env, config CAS, `bootstrap-sources` run by the owner) let identities drain once the ingest-proxy was force-recreated, but stranded the four economic streams' pending spools sealed at `0.2.1` during the window. Converging forward to `0.2.1` (env, config CAS, `bootstrap-sources` and the five-agent recreate, run by the owner) drained all five streams within 60 s: `pending_spools=0`, readyz 200 at 20:43Z, no version conflicts, zero agent restarts. Sync status for every Sub2API stream: approved `0.2.1` / cutover `0.1.179` / observed `0.2.1`.
- Post-recovery watch (`rc100-watch2.log`, 20:48:50Z to 21:18Z): 30/30 minutes `readyz=200 error_lines=0 version_conflicts=0 proxy_refused=0`; end line `RC100-WATCH2-DONE readyz_non200=0 error_lines=0 version_conflicts=0 sub2api_agent_restarts=0 pending_spools=0`. Deployment record `rc100-deploy-20260905T212116Z` (containers-after: 18 on rc100, both watch logs, the roll-forward log, `INCIDENT-NOTE.md`). RC100 is bound.
- Carried into the next slices: the source-upgrade ceremony gains a "pending spools must be empty" gate before any pin change, and the roll-forward recreates the ingest-proxy only after the api confirms listening (or nginx resolves the upstream dynamically).
