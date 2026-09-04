# RC88 Release Implementation Plan

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
