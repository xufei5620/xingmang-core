# RC92 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC92, which carries two repairs to the release and ingest machinery and the records of how each was found:

- **XM-INV-SHADOW-EVAL-VACUOUS.** The shadow evaluation, the only pre-deploy check an evaluator change gets against real production data, had never projected a single account: it drains a queue that a healthy production leaves empty. `--reproject-all` now queues one job per account at its own `finalized_through`; the report carries `accounts_enqueued`/`accounts_projected` and per-account quantities; a run that was asked to reproject everything and projected nothing is `not_ready`, in both the Go and the bash verdict.
- **XM-INV-CATCHUP-BURST-BACKPRESSURE, fix 1.** `finalizeSourceAccountsTx` runs inside the cycle-publication transaction and its enqueue waited, under `lock_timeout=5s`, on a job row a running projection holds for its whole transaction. On 2026-09-04 that kept the balances stream from publishing for 29 minutes and readiness at 503. It now locks only the job rows it can take without waiting (`FOR UPDATE SKIP LOCKED`) and leaves a held account for the next pass.

**Architecture:** RC92 is a backend roll-forward from RC91 with **no migration**. One statement changes in the finalization path; the rest is the rehearsal tool, its scripts, tests and documents. No evaluator, projection or allocation logic changes, so the shadow evaluation is not required by rule — **but this release runs it anyway, with `--reproject-all`, as the explicit acceptance of the shadow-eval fix itself** (see Task 3). Source agent stays 0.3.2. Release env carries over from RC91 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** the two handoffs, `docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md` and `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` (its 2026-09-05 revision).

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc91 (`eb467a6`) stay fixed; RC91 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC92 uses only `v0.1.0-rc92-signed`, `releaseName=0.1.0-rc92`, nine exact `:0.1.0-rc92` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc92-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver is available but must not be used if the audit can reach the service. Record which happened.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc92-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC92 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC92 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC91 with `INVOICE_IMAGE_TAG=0.1.0-rc92`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] **Shadow evaluation with `--reproject-all` — the acceptance of XM-INV-SHADOW-EVAL-VACUOUS.** Against the newest signed backup with the rc92 tools image. Require `accounts_enqueued` == `accounts_projected` == the row count of `source_account_eligibility_state` (8 at the time of writing), `before`/`after` no longer byte-identical on `projection_version`, and verdict `ready`. If `accounts_projected` is 0 the verdict must be `not_ready` with exit 3 — that outcome is the gate working and the slice failing, and it blocks the tag until understood. Record the report path.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch; additionally count `canceling statement due to lock timeout` and `stream already has an active scan cycle` lines in the api log over the window and require both to be 0, and confirm every balances cycle in the window published within the safety delay.

Production remains blocked until a 30-minute readiness watch binds RC92.

## Known and deliberately not in this release

- Fix 1b (take the job row lock after the carry-forward proof phase), fix 2 (a stuck checkpoint event flipping readiness) and fix 3 (bound the evidence pass by checkpoint count) from the catch-up handoff's revised ranking. The observe-path enqueue deliberately still waits.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.
