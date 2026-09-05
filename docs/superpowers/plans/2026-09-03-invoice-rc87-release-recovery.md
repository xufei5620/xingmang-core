# RC87 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC87 shipped at signed tag
> `v0.1.0-rc87-signed` (`705beb4`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc98-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC87 — two production defects the product owner surfaced while working in the embedded admin console:

- **XM-INV-CATCHUP-RELEASE.** `completeEligibilityCatchupTx` released only accounts still marked `syncing`. An account that leaves `syncing` during its own catch-up — the POLICY_ANCHOR bootstrap parks one in `not_invoiceable_pending_reconciliation` the moment its triggering checkpoint reports a negative balance — kept `catchup_key_hmac`, which is what `finalizeSourceAccountsTx` excludes on. The account was then excluded from finalization permanently: no projection, no checkpoint evaluation, `finalized_through` frozen, the self-clearing pending state unable to reach its two matches, and the invoiceable amount stuck. Production had exactly one, showing ¥5.46 against ¥55.78 of real post-start usage.
- **XM-INV-EMBED-HEIGHT.** The embedded frame stopped growing with its content: the height hook measured and observed `documentElement` alone, whose box is pinned to the viewport by the embedded layout, so the reported height froze at whatever was measured before the data arrived.

**Architecture:** RC87 is a backend-and-web roll-forward from RC86: no migration, no evaluator-branch change (the catch-up fix touches the release path, not any eligibility decision), no authorization change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order. The shadow evaluation is not required and is recorded as skipped. The release env carries over from RC86 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-RELEASE.md`; `docs/handoffs/XM-INV-EMBED-HEIGHT.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc86 (`4c4a799`) stay fixed; RC86 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC87 uses only `v0.1.0-rc87-signed`, `releaseName=0.1.0-rc87`, nine exact `:0.1.0-rc87` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc87-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc87-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC87 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC87 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC86 with `INVOICE_IMAGE_TAG=0.1.0-rc87`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] Shadow evaluation: skipped by rule; record the skip.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Record deployment evidence beside the release.
- [ ] **Post-deploy repair, one account.** The fix repairs the release path; it does not retroactively clear a key already set. Clear `catchup_key_hmac` once for the stuck account (`acdcdce9-c7f4-4cb4-9a02-ce527849a440`, external user 12) and then require, within one finalization cycle: `finalized_through` advancing past 2026-09-01, a projection job appearing and completing, its balance checkpoints leaving `pending_finalization`, and the ledger's 起点后消耗 moving off ¥5.46 toward the ~¥55.78 its usage actually represents. If instead a real reconciliation difference surfaces, stop and report it rather than repairing further — that would be a genuine finding, not a leftover.
- [ ] Confirm the embedded console frame grows with its content: the 用户账本 table should render without the frame's own scrollbar.

Production remains blocked until a 30-minute readiness watch binds RC87.

## Execution record (2026-09-04)

- Task 1: identity bump `705beb4` over `ee5add9` (XM-INV-CATCHUP-RELEASE) and `0e553b5` (XM-INV-EMBED-HEIGHT). Every gate exit 0: backend build/vet, full unit suite, integration suite against the disposable PostgreSQL 18, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks. The self-test refused the bump once, correctly: the plan above was missing the verbatim phrase the guard requires about not manually parsing manifest decisions. Tag `v0.1.0-rc87-signed` created, verified, peeling to HEAD.
- Task 2: `release/0.1.0-rc87-exact1` first try: image gate 42, ordinary and strict verifiers 0 and 0.
- Task 3: transfer verified on the host; staging loaded nine images and verified the tag and evidence signatures; release env carried over from RC86 with `INVOICE_IMAGE_TAG=0.1.0-rc87` and `SOURCE_AGENT_VERSION=0.3.2`. Shadow evaluation skipped by rule. Fresh signed pre-deploy backup `invoice-20260904T033226Z`, signing key on tmpfs and shredded after. Roll-forward PASS 03:36Z: 18 containers on rc87, healthz/readyz 200. Deployment record `rc87-deploy-20260904T033632Z`; `console_assertion_nonces` still `t|t|f|f`.

### The canary did not pass, and the cause was the repair

The 30-minute watch recorded 17 non-200 readiness probes: every probe from 04:00:23Z to the watch's end at 04:06:24Z, with the api log showing the condition persisting to about 04:17Z. Zero error lines, zero reconcile errors, normal cycle counts throughout.

One batch on the sub2api `balances` stream was deferred every minute from 03:58:22Z to 04:14:03Z with "stream already has an active scan cycle", and two lock timeouts sit inside that window — the source-projection worker failing to mark a dependency wait on a balances event at 04:04:44Z, and a credits batch commit rejected at 04:14:55Z. A stream that cannot commit goes stale, and the readiness freshness gate answers 503.

The trigger is the post-deploy repair itself. Clearing `catchup_key_hmac` released an account that had been excluded from finalization since 2026-09-01 12:14Z, so its first projection had three days of backlog to replay in a single pass — the audit shows that replay landing at 04:16:42Z as one `eligibility.projection.rebuilt` with 378 `pending_reconciliation.entered` rows behind it. That transaction contends for exactly the rows the ingest path needs.

It resolved without intervention: the account's `finalized_through` reached 04:01:35Z, the projection job queue drained to zero, readiness returned to 200, and the balances watermark is current again.

This is a real standing risk rather than a one-off — any future release of a long-excluded account repeats it. Follow-up filed as `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md`. A clean 30-minute watch was re-run afterwards to bind RC87 properly.

### Post-deploy repair: what the account actually shows

The four indicators the plan asks for, before and after:

| indicator | before | after |
| --- | --- | --- |
| `finalized_through` | 2026-09-01 12:14:31, frozen | tracks real time (04:01:35Z and advancing) |
| projection job | none ever enqueued | enqueued, ran, queue drained to zero |
| checkpoint evaluation | stopped at 2026-09-01 12:14:31 | current |
| `consumed_cash_minor` | 546 | 6186 |

The third indicator was worded as "checkpoints leaving `pending_finalization`", which is not the signal it sounds like: that column stays `pending_finalization` after a checkpoint is evaluated, and the real evidence is rows in `balance_checkpoint_evaluations`. Read that way, the account is current.

The recovered numbers reconcile against the upstream balance independently. Six cash top-ups totalling ¥70.00 (¥20 on 09-01, then ¥10 each on 09-03 ×3 and 09-04 ×2), ¥61.86 consumed oldest-lot-first, ¥8.14 left. The checkpoint at 03:55:37Z put our expected balance at 814359980 units — ¥8.1436 at this source's 1e8 units per yuan — against 812998180 reported upstream. The gap is ¥0.0136.

That gap is why the account sits in `not_invoiceable_pending_reconciliation` with `pending_reconciliation_consecutive_matches` at 0 since 04:16:42Z, reason `UNKNOWN_NEGATIVE_BALANCE`. The state is self-clearing by design and needs consecutive matches to leave.

Worth flagging to the owner rather than treating as settled: the three-day backlog contained 378 negative-balance checkpoints. This account habitually runs its balance to zero and tops up ¥10 at a time, so it will keep re-entering the pending state, and will be non-invoiceable for much of any given window. That is the eligibility rules working as written, not a defect, but it is a product question about whether small-balance accounts should be handled differently.
