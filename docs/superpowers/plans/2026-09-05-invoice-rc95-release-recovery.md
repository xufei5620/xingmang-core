# RC95 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC95, which ships the last fix of the 2026-09-04 stall's ranking **switched off**, and uses its own rehearsal to decide whether it may ever be switched on:

- **XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3, behind `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`.** With a bound, a projection job cuts its window at the as_of of the limit-th pending balance-evidence item, publishes that, and requeues itself for the rest; unbounded (`0`, the production default in this release) is byte-for-byte the previous behaviour. The rehearsal tool takes the same knob as `--evidence-batch-limit` and records it in the report.

**Architecture:** RC95 is a backend roll-forward from RC93 (RC94 was built, staged and rehearsed but not rolled forward: its differential rehearsal turned out inert, see its plan) with **no migration** and **no behaviour change in production** (the knob defaults to `0`). The change is in one job function plus the rehearsal tool and scripts. No evaluator, projection or allocation logic changes. **The shadow evaluation is run three times: the ordinary `--reproject-all` pass, and the differential pair** (see Task 3). Source agent stays 0.3.2. Release env carries over from RC93 unchanged — `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` is deliberately not set.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` (the fix 3 section) and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc94 (`254d99d`) stay fixed; RC93 is the release in production and its evidence is immutable; RC94 is retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC95 uses only `v0.1.0-rc95-signed`, `releaseName=0.1.0-rc95`, nine exact `:0.1.0-rc95` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc95-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc95-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC95 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC94 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC93 (RC94 was never live) with `INVOICE_IMAGE_TAG=0.1.0-rc95`, `SOURCE_AGENT_VERSION=0.3.2`, and **no** `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`).
- [ ] **Differential rehearsal — the acceptance of fix 3, this time with the bound engaged.** The newest signed backup, `--reproject-all --reevaluate-evidence`, twice with the rc95 tools image: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require, in both reports, `evaluations_cleared` > 0 and equal and `accounts_rewound` == the number of POLICY_ANCHOR accounts (4 at the time of writing); **the bounded run's `projection_version` strictly greater than the unbounded run's for every rewound account** (the proof the bound chunked — RC94's pair failed exactly this, silently); both verdicts `ready`, both 8 of 8 with `pending_accounts` null; `evaluations_by_status` identical; `after.accounts` identical in every field but `projection_version`. Record both report paths. A quantity difference does not block this release (the knob is off in production) but blocks ever switching it on, and is filed as a finding.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch with the RC93 counters (lock timeouts, cycle deferrals, late balances cycles, busy events); confirm the api runs with the knob unset (`ELIGIBILITY_EVIDENCE_BATCH_LIMIT` absent from the container env).

Production remains blocked until a 30-minute readiness watch binds RC95.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.
