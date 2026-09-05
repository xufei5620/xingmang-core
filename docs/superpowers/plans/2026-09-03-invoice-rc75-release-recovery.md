# RC75 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC75 shipped at signed tag
> `v0.1.0-rc75-signed` (`37636ca`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc97-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC75 — the balance-evidence blip fix (XM-INV-BALANCE-BLIP: a usage event within one second of a checkpoint's `as_of` is reconciled by a boundary rule instead of synthesizing a credit; a mid-stream positive difference is deferred until the next checkpoint or proof confirms it exactly, otherwise recorded as `positive_blip_ignored`; `invoice-eligibility-repair --kind=balance-blip` reverses the 2026-09-02 whale incident) plus the release-tooling follow-up XM-INV-TRIVY-REFRESH-LOG.

**Architecture:** RC75 is a migration-carrying roll-forward from RC74: migration `0019_balance_blip_repair.sql` extends the two balance-evaluation status CHECK constraints and replaces the `source_credit_events` immutability trigger with a transaction-local, GUC-gated exception that only the repair tool uses for `UNKNOWN_POSITIVE` rows. No deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate → idp → main → sources → restart api → restart ingest-proxy); step 0 is NOT a no-op this time, so a fresh signed backup under two hours old is mandatory before the roll-forward. The exit-42 pending-canary protocol is unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` (section 2.8); `docs/handoffs/XM-INV-BALANCE-BLIP.md`; `docs/handoffs/XM-INV-TRIVY-REFRESH-LOG.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc74 (`e38d7c0`) stay fixed; RC74 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC75 uses only `v0.1.0-rc75-signed`, `releaseName=0.1.0-rc75`, nine exact `:0.1.0-rc75` references, and one new `release/0.1.0-rc75-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- The balance-blip repair and the pending balance-anchor repair run on production only in `--dry-run` first; `--apply` requires the owner's approval recorded in the platform acceptance log and the operator id written into every resolved freeze.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc75-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC75 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production and repair

- [x] Sign exactly one strict-ready RC75 directory, transfer only its nine manifest-bound images, stage, take a fresh pre-deploy backup (migration release: never reuse an older backup; offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, confirm migration 0019 applied (the evaluation CHECK constraints list `positive_blip_ignored`), require readyz 200 in the verify step, and record deployment evidence beside the release.
- [ ] Run `invoice-eligibility-repair --kind=balance-blip` from the RC75 tools image in `--dry-run` (expected: account 40bd883d, one `UNKNOWN_POSITIVE` credit of 3,667,080, nine `UNKNOWN_NEGATIVE_BALANCE` freezes, eight checkpoint evaluations reset); then, together with the still-pending `--kind=balance-anchor` apply (79 freezes on 98cce4c8/6706ea6a), `--apply` with the approved operator id only after the owner's approval; confirm subsequent checkpoints evaluate `matched` and no new freezes open.

Production remains blocked until the credentialed human canary (the whale's next balance checkpoint evaluating `matched` after the repair, and no `positive_classified_non_cash` synthesized from a lone checkpoint in the following day) binds RC75.

## Execution record (2026-09-03)

- Task 1: XM-INV-BALANCE-BLIP merged (`84e1f34`), XM-INV-TRIVY-REFRESH-LOG merged (`881bfcb`), identity bump (`37636ca`). Backend full suite green (twice, including the migrate exact-set tests with 0019), web typecheck/94 tests/build green, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc75-signed` -> `37636ca`.
- Task 2: `release/0.1.0-rc75-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified.
- Task 3: transfer verified on the host; staging loaded nine images and verified tag and evidence signatures; fresh backup `invoice-20260903T011358Z` (migration release; signing key on tmpfs, shredded after); `deploy/roll-forward.sh 37636ca…` ROLL FORWARD PASS with 18 containers on `0.1.0-rc75`, healthz 200, readyz 200; migration 0019 confirmed applied (both evaluation-status CHECK constraints list `positive_blip_ignored`). Deployment record `deployment-records/rc75-deploy-*`. Repairs: dry-runs next; apply only after the owner's approval.
