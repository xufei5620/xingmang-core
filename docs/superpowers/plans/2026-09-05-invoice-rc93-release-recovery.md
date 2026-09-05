# RC93 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC93 shipped at signed tag
> `v0.1.0-rc93-signed` (`34c7775`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc96-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC93, which finishes the 2026-09-04 stall's repair at each of its three layers and lets the rehearsal exercise the account that matters most:

- **XM-INV-CATCHUP-BURST-BACKPRESSURE fix 1b.** The carry-forward proof — the slow phase on a contended account — now runs before the job takes its own row lock, in its own short transaction; a proof-pending job never holds the row, and a proceeding job holds it only for the write phase. A window raised meanwhile is clamped and requeued rather than dropped. Both projection-job upserts leave a live processing row's status and lease alone and merge only its window.
- **Fix 2.** Readiness no longer flips to 503 for an ingest event that is merely waiting fifteen seconds on an account's write-phase lock: such an event is tolerated for a ten-minute grace (shorter than the fifteen-minute watermark staleness bound, so a genuinely stuck stream still fails), then counted again.
- **XM-INV-SHADOW-EVAL-VACUOUS follow-ups.** `--reproject-all` retargets a captured non-dead job to the account's own boundary, so a continuously-consuming account is exercised on a copy instead of returning proof-pending; the report names any account still pending, and why.

**Architecture:** RC93 is a backend roll-forward from RC92 with **no migration**. One readiness query and one projection-job function change; the rest is rehearsal tooling, tests and documents. No evaluator, projection or allocation logic changes. **The shadow evaluation is run anyway, with `--reproject-all`, because this release is what makes it complete** (see Task 3). Source agent stays 0.3.2. Release env carries over from RC92 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` (its 2026-09-05 sections) and `docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md`.

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc92 (`8358ddc`) stay fixed; RC92 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC93 uses only `v0.1.0-rc93-signed`, `releaseName=0.1.0-rc93`, nine exact `:0.1.0-rc93` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc93-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver is available but must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` runs while the full suite is running: they share the test database and collide (recorded 2026-09-05).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc93-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC93 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC93 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC92 with `INVOICE_IMAGE_TAG=0.1.0-rc93`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] **Shadow evaluation with `--reproject-all`.** Against the newest signed backup with the rc93 tools image. Require: every account not listed in `pending_accounts` projected (`projection_version` moved), `accounts_enqueued == accounts_projected + len(pending_accounts)`, verdict `ready`. **Expected this time: 8 of 8 and `pending_accounts` null**, because the whale's captured job is now retargeted; if it still lands in `pending_accounts`, its row's `last_error_code` says why, and the release proceeds only if that reason is one a frozen copy cannot satisfy. Record the report path.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch; additionally require `canceling statement due to lock timeout` and `stream already has an active scan cycle` lines in the api log to be 0, every balances cycle in the window published within the safety delay, and — new for fix 2 — count `ACCOUNT_LOCK_BUSY` requeues in the window and confirm none exceeded the ten-minute grace.

Production remains blocked until a 30-minute readiness watch binds RC93.

## Known and deliberately not in this release

- Fix 3 (bound the evidence pass by checkpoint count) waits for a differential rehearsal — single-pass vs chunked on two restores of the same backup, per-account quantities diffed — which RC93's own rehearsal makes possible for the first time with the whale included.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `34c7775`; every gate 0, web 185 tests, four failure-evidence scripts 0/0/0/0. The plan file keeps its true date and the gate self-test's superseded-document pointer moved with it in the same token pass, so no correction was needed this time. Tag `v0.1.0-rc93-signed` created and verified, peeling to `HEAD`; the derived roll-forward script's `SHA=` checked against the tag commit before anything ran.
- Task 2: `release/0.1.0-rc93-exact1`, first attempt: binding bound to `34c7775e…`, loopback preflight passed on attempt 1, image gate 42, ordinary verifier 0, strict transfer-ready verifier 0. The audit ran for real (`found 0 vulnerabilities`), waiver cleared, not used.
- Task 3: staged (nine images, tag and evidence signatures good); signed pre-deploy backup `invoice-20260905T010919Z`; roll-forward PASS, 18 containers on rc93, healthz/readyz 200, migrations a no-op (0025 remains the newest). Deployment record `rc93-deploy-20260905T011300Z`.

### Shadow evaluation with `--reproject-all` — the follow-ups close the gap

Report `rehearsals/20260905T010341Z-3004051/shadow-eval.json`, backup `invoice-20260904T235516Z`, rc93 tools image.

| field | RC92 | RC93 |
| --- | --- | --- |
| `accounts_enqueued` / `accounts_projected` | 7 / 7 | **8 / 8** |
| `projection_version` moved | 7 of 8 | **8 of 8** |
| `after_projection_health.proof_pending` | 1 | **0** |
| `pending_accounts` | (field did not exist) | **null** |
| whale `40bd883d` | 5105 → 5105, proof-pending | **5684 → 5685** |
| per-account `consumed_cash_minor` / overage | unchanged | unchanged |
| verdict | ready | ready, both implementations |

The whale's captured job was retargeted to its own boundary and replayed. Every quantity is again identical before and after, which is the correct answer for a release without an evaluator change — and now it is the correct answer for all eight accounts, including the one whose reprojection matters most. This is the rehearsal the gate was built to run.

### Post-deploy, first five minutes

api image `invoice-system-api:0.1.0-rc93`; projection queue drained; zero `lock timeout` / `active scan cycle` lines; zero events in `ACCOUNT_LOCK_BUSY`; balances cycles publishing with 0 s lag.

### Canary

30 minutes from 01:14:21Z: `readyz_non200=0`, zero error lines, zero reconcile errors, usage 27 cycles, credits 28 cycles with zero sync failures, `unclassified_request_failures=0`; the finalization counters — lock timeouts, "active scan cycle" deferrals, late balances cycles — **0, 0, 0**; and the fix-2 counter, events sitting in `ACCOUNT_LOCK_BUSY` at the end of the window, **0** (oldest age none). Deployment record `rc93-deploy-20260905T011300Z` holds the containers list, roll-forward log, shadow-eval report and log, transfer checksums and the watch log.
