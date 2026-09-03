# RC82 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC82 shipped at signed tag
> `v0.1.0-rc82-signed` (`65d2098`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc83-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC82 — XM-INV-ASSERT-ORIGIN (backend only): the console-assertion exchange accepts the invoice app's own public origin as the caller (the embedded console redeems with a same-origin fetch from https://invoice.solov.cc) in addition to the console issuer, closing the `origin_rejected` failure found in the second CR-0006 canary after RC81 made the handshake itself work.

**Architecture:** RC82 is a code-only roll-forward from RC81: no migration, no deploy-file change, no evaluator or ingest change. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The shadow evaluation is not required for this release (no evaluator, projection, or migration change) and is recorded as skipped; the latest signed backup may be reused if under two hours old, otherwise a fresh one is taken. Console-assertion flags stay enabled on both sides; the transitional OIDC admin login remains available throughout.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-ASSERT-ORIGIN.md`; `docs/handoffs/XM-INV-ASSERT-HANDSHAKE.md`; `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`; xingmang-platform `docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md` step 3

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc81 (`ced1bfb`) stay fixed; RC81 is the release in production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC82 uses only `v0.1.0-rc82-signed`, `releaseName=0.1.0-rc82`, nine exact `:0.1.0-rc82` references, `SourceAgentVersion=0.3.1`, and one new `release/0.1.0-rc82-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Evaluator-change discipline (2026-09-03) is unaffected: no evaluator branch changes in this release (backend auth handler only).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc82-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC82 exactN, run the image gate with `-SourceAgentVersion 0.3.1` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC82 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc82`, `SOURCE_AGENT_VERSION=0.3.1`, and the console-assertion variables carried over from the RC81 env).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection, or migration change); record the skip.
- [ ] Reuse the latest signed pre-deploy backup if it is under two hours old, otherwise take a fresh one (signing key on tmpfs for the run only, shredded after); run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm the api starts with `CONSOLE_ASSERTION_ENABLED=true` and the corrected keyring, and record deployment evidence beside the release.
- [ ] Canary (product owner, real browser): console login (password + TOTP) → Sub2API "支付与财务→开票" renders the signed-in admin console without the login card; repeat for NewAPI. Evidence: invoice `audit_events` must show a console-assertion acceptance (no `origin_rejected`) and the platform `audit.audit_event` `staff.console_assertion.issue` rows and invoice `audit_events` console-assertion redemption rows for the same minute.

Production remains blocked until the credentialed human canary (projection worker completing batches without errors for 30 minutes, readiness 200 through the restart, and the assertion login canary above) binds RC82.

## Execution record (2026-09-03)

- Task 1: merged XM-INV-ASSERT-ORIGIN (`3cf5f71`); identity bump `65d2098`. Backend full suite green, agents module tests green, web typecheck/tests/build green, gitleaks 0, gate self-test 0, shadow static test 0.
- Task 2: `release/0.1.0-rc82-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; nine local image IDs matched the manifest.
- Task 3: transfer verified on the host; staging loaded nine images, verified tag and evidence signatures, release env `INVOICE_IMAGE_TAG=0.1.0-rc82`, `SOURCE_AGENT_VERSION=0.3.1`, console-assertion variables carried over. Shadow evaluation skipped by rule. Fresh signed pre-deploy backup `invoice-20260903T151811Z` (signing key on tmpfs, shredded). Roll-forward started 15:21Z: migrate no-op, idp, main, sources, api and ingest-proxy restarted; all 18 containers on rc82, healthz 200. The verify step FAILED at 15:33Z: `readyz not 200 within 600s` (roll-forward exit non-zero, no deployment record written by the script, 30-minute watch aborted).
- Incident (not caused by RC82): the Sub2API credits stream stopped committing at 14:52Z, before the roll-forward. Its first 6-hour periodic reconcile under agent 0.3.1 (due 14:58Z from the persisted schedule) failed permanently with `pending batch cursor transition is invalid`: the economics scanner stamps `reconcile_baseline_cursor` on every stream's completed reconcile page while `validateStoredFileCursorForStream` allows the field on usage only. Each attempt trips the circuit, the container restarts and retries; the credits watermark stays at 14:52Z and readiness reports 503 on stream freshness. The usage and payments streams kept committing under rc82 (usage cycles every ~65 s). Interim mitigation staged for the owner: `OWNER-ACTION-defer-credits-reconcile.sh` (stop the credits agent, back up `state.json`, rewrite `last_reconcile_at` to now, start the agent). Permanent fix: agent 0.3.2 in RC83 (XM-INV-AGENT-CREDITS-RECONCILE-FIX).
- Canary: pending readiness recovery; the assertion canary (third attempt) is deferred to RC83.
