# RC83 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC83 shipped at signed tag
> `v0.1.0-rc83-signed` (`702990f`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc86-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC83 — XM-INV-AGENT-CREDITS-RECONCILE-FIX (source agent 0.3.2): the economics scanner stamps the rolling reconcile-window baseline (`reconcile_baseline_cursor`) only on the usage stream. Agent 0.3.1 stamped it on every stream while the stored-cursor validator allows it on usage only, so the Sub2API credits stream's first 6-hour periodic reconcile under 0.3.1 (2026-09-03 14:58Z) failed permanently ("pending batch cursor transition is invalid"), the credits watermark froze at 14:52Z and readiness fell to 503 during the RC82 roll-forward verify step.

**Architecture:** RC83 is a code-only roll-forward from RC82 (agents module and its handoff only; no migration, no deploy-file change, no evaluator or ingest change). `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The source-agent image reports 0.3.2 and the release env carries `SOURCE_AGENT_VERSION=0.3.2`. The shadow evaluation is not required (no evaluator, projection, or migration change) and is recorded as skipped; the RC82 pre-deploy backup (`invoice-20260903T151811Z`) is older than two hours by the time RC83 rolls forward, so a fresh signed backup is taken. Console-assertion flags stay enabled on both sides. The interim production mitigation (rewriting `last_reconcile_at` in the credits state file to defer the reconcile) is superseded by this release: after the roll-forward the next credits reconcile must complete.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-AGENT-CREDITS-RECONCILE-FIX.md`; `docs/handoffs/XM-INV-AGENT-RESTART-GRACE.md` (part B, rolling reconcile window)

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc82 (`65d2098`) stay fixed; RC82 is the release in production (roll-forward completed, verify step failed on readiness for the reason above), and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC83 uses only `v0.1.0-rc83-signed`, `releaseName=0.1.0-rc83`, nine exact `:0.1.0-rc83` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc83-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Evaluator-change discipline (2026-09-03) is unaffected: no evaluator branch changes in this release.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests (including the new credits reconcile regression tests), web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc83-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC83 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC83 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc83`, `SOURCE_AGENT_VERSION=0.3.2`, and the console-assertion variables carried over from the RC82 env).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection, or migration change); record the skip.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm every source agent logs `agent_version="0.3.2"`, and record deployment evidence beside the release.
- [ ] Post-deploy: confirm the Sub2API credits stream completes a `mode="reconcile"` cycle under 0.3.2 (triggered by the persisted schedule, or by restoring the pre-mitigation `last_reconcile_at` from the `state.json.bak-*` copy if the owner prefers not to wait) and that the stored credits cursor carries no `reconcile_baseline_cursor`.

Production remains blocked until the credentialed human canary (projection worker completing batches without errors for 30 minutes, readiness 200 through the restart, every stream watermark under 15 minutes old, and the credits reconcile above) binds RC83.

## Execution record (2026-09-03)

- Task 1: merged XM-INV-AGENT-CREDITS-RECONCILE-FIX (`7d6479a`, `a431f18`, `7cd9ed0`); identity bump `702990f`. Backend full suite green, agents module tests green, web typecheck/155 tests/build green, gitleaks 0, gate self-test 0, shadow static test 0. Review of the first fix caught a regression before it left the branch: clearing the baseline on every completed page would have erased the usage rolling reconcile window on the next incremental cycle, so the helper now preserves the carried baseline outside reconcile (`a431f18`).
- Task 2: `release/0.1.0-rc83-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; nine local image IDs matched the manifest; the strict verifier was re-run independently afterwards and also returned 0.
- Task 3: transfer verified on the host; staging loaded nine images, verified tag and evidence signatures, release env `INVOICE_IMAGE_TAG=0.1.0-rc83`, `SOURCE_AGENT_VERSION=0.3.2`, console-assertion variables carried over. Shadow evaluation skipped by rule. Fresh signed pre-deploy backup `invoice-20260903T162516Z` (signing key on tmpfs, shredded). Roll-forward PASS 16:29Z–16:30Z: 18 containers on rc83, healthz/readyz 200; all ten source agents log `agent_version="0.3.2"`; every stream watermark under 6 minutes. Deployment record `rc83-deploy-20260903T163203Z`.
- Interim mitigation superseded: the RC82 incident had been held off by deferring the credits stream's `last_reconcile_at` at 16:16Z (`state.json.bak-20260903T161601Z` kept beside the file). RC83 removes the defect that made deferring necessary; the deferred schedule still stands until the next reconcile falls due.
- Canary: 30 minutes from 16:30Z, readyz 200 on every probe, 0 error lines, 0 reconcile errors, 28 usage cycles and 28 credits cycles complete, 0 sync failures.
- Assertion canary (product owner, real browser, 16:38Z): the exchange now reaches claim verification and is rejected with `roles does not include the configured administrator role` — the console signs its own staff roles while the invoice side required the Keycloak realm role. Fixed in RC84 (XM-INV-CONSOLE-ASSERT-ADMIN-ROLE).
