# RC99 Release Implementation Plan

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
