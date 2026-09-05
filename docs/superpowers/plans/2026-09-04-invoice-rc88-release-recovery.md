# RC88 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC88 shipped at signed tag
> `v0.1.0-rc88-signed` (`0eb602d`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc95-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC88 — one production defect, the one that makes the product's own usage pattern unbillable:

- **XM-INV-OVERAGE-CARRY-FORWARD.** Both sources bill as they go and let a request overdraw; the user's next top-up settles that debt before anything else. `buildEligibilityProjectionTx` dropped the overdrawn units instead of carrying them, so its expected balance stayed permanently above the source's by exactly the overdrawn amount. Every checkpoint after an overdraw therefore read as an unexplained negative difference, which resets the pending-reconciliation match counter, so the self-clearing state could never clear — and the units the next top-up had really paid for were never invoiced. Production account `acdcdce9` sat in `not_invoiceable_pending_reconciliation` behind 380 negative differences that were all the same number, `-1361800`, equal to the overage its own row already recorded. Per the product owner, essentially every user tops up small amounts and spends to zero, so every burn-to-zero cycle mints one of these. A carried debt is settled by cash only: when a gift, a `REBATE`, or an evaluator-synthesized credit is what cleared the negative balance upstream, the consumption is not invoiceable anyway.

**Architecture:** RC88 is a backend roll-forward from RC87: no migration, no authorization change, no web change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order. Unlike RC85 through RC87, the shadow evaluation is run rather than skipped — this changes what the evaluator allocates — though RC88's own run showed the gate cannot currently exercise a projection change. The release env carries over from RC87 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-OVERAGE-CARRY-FORWARD.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc87 (`705beb4`) stay fixed; RC87 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC88 uses only `v0.1.0-rc88-signed`, `releaseName=0.1.0-rc88`, nine exact `:0.1.0-rc88` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc88-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- The shadow evaluation must reach verdict `ready`, and the execution record must state how many projection rounds it ran rather than quoting the verdict alone. The blast radius is established from production read-only, and post-deploy verification is what confirms the numbers.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc88-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC88 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC88 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC87 with `INVOICE_IMAGE_TAG=0.1.0-rc88`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] **Shadow evaluation.** Run `deploy/rehearsal/shadow-eval.sh` against a throwaway PostgreSQL restored from a signed production backup and require verdict `ready`. Record explicitly, in the execution record, how many projection rounds it actually ran: the RC88 first attempt returned `ready` after one round with a queue that was already empty, so its before and after snapshots were identical and no projection was exercised at all. A `ready` verdict from a run that did no work is not evidence for an evaluator change, and the same "one round, drained" shape appears in the RC78 and RC79 records — the gate has never exercised one. Filed as `docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md`; it does not block this release, whose blast radius is established from production instead.
- [ ] **Blast radius, established read-only from production before the release.** Exactly one account changes: `acdcdce9-c7f4-4cb4-9a02-ce527849a440`, whose expected balance moves from 814359980 to **812998180** units — exactly the balance its checkpoints report — so its difference goes to zero, its overage clears, and it leaves `not_invoiceable_pending_reconciliation` once two consecutive matches land. `40bd883d-26fa-4938-b8c8-0f51c8b88686` is deliberately untouched: its overdraw is not deducted upstream (its difference reads 0) and its only later pools are synthesized `UNKNOWN_POSITIVE` credits, which the cash-only rule excludes. No other account can change, because `carried` only becomes non-empty where the old code recorded an overage, and only these two did.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Record deployment evidence beside the release.
- [ ] Post-deploy, confirm on production that `acdcdce9` reaches `expected_service_units` 812998180 with difference 0 on its next evaluated checkpoint, and that `non_invoiceable_overage_units` clears. Do not touch its data to make this happen — the fix is retroactive because reprojection rebuilds allocations from facts, so if it does not happen on its own that is a finding, not something to repair by hand.

Production remains blocked until a 30-minute readiness watch binds RC88. Because RC87's own watch was broken by a heavy catch-up projection contending with the ingest path, watch the `balances` and `credits` streams' batch commits during this window too, not only readyz.

## Execution record (2026-09-04)

- Task 1: identity bump `6b8fc01` over `87134df`. The gate self-test refused it once, correctly: the mechanical rc87→rc88 rename left the superseded-document pointer at the old `2026-09-03-` date prefix while the new plan was filed under `2026-09-04-`. Aligned both and the self-test passed. All gates 0, including the four failure-evidence scripts — which must be run from `K:\发票\wt-XM-INV-SEC-RC49`'s **own** copies; invoking the AUTOLOGIN copies against that tree fails with "exact directory namespace drifted" because each script resolves paths relative to its own location.
- **The first draft was too broad, and production said so.** `0eb602d` narrowed it: only cash settles a carried debt. The reasoning and the two accounts' data are in `docs/handoffs/XM-INV-OVERAGE-CARRY-FORWARD.md`. The tag was rebuilt on the new head (RC88 had never been published, so rebuilding rather than burning an RC number follows the RC68 precedent) and the image gate re-ran into `exact2`.
- Task 1, second run at `0eb602d`: every gate 0 again, web 174 tests.
- Task 2: `release/0.1.0-rc88-exact2`, image gate 42, ordinary and strict verifiers 0 and 0.
- Task 3: staged, tag and evidence signatures verified. Fresh signed pre-deploy backup `invoice-20260904T065429Z`. Roll-forward PASS: 18 containers on rc88, healthz/readyz 200.
  - **Caught while reading the roll-forward script before running it:** the mechanically derived `rc88-rollforward.sh` still carried RC87's `SHA=705beb40…`, so running it would have redeployed RC87 under an RC88 banner. Corrected to `0eb602d…` first. The derivation renames `rcNN` strings but not embedded commit hashes; any future derived script must have its sha checked explicitly.
- Shadow evaluation: verdict `ready`, **one round against an already-empty queue, before and after snapshots identical, no projection exercised**. Recorded as vacuous rather than as evidence; filed as `docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md`. The blast radius was established read-only from production instead, and post-deploy verification confirmed it.

### Post-deploy verification

`acdcdce9` reconciled on its own within three minutes of the deploy, with no data touched by hand:

| | before | after |
| --- | --- | --- |
| `eligibility_status` | `not_invoiceable_pending_reconciliation` | `active` |
| checkpoint difference | −1361800, on 380 consecutive evaluations | 0, on all 12 post-deploy evaluations |
| `non_invoiceable_overage_units` | 1361800 | cleared |

The exact number predicted before the release — that account's checkpoint at 03:55:37Z re-evaluating from 814359980 to 812998180 — was not directly testable: `balance_checkpoint_evaluations` rows are immutable history, so the new projection evaluated newer checkpoints rather than rewriting that one. The equivalent claim is what held, and more strongly: the constant offset is gone from every checkpoint evaluated since.

`40bd883d` behaved as the cash-only rule intended, with one correction to the prediction worth recording. Its invoiceable amount did not move (its single ¥5 lot stays fully consumed) and its reconciliation did not move (4 post-deploy evaluations, all matched at difference 0) — which is exactly what the rule was narrowed to protect. But its `non_invoiceable_overage_units` did change, 300000 → 15172546, because its cash lot completed at 2026-09-01 14:10:49, *after* the 2026-08-31 17:34:34 overdraw. That cash now settles the oldest debt first and displaces a later, larger usage event into the overage. The plan's "no other account can change" was therefore right about the quantities that matter and wrong as an absolute: a diagnostic field on a second account moved. The new attribution is the better one — cash pays the oldest consumption, which is the order the source settles in — but the claim should have been scoped to invoiced amounts and reconciliation rather than stated flatly.

### Canary

30 minutes from 07:00:08Z: **readyz 200 on every probe** (`readyz_non200=0`), zero error lines, zero reconcile errors, 27 usage cycles and 27 credits cycles. Zero `active scan cycle` contention lines — the shape that broke RC87's own canary did not recur, because this release's reprojection had no multi-day backlog to replay.

One blemish, recorded rather than smoothed over: `RC88-WATCH-CREDITS` reported `sync_failures=1`. At 07:27:37Z one credits batch commit (sequence 12931) was rejected 409 with `deadlock detected (SQLSTATE 40P01)`. The agent retried and recovered — three cycles completed after it, the credits watermark is current, readiness never dropped. This is not RC82's permanent-failure loop, which never recovered; it is a single transient deadlock between the projection and ingest paths, the same contention family as `XM-INV-CATCHUP-BURST-BACKPRESSURE`.

Whether it predates RC88 cannot be answered cheaply: the api container restarted at deploy, so its log only reaches back to 06:55Z. What can be said is that it is not routine — RC85's and RC87's watches both reported `sync_failures=0`. Worth watching on the next release rather than acting on a single occurrence.
