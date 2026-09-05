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
