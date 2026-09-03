# RC80 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, shadow-evaluate, and deploy RC80 — XM-INV-PROJECTION-FAILURE-GRADING: per-account eligibility projection failures are retried with exponential backoff (30 s doubling, capped at 30 minutes) and graded `dead` only after 8 consecutive failures; readiness turns not-ready only on a dead job or an actionable backlog older than 15 minutes, never on a retrying account; `eligibility_projection_jobs` gains `attempts`/`last_error` and the `dead` status (migration 0023); dead rows are never revived by fact upserts; `invoice-eligibility-repair --kind=projection-requeue-dead` requeues them after a fix; the admin source-health response carries `eligibility_projection` (retrying/dead) counters. This closes the structural gap found by the RC79 read-only audit: a single account's transient projection error could make the whole API not ready (the RC75 incident mechanism).

**Architecture:** RC80 is a migration release from RC79 (0023 adds two columns and widens a status CHECK; no data rewrite). `deploy/roll-forward.sh` keeps the cutover order (migrate → idp → main → sources → restart api → restart ingest-proxy). The projection worker's per-account isolation is unchanged; only what happens after a per-account error changes. Because this release changes the projection worker and readiness, Task 3 runs the shadow evaluation for real before roll-forward and takes a fresh signed pre-deploy backup.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0, age + pg_restore (shadow evaluation).

**Spec:** `docs/handoffs/XM-INV-PROJECTION-FAILURE-GRADING.md`; `docs/ELIGIBILITY-OPERATIONS.md` (failure grading section); `docs/PRODUCTION-RUNBOOK.md` sections 9 and 11.2

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc79 (`5cd525a`) stay fixed; RC79 is the release in production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC80 uses only `v0.1.0-rc80-signed`, `releaseName=0.1.0-rc80`, nine exact `:0.1.0-rc80` references, `SourceAgentVersion=0.3.1`, and one new `release/0.1.0-rc80-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Evaluator-change discipline (2026-09-03): every evaluator branch returns a defined outcome; per-account isolation tests stay green; the shadow evaluation is mandatory and a `regressed` verdict (exit 3) blocks production without exception.
- Migration release: take a fresh pre-deploy backup; never reuse an older backup.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc80-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC80 exactN, run the image gate with `-SourceAgentVersion 0.3.1` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Shadow evaluation, rehearsal, and production

- [ ] Sign exactly one strict-ready RC80 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc80`, `SOURCE_AGENT_VERSION=0.3.1`).
- [ ] Shadow evaluation: on the host, from the staged RC80 source, run `deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rc80` against the latest signed backup (identity on tmpfs for the run only, shredded after). Require `migrations_applied` to list 0023 and exit `0` (ready); exit `3` blocks production; exit `1`/`2` is a tooling defect — fix the tool, re-bump the identity, and rerun Tasks 1–3. Copy the report and summary beside the release evidence.
- [ ] Take a fresh pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm migration 0023 applied (`attempts`/`last_error` columns, status CHECK includes `dead`), confirm the admin source-health response carries `eligibility_projection`, and record deployment evidence beside the release.
- [ ] Post-deploy: `invoice-eligibility-repair --kind=projection-requeue-dead` dry-run; expect zero dead jobs.

Production remains blocked until the credentialed human canary (the projection worker completing batches without projection errors for 30 minutes, readiness staying 200 through the restart) binds RC80.
