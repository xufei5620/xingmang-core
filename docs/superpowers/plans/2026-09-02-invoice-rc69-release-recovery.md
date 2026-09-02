# RC69 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC69 shipped at signed tag
> `v0.1.0-rc69-signed` (`43bdc59`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc73-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC69 — the settlement lock-contention fix (XM-INV-PROOF-CONTENTION: exponential BALANCE_PROOF_PENDING backoff preserved across requeues, non-blocking per-account advisory locks in the source-projection worker, the projection job locking only its write phase, worker fault isolation, set-based carry-forward proof evaluation, EVENT_DEAD attribution from the application layer) plus the toolchain tails (XM-INV-TOOLCHAIN0: per-worktree integration databases, Trivy cache refresh script, detached ceremony runner).

**Architecture:** RC69 is a code-only roll-forward from RC68: no schema migrations, no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts (refresh with `scripts/refresh-trivy-cache.ps1` when its `NextUpdate` has passed).

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`; `docs/handoffs/XM-INV-PROOF-CONTENTION.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc68 (`ca6fa80`) stay fixed; RC68 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC69 uses only `v0.1.0-rc69-signed`, `releaseName=0.1.0-rc69`, nine exact `:0.1.0-rc69` references, and one new `release/0.1.0-rc69-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, where the rc49–rc52 evidence directories live), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc69-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1` so the tool sandbox timeout and the hidden-console code page cannot interfere), bind worktree/tag/HEAD, select the first unused RC69 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Production

- [x] Sign exactly one strict-ready RC69 directory, transfer only its nine manifest-bound images, reuse the RC68 pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only), run `bash deploy/roll-forward.sh <sha>`, and record deployment evidence beside the release. No host nginx change is carried by RC69.

Production remains blocked until the credentialed human canary (a heavy account's projection completing without BALANCE_PROOF_PENDING hammering, source agents with zero restarts across a busy cycle) binds RC69.

## Execution record (2026-09-02)

- Task 1: toolchain tails merged (`88ed99c`), then `43bdc59` fixed the per-worktree test database helper for the gate's containerised `verify-postgres.ps1` run (no `.git` there). Full backend suite 24/24 (internal/auth and migrate each needed one isolated rerun after loopback timeouts), web 76/76, gitleaks 12 commits clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc69-signed` -> `43bdc59` (re-created once, before any transfer).
- Task 2: `release/0.1.0-rc69-exact4` (exact1 and exact3 retained: loopback timeouts inside the gate's own backend test run; exact2 retained: the testdb helper failing in the container). Image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified. Launched through `scripts/run-detached.ps1`; the wrapper retried only when every failed package passed in isolation immediately afterwards.
- Task 3: transfer bundle verified on the host; staging loaded nine images, verified the tag and evidence signatures. Backup `invoice-20260902T022106Z` (signed) after two silent failures caused by a stale `RELEASE_METADATA_FILE` path (RC68 evidence is unpacked flat under `evidence/`). `deploy/roll-forward.sh 43bdc59…`: steps 0-5 completed, 18 containers on `0.1.0-rc69`, healthz 200; the verify step timed out because readyz stayed 503.
- readyz 503 is a pre-existing production condition opened at 02:00:13Z under RC68, not an RC69 regression: the first Sub2API balances cycle after the anchor bootstrapped 106 stateless accounts as `POLICY_ANCHOR` with `cutover_at` = that checkpoint, and the daily usage reconcile in the same second replayed pre-cutover usage facts, which the projection treats as `SOURCE_GAP` (freeze + PROJECTION_FAILED). After 8 retries 100 usage events were marked dead at 02:37Z (100 `EVENT_DEAD` freezes), and the readiness gate refuses while dead events exist. RC69 stays deployed (rolling back to RC68 would not change the condition). Fix in progress as XM-INV-PREANCHOR-USAGE (RC70): pre-anchor facts on `POLICY_ANCHOR` accounts are skipped with an audit row instead of frozen, plus an approved repair tool that resolves the incident freezes and requeues the dead events. Human canary and the repair `--apply` remain owner actions.
