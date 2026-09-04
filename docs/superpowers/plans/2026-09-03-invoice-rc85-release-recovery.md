# RC85 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC85 shipped at signed tag
> `v0.1.0-rc85-signed` (`f520976`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-04-invoice-rc90-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC85 — the work accumulated on the release line since RC84: XM-INV-PROJECTION-HEALTH-UI (the admin 来源同步状态 screen renders the eligibility-projection queue counters the source-health endpoint has returned since RC80, with an honest empty state when the block is absent), the runtime-role hardening that revokes `UPDATE` on `console_assertion_nonces`, the RC83/RC84 execution records, and the per-worktree integration-test database convention in `docs/CONFIGURATION.md`.

**Architecture:** RC85 is a web-and-config roll-forward from RC84: no backend behaviour change, no migration, no evaluator change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The shadow evaluation is not required and is recorded as skipped. The release env is carried over from RC84 unchanged, including `CONSOLE_ASSERTION_ADMIN_ROLE=admin`. The `harden-runtime-role.sql` change is inert until the `permissions` job is replayed, which sits behind the compose `tools` profile and is therefore a deliberate post-deploy step, not part of roll-forward.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-PROJECTION-HEALTH-UI.md`; `docs/PRODUCTION-RUNBOOK.md` section 4.1 (the permissions replay and its privilege check)

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc84 (`adc67fb`) stay fixed; RC84 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC85 uses only `v0.1.0-rc85-signed`, `releaseName=0.1.0-rc85`, nine exact `:0.1.0-rc85` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc85-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- The RC84 console-assertion canary is still outstanding. RC85 does not touch the exchange path (`production_auth.go` and `console_assertion.go` are byte-identical to RC84), so it neither blocks nor complicates that canary.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc85-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC85 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC85 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC84 with `INVOICE_IMAGE_TAG=0.1.0-rc85`, `SOURCE_AGENT_VERSION=0.3.2`, `CONSOLE_ASSERTION_ADMIN_ROLE=admin`).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection, or migration change); record the skip.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step; record deployment evidence beside the release.
- [ ] Post-deploy, replay the runtime-role policy: `docker compose --env-file <release env> -f deploy/docker-compose.prod.yml run --rm permissions`, then require `has_table_privilege('invoice_app','console_assertion_nonces', ...)` to report `t|t|f|f` for INSERT/DELETE/UPDATE/TRUNCATE. Confirm readyz stays 200 afterwards.
- [ ] Confirm the admin 来源同步状态 screen shows the 资格投影队列 card. The queue is normally empty in production, so the expected reading is zeros with 队列已排空 and a green 无死信 badge — not the empty state, which would mean the server stopped reporting the block.

Production remains blocked until a 30-minute readiness watch binds RC85. The outstanding RC84 console-assertion canary carries over unchanged.

## Execution record (2026-09-03)

- Task 1: identity bump `f520976` over the four commits that had accumulated since RC84 (`ef7451a` projection-health UI, `4f2ae5f` runtime-role hardening, `ed059bb` test-database convention, `9c28920`/`0984e58` execution records). The gate self-test refused the bump twice before it was committed, both times correctly: `docs/PRODUCTION-RUNBOOK.md` had gained a one-shot Compose command without `--pull never`, and then a prose line carrying a bare bring-up token that the same guard requires to be written with `--no-build`. Both were rewritten and the self-test passed. Backend full suite green, agents module tests green, web typecheck/160 tests/build green, gitleaks 0, shadow static test 0.
- Task 2: `release/0.1.0-rc85-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; nine local image IDs matched the manifest.
- Task 3: transfer verified on the host; staging loaded nine images, verified tag and evidence signatures, release env carried over from RC84 (`INVOICE_IMAGE_TAG=0.1.0-rc85`, `SOURCE_AGENT_VERSION=0.3.2`, `CONSOLE_ASSERTION_ENABLED=true`, `CONSOLE_ASSERTION_ADMIN_ROLE=admin`). Shadow evaluation skipped by rule. Fresh signed pre-deploy backup `invoice-20260903T224218Z` (signing key on tmpfs, shredded). Roll-forward PASS 22:45Z–22:46Z: 18 containers on rc85, healthz/readyz 200, agents reporting 0.3.2, every stream watermark under 6 minutes. Deployment record `rc85-deploy-20260903T224750Z`.
- Post-deploy: replayed the runtime-role policy with `docker compose … run --rm --pull never permissions`. `invoice_app`'s privileges on `console_assertion_nonces` moved from `t|t|t|f` to `t|t|f|f` (INSERT/DELETE/UPDATE/TRUNCATE), which is the intended end state — the store claims a nonce with `INSERT … ON CONFLICT DO NOTHING` and sweeps expired rows with `DELETE`, so neither rewriting a claimed nonce nor clearing every claim is ever a runtime operation. Readiness stayed 200 through the replay. The before/after reading is kept in the deployment record.
- Canary: 30 minutes from 22:46Z, readyz 200 on every probe, zero error lines, zero reconcile errors, 27 usage cycles and 27 credits cycles. The credits stream also completed one periodic `mode="reconcile"` cycle inside the window with no sync failure — an unprompted second confirmation of the RC83 agent fix, this time on the ordinary schedule rather than a triggered run.
- Outstanding and unchanged: the console-assertion canary from RC84. RC85 leaves `production_auth.go` and `console_assertion.go` byte-identical, so that canary applies to this release as it stands.
