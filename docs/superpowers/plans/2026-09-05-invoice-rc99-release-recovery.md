# RC99 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC99 shipped at signed tag
> `v0.1.0-rc99-signed` (`555613e`) and is deployed (fix 3 on at 25, canary
> clean, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc100-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and **roll forward** RC99, which switches XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3 on in production:

- `docker-compose.prod.yml` passes `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` through to the api (`${ELIGIBILITY_EVIDENCE_BATCH_LIMIT:-0}`); it never did, so RC94's variable could not have been set from any release env. `.env.production.example` and `docs/CONFIGURATION.md` document it.
- The release env sets `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25`, the value RC98's differential proved equal to the single pass on the 2026-09-04 incident's own data (both runs ready, the released account replayed its three days once in one pass and once in 22 chunks, every quantity and evaluation identical).
- The rehearsal instrument from RC95-RC98 rides along (tools image only).

**Architecture:** RC99 is a tools-image-only change over RC94, the release in production (RC95 and RC99 were built and staged but not rolled forward; see their plans). **No migration, no behaviour change in production** (the knob stays unset). Whether RC99 rolls forward at all is decided after its differential: production runs none of the changed code.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` ("Differential rehearsal, take three") and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc99 (`43eb05a`) stay fixed; RC94 is the release in production and its evidence is immutable; RC95 and RC99 are retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC99 uses only `v0.1.0-rc99-signed`, `releaseName=0.1.0-rc99`, nine exact `:0.1.0-rc99` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc99-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against the disposable PostgreSQL, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks — every exit 0.
- [ ] Create `v0.1.0-rc99-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC99 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions. Record whether the npm audit reached the service.

### Task 3: Production

- [ ] Sign exactly one strict-ready RC99 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC94, the live release, with `INVOICE_IMAGE_TAG=0.1.0-rc99`, `SOURCE_AGENT_VERSION=0.3.2` and `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25`). Check the stage script's `PREV_SHA` against the running release first.
- [ ] Shadow evaluation: skipped by rule (no evaluator or migration change; the instrument was exercised by RC98's differential); record the skip.
- [ ] Take a fresh signed pre-deploy backup, check the roll-forward script's own `SHA=`, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch with the RC93 counters (lock timeouts, cycle deferrals, late balances cycles, busy events) plus `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25` **present in the api container's environment** and the api image at `0.1.0-rc99`. Any non-200 readiness or new error line: roll back to the RC94 release directory and report.

Production remains blocked until a 30-minute readiness watch binds RC99.

## Known and deliberately not in this release

- Any further tuning of the bound (a value other than 25) -- a new differential first.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-06)

- Task 1: identity bump `555613e` over `f25d5af` (the compose passthrough `ELIGIBILITY_EVIDENCE_BATCH_LIMIT: ${ELIGIBILITY_EVIDENCE_BATCH_LIMIT:-0}`, `.env.production.example` and `CONFIGURATION.md` entries, handoff status) and `7324425` (RC98's execution record). The first bump attempt failed inside the banner script — an apostrophe in the superseded-plan banner text broke a byte-string literal — after the identity renames had already been applied; a Task 1 gate started against that half-bumped tree (`HEAD=f25d5af`, dirty) was stopped, the banner fixed, the bump re-run on the same edits and committed clean. Every gate 0 on `555613e` (see below). Tag `v0.1.0-rc99-signed` created on `555613e`, SSH signature verified, peels to `HEAD`; the derived roll-forward script's `SHA=` points at it; the stage script's `PREV_SHA` stays at RC94 (`254d99dd…`), the running release, and additionally writes `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25` into the release env (the value RC98's differential proved; copied from the runbook, not recalled).
- Task 2: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc99-task2-gate-20260905T155046Z-4d8e`, pid 117044): binding `555613e…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc99-exact1`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`. Wall time 23:50 → 23:59 local.
- Task 3: transfer complete on the first attempt, all three remote checksums matched. Stage: `RC99-STAGED sha=555613e354a55dbe0dce27a33da7db88cd8b8ca1`; the stage script's `PREV_SHA` (RC94 `254d99dd…`) matched the running release; the release env carries `INVOICE_IMAGE_TAG=0.1.0-rc99`, `SOURCE_AGENT_VERSION=0.3.2` and `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25`. Shadow evaluation skipped by rule (no evaluator or migration change; the instrument was exercised by RC98's differential on the incident's own data). Fresh signed pre-deploy backup `invoice-20260905T160355Z` of the running RC94 (signing key on tmpfs), exit 0. Roll-forward launched at 16:07:43Z from the derived script whose `SHA=` had been checked against the tag commit.

### Roll-forward

`ROLL FORWARD PASS: tag=0.1.0-rc99 sha=555613e354a55dbe0dce27a33da7db88cd8b8ca1 containers=18 healthz=200 readyz=200` at ~16:08Z, migrations a no-op. Post-deploy: readyz 200, 18 of 18 containers on `0.1.0-rc99`, api image `invoice-system-api:0.1.0-rc99`, and `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25` **present in the api container's environment** — the first release on which the variable reaches the process. First six minutes: zero `lock timeout` lines, zero `active scan cycle` lines, zero error lines, projection queue empty.

### Canary

30 minutes from 16:08:50Z with the bound on: `readyz_non200=0`, zero error lines, zero reconcile errors, `unclassified_request_failures=0`; usage 27 cycles, credits 27 cycles with zero sync failures; lock timeouts, cycle deferrals, late balances cycles **0, 0, 0**; busy events at the end **0**; `RC99-WATCH-KNOB ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25 image=invoice-system-api:0.1.0-rc99`. Bound-specific: `requeued_now=0`, six projections rebuilt in the window — steady state, where no account has more than 25 pending items in a window and the bound never engages; it is there for the next catch-up. One job was queued at the watch's last sample and had completed by the time the record was cut; readiness stayed 200 throughout. Deployment record `rc99-deploy-20260905T164010Z` holds the containers list, roll-forward log, watch log and transfer checksums.

### Outcome

RC99 is the release in production. XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3 is **on** at 25, the value RC98's differential proved equal to the single pass on the incident's own data. Rollback, if ever needed, is the RC94 release directory (`254d99dd…`) or this release with the variable at 0.
