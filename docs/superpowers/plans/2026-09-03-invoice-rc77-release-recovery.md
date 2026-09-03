# RC77 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, shadow-evaluate, and deploy RC77 — eligibility simplification slice 1 (XM-INV-ELIG-AUTO-RECONCILE: a negative or unreconciled balance difference downgrades the account to the self-clearing `not_invoiceable_pending_reconciliation` state instead of opening a manual freeze, auto-exiting after two consecutive matched evaluations; `USAGE_EXCEEDS_LEDGER` records the overage on the account row instead of freezing; migration 0020) plus the release-rehearsal shadow-evaluation tool (XM-INV-SHADOW-EVAL: `invoice-eligibility-shadow` in the tools image and `deploy/rehearsal/shadow-eval.sh`).

**Architecture:** RC77 is a migration release from RC76 (0020 adds columns and CHECK constraints to `source_account_eligibility_state`; no data rewrite). `deploy/roll-forward.sh` keeps the cutover order (migrate → idp → main → sources → restart api → restart ingest-proxy); the tools image gains one binary. Because this release changes the evaluator, Task 3 runs the shadow evaluation for real before roll-forward: the latest signed backup's database component is restored into an isolated throwaway PostgreSQL on the host and the RC77 tools image drains the projection queue there; only exit 0 (ready) allows production. A migration release never reuses an older backup: a fresh signed pre-deploy backup is mandatory.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0, age + pg_restore (shadow evaluation).

**Spec:** `docs/handoffs/XM-INV-ELIG-AUTO-RECONCILE.md`; `docs/handoffs/XM-INV-SHADOW-EVAL.md`; `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md` (sections 3(A), 3(B)); `docs/PRODUCTION-RUNBOOK.md` section 11.2

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc76 (`17945dc`) stay fixed; RC76 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC77 uses only `v0.1.0-rc77-signed`, `releaseName=0.1.0-rc77`, nine exact `:0.1.0-rc77` references, and one new `release/0.1.0-rc77-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- Evaluator-change discipline (2026-09-03): every evaluator branch returns a defined outcome; per-account isolation tests stay green; the shadow evaluation is mandatory for this release (the tool ships in it) and a `regressed` verdict (exit 3) blocks production without exception.
- Migration release: take a fresh pre-deploy backup; never reuse the RC76-era backup.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), web typecheck/tests, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc77-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC77 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Shadow evaluation, rehearsal, and production

- [ ] Sign exactly one strict-ready RC77 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc77`).
- [ ] Shadow evaluation (first real run of the tool): on the host, from the staged RC77 source, run `deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rc77` against the latest signed backup with `BACKUP_DIR`, `BACKUP_ALLOWED_SIGNERS_FILE`, and `AGE_IDENTITY_FILE` set as documented in runbook section 11.2. Exit `0` (ready) is required to continue; exit `3` (regressed) blocks production and returns the slice to development; exit `1`/`2` is a tooling defect — fix the tool, re-bump the identity, and rerun Tasks 1–3. Copy `rehearsals/<stamp>/shadow-eval.json` and `shadow-eval-summary.txt` beside the release evidence.
- [ ] Take a fresh pre-deploy backup (offline backup signing key mounted on tmpfs for the run only, shredded after; `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), then run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm migration 0020 applied (`source_account_eligibility_state` status CHECK includes `not_invoiceable_pending_reconciliation`; the pending-reconciliation and overage columns exist), and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (the projection worker completing batches without projection errors for 30 minutes, readiness staying 200, no account entering `frozen` for `UNKNOWN_NEGATIVE_BALANCE` or `USAGE_EXCEEDS_LEDGER` after the cutover) binds RC77. Existing open freezes are not resolved by this release; `--kind=queue-narrow` arrives with RC78.
