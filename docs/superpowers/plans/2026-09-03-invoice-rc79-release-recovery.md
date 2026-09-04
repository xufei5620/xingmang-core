# RC79 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC79 shipped at signed tag
> `v0.1.0-rc79-signed` (`5cd525a`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc92-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, shadow-evaluate, and deploy RC79 — eligibility simplification slice 3 (XM-INV-ELIG-POLICY-START-ANCHOR: POLICY_ANCHOR accounts anchor at the global policy start with a derived opening balance that unwinds credits, cash recharges and usage inside the window; migration 0021 adds the `invoice.policy_anchor_start_reanchor` GUC; `invoice-eligibility-repair --kind=policy-start-reanchor`), the scan-cycle self-heal (XM-INV-SCAN-CYCLE-SUPERSEDE: a stale active scan cycle is superseded inside CommitSourceBatch instead of wedging the stream; migration 0022), the operator user-ledger view (XM-INV-CR0009-LEDGER-VIEW: `GET /api/v1/admin/accounts/ledger` and per-account detail, the 用户账本 tab, the narrowed 资格冻结 tab, tolerant freeze-reason labels), the 37-minute rescan activity window (XM-INV-AGENT-RESTART-GRACE follow-up), and the shadow-eval verdict fix (nil-means-none report contract, per-reason freeze deltas).

**Architecture:** RC79 is a migration release from RC78 (0021 and 0022 add columns/constraints only; no data rewrite). `deploy/roll-forward.sh` keeps the cutover order (migrate → idp → main → sources → restart api → restart ingest-proxy). Because this release changes the evaluator (bootstrap derivation) and the ingest commit path, Task 3 runs the shadow evaluation for real before roll-forward and takes a fresh signed pre-deploy backup. The source agent stays 0.3.1; its restart during the backup quiesce and the roll-forward now resumes the persisted schedule, and a cycle orphaned by the quiesce restart is superseded server-side after the activity window instead of blocking the stream.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0, age + pg_restore (shadow evaluation).

**Spec:** `docs/handoffs/XM-INV-ELIG-POLICY-START-ANCHOR.md`; `docs/handoffs/XM-INV-SCAN-CYCLE-SUPERSEDE.md`; `docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md`; `docs/handoffs/XM-INV-AGENT-RESTART-GRACE.md`; `docs/handoffs/XM-INV-SHADOW-EVAL.md`; `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md` (section 3(D), 3(E)); `docs/PRODUCTION-RUNBOOK.md` section 11.2

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc78 (`99377ea`) stay fixed; RC78 is the release in production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC79 uses only `v0.1.0-rc79-signed`, `releaseName=0.1.0-rc79`, nine exact `:0.1.0-rc79` references, `SourceAgentVersion=0.3.1`, and one new `release/0.1.0-rc79-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Evaluator-change discipline (2026-09-03): every evaluator branch returns a defined outcome; per-account isolation tests stay green; the shadow evaluation is mandatory and a `regressed` verdict (exit 3) blocks production without exception.
- Migration release: take a fresh pre-deploy backup; never reuse an older backup.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc79-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC79 exactN, run the image gate with `-SourceAgentVersion 0.3.1` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Shadow evaluation, rehearsal, and production

- [x] Sign exactly one strict-ready RC79 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc79`, `SOURCE_AGENT_VERSION=0.3.1`).
- [x] Shadow evaluation: on the host, from the staged RC79 source, run `deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rc79` against the latest signed backup (identity on tmpfs for the run only, shredded after). Require `migrations_applied` to list 0021 and 0022 and exit `0` (ready); exit `3` blocks production; exit `1`/`2` is a tooling defect — fix the tool, re-bump the identity, and rerun Tasks 1–3. Copy the report and summary beside the release evidence.
- [x] Take a fresh pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm migrations 0021 and 0022 applied, confirm the usage stream keeps committing after the restart (a superseded orphan cycle, if any, appears as a `source.scan_cycle.superseded` audit event rather than a 503 loop), and record deployment evidence beside the release.
- [x] Post-deploy: `invoice-eligibility-repair --kind=policy-start-reanchor` dry-run; expect every POLICY_ANCHOR account to report NoOp (zero in-window funding lots); anything else stops for owner review before any apply.

Production remains blocked until the credentialed human canary (the projection worker completing batches without projection errors for 30 minutes, readiness staying 200 through the restart, the 用户账本 tab loading real rows for an admin) binds RC79.

## Execution record (2026-09-03)

- Task 1: merged XM-INV-AGENT-RESTART-GRACE follow-up (`2400844`, 37-minute window), XM-INV-SHADOW-EVAL verdict fix (`d608d3b`), XM-INV-ELIG-POLICY-START-ANCHOR (`5335446`, incl. the window cash-payment derivation fix), XM-INV-CR0009-LEDGER-VIEW (`e8c672c`), XM-INV-SCAN-CYCLE-SUPERSEDE (`e01917b`, migrate_test.go conflict resolved by keeping both the 0021 and 0022 exclusions); identity bump `5cd525a`. Backend full suite green after one isolated postgresstore re-run (loopback pool timeout, no assertion failure), agents module tests green, web typecheck/141 tests/build green, gitleaks clean, gate self-test 0, shadow static test 0, four failure-evidence verifiers 0. Tag `v0.1.0-rc79-signed` -> `5cd525a`.
- Task 2: `release/0.1.0-rc79-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; nine local image IDs matched the manifest.
- Task 3: transfer verified on the host; staging loaded nine images, verified tag and evidence signatures, release env `INVOICE_IMAGE_TAG=0.1.0-rc79`, `SOURCE_AGENT_VERSION=0.3.1`. Shadow evaluation against backup `invoice-20260903T085355Z`: first run ready — migrations_applied 0020/0021/0022, one projection round drained the queue, 0 round errors, 0 failed accounts, freeze deltas all 0; artifacts copied to `evidence/rehearsal/`. Fresh backup `invoice-20260903T102528Z` (signing key on tmpfs, shredded). `deploy/roll-forward.sh 5cd525a...` ROLL FORWARD PASS 10:30Z with 18 containers on `0.1.0-rc79`, healthz 200, readyz 200; migrations 0021 and 0022 applied 10:29:14Z; the 0.3.1 usage agent restarted straight into incremental mode (persisted schedule), no orphaned cycle, no 503 loop. Deployment record `rc79-deploy-20260903T103105Z`.
- Post-deploy: `--kind=policy-start-reanchor` dry-run examined 4 POLICY_ANCHOR accounts: 40bd883d, 6706ea6a, 98cce4c8 NoOp (no in-window lots); acdcdce9 (Sub2API user 12, the test account) has one in-window WALLET_CASH lot (2000 minor, 2026-09-01T09:56Z, before its cutover 12:14Z) so re-anchoring would move cutover_at to the policy start and the opening balance from 1453705040 to 2994240 units — apply awaits the owner's confirmation.
- Canary: 30 minutes from 10:30Z, readyz 200 throughout, projection errors 0, reconcile errors 0, 27 usage cycles.
- Read-only audit after deploy (defined-outcome rule): bootstrap, derivation, observeEligibilityFact, reproject, both repairs, CommitSourceBatch/supersede and the 0020-0022 CHECKs are clean; the structural gap is that a single failed projection job still turns readiness 503 with no retry grading — XM-INV-PROJECTION-FAILURE-GRADING is in progress for RC80.
