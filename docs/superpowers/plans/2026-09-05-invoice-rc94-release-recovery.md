# RC94 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC94 shipped at signed tag
> `v0.1.0-rc94-signed` (`254d99d`) and is deployed (its differential rehearsal
> was inert, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc97-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC94, which ships the last fix of the 2026-09-04 stall's ranking **switched off**, and uses its own rehearsal to decide whether it may ever be switched on:

- **XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3, behind `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`.** With a bound, a projection job cuts its window at the as_of of the limit-th pending balance-evidence item, publishes that, and requeues itself for the rest; unbounded (`0`, the production default in this release) is byte-for-byte the previous behaviour. The rehearsal tool takes the same knob as `--evidence-batch-limit` and records it in the report.

**Architecture:** RC94 is a backend roll-forward from RC93 with **no migration** and **no behaviour change in production** (the knob defaults to `0`). The change is in one job function plus the rehearsal tool and scripts. No evaluator, projection or allocation logic changes. **The shadow evaluation is run three times: the ordinary `--reproject-all` pass, and the differential pair** (see Task 3). Source agent stays 0.3.2. Release env carries over from RC93 unchanged — `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` is deliberately not set.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` (the fix 3 section) and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc93 (`34c7775`) stay fixed; RC93 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC94 uses only `v0.1.0-rc94-signed`, `releaseName=0.1.0-rc94`, nine exact `:0.1.0-rc94` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc94-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc94-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC94 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC94 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC93 with `INVOICE_IMAGE_TAG=0.1.0-rc94`, `SOURCE_AGENT_VERSION=0.3.2`, and **no** `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`).
- [ ] **Differential rehearsal — the acceptance of fix 3.** The newest signed backup, `--reproject-all --reevaluate-evidence`, twice with the rc94 tools image: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require `evaluations_cleared` > 0 and equal in both reports (otherwise the pair evaluated nothing and proves nothing), both verdicts `ready`, both 8 of 8 with `pending_accounts` null, `evaluations_by_status` identical, and `after.accounts` identical in every field but `projection_version`. Record both report paths. **A difference does not block this release** (the knob is off in production) but blocks ever switching it on, and is filed as a finding.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch with the RC93 counters (lock timeouts, cycle deferrals, late balances cycles, busy events); confirm the api runs with the knob unset (`ELIGIBILITY_EVIDENCE_BATCH_LIMIT` absent from the container env).

Production remains blocked until a 30-minute readiness watch binds RC94.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `254d99d`; every gate 0, web 185 tests, four failure-evidence scripts 0/0/0/0; gate self-test 0 on the first run. Tag `v0.1.0-rc94-signed` created and verified, peeling to `HEAD`; the derived roll-forward script's `SHA=` checked against the tag commit before anything ran.
- Task 2: `release/0.1.0-rc94-exact1`, first attempt: binding bound to `254d99dd…`, loopback preflight passed on attempt 1, image gate 42, ordinary verifier 0, strict transfer-ready verifier 0. The audit ran for real (`found 0 vulnerabilities`), waiver cleared, not used.
- Task 3: staged (nine images, tag and evidence signatures good); release env carried over from RC93 with `INVOICE_IMAGE_TAG=0.1.0-rc94` and **no `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`** (the knob stays off in production). The rc94 tools image exposes `-reproject-all`, `-reevaluate-evidence` and `-evidence-batch-limit`; the staged rehearsal script passes all three.

### Differential rehearsal — run, and found inert

Backup `invoice-20260905T010919Z`, rc94 tools image, `--reproject-all --reevaluate-evidence`, `--evidence-batch-limit 0` (report `rehearsals/20260905T092119Z-1670059`) and `25` (`rehearsals/20260905T092816Z-1711075`).

| field | unbounded | bounded |
| --- | --- | --- |
| `evaluations_cleared` | 2743 | 2743 |
| `accounts_projected` / `pending_accounts` | 8 / null | 8 / null |
| `rounds_run` | 1 | 1 |
| `evaluations_by_status` after | matched 3643, negative_frozen 7, positive_blip_ignored 332, positive_classified_non_cash 2 | identical |
| `after.accounts` (all fields incl. `projection_version`) | — | identical, per account |
| verdict | ready | ready |

`--reevaluate-evidence` did what it was built for: 2,743 evaluations cleared and 2,742 re-made by the candidate in one pass per account (the one short of the count is the last deferred item, unwritten by rule). But the two runs are identical **including `projection_version`**, which means the bounded run took exactly one job per account: **the bound never engaged.** The cleared evidence is history — every item's `as_of` lies before the account's published `finalized_through` — and the batch boundary deliberately counts only items after the published boundary (the rule that keeps a deferred item from pinning it). On this copy the bound had nothing to chunk, so the pair compared two unbounded runs. It is not the acceptance the plan asked for, and it is recorded as such.

The repair is in the tool, not the evaluator: `--reevaluate-evidence` now also rewinds every POLICY_ANCHOR account's `finalized_through` to its cutover after the jobs have been queued at the old boundary, so each account's window is its whole evidence history and the cleared items sit in front of the bound (`accounts_rewound` in the report; zero with re-evaluation set means a bounded run had nothing to chunk). That is a Go change and needs a new image; RC94's signed tag is fixed, so the real differential is RC95's.

RC94 itself changes nothing in production — the knob is unset — and was not rolled forward pending the owner's decision.

### Deployed after all

The owner chose to roll RC94 forward before RC95. Signed pre-deploy backup `invoice-20260905T101318Z`; roll-forward PASS, 18 containers on rc94, healthz/readyz 200, migrations a no-op. Post-deploy: api image `invoice-system-api:0.1.0-rc94`, **`ELIGIBILITY_EVIDENCE_BATCH_LIMIT` absent from the api container's environment** (the bound is off; production behaviour is RC93's), zero `lock timeout` / `active scan cycle` lines in the first five minutes, zero busy events, balances cycles publishing with 0–1 s lag. Deployment record `rc94-deploy-20260905T101700Z` holds the containers list, roll-forward log, both differential reports and summaries, the differential log, transfer checksums and the watch log.

### Canary

30 minutes from 10:18:21Z: `readyz_non200=0`, zero error lines, zero reconcile errors, usage 28 cycles, credits 27 cycles with zero sync failures, `unclassified_request_failures=0`; lock timeouts, cycle deferrals, late balances cycles **0, 0, 0**; busy events at the end **0**. One projection job remained queued at the end: the whale's, in `BALANCE_PROOF_PENDING` backoff — the same wait it has had since RC92, benign, and the reason readiness stayed 200 through it is fix 2.
