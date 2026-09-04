# RC87 Release Implementation Plan

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
