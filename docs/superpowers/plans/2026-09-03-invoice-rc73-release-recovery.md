# RC73 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC73 shipped at signed tag
> `v0.1.0-rc73-signed` (`7061d81`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-04-invoice-rc88-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC73 — the POLICY_ANCHOR balance-evidence fix (XM-INV-ANCHOR-BALANCE: a POLICY_ANCHOR account's own anchor is a trusted interval start for balance checkpoints and carry-forward proofs, so post-anchor evidence no longer opens one `SOURCE_GAP` freeze per checkpoint; `invoice-eligibility-repair --kind=balance-anchor` resolves the existing freezes), the CR-0007 freeze-queue operability delivery (XM-INV-FREEZE-QUEUE-UX: `external_user_id` on the admin freeze queue with an exact-match filter, embedded-mode inline resolve results and top-anchored toasts, distinguishable resolve-precondition error codes), and the release-tooling hardening (XM-INV-TRIVY-REFRESH-FIX, XM-INV-GATE-TMPDIR).

**Architecture:** RC73 is a code-only roll-forward from RC72: no schema migrations, no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; the Trivy cache is refreshed daily by the registered task and the gate now keeps its download TMPDIR on the cache volume.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` (section 2.7); `docs/handoffs/XM-INV-ANCHOR-BALANCE.md`; platform repo `docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md`; `docs/handoffs/XM-INV-FREEZE-QUEUE-UX.md`; `docs/handoffs/XM-INV-TRIVY-REFRESH-FIX.md`; `docs/handoffs/XM-INV-GATE-TMPDIR.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc72 (`cadf009`) stay fixed; RC72 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC73 uses only `v0.1.0-rc73-signed`, `releaseName=0.1.0-rc73`, nine exact `:0.1.0-rc73` references, and one new `release/0.1.0-rc73-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- The balance-anchor repair runs on production only in `--dry-run` first; `--apply` requires the owner's approval recorded in the platform acceptance log and the operator id written into every resolved freeze.

### Task 1: Source identity

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc73-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC73 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production and repair

- [x] Sign exactly one strict-ready RC73 directory, transfer only its nine manifest-bound images, reuse the latest pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release.
- [ ] Run `invoice-eligibility-repair --kind=balance-anchor` from the RC73 tools image in `--dry-run`; compare the per-account summary against the 2026-09-02 snapshot (66 freezes on 98cce4c8, 13 on 6706ea6a, likely grown since); then `--apply` with the approved operator id only after the owner's approval; confirm the accounts leave `frozen` unless another freeze reason remains, and that subsequent checkpoints evaluate without new `SOURCE_GAP` freezes.

Production remains blocked until the credentialed human canary (an anchored account's next balance checkpoint evaluating `matched`, the admin freeze queue showing the source user id and a refused unfreeze explaining its reason inside the embedded drawer) binds RC73.

## Execution record (2026-09-03)

- Task 1: XM-INV-ANCHOR-BALANCE (`80ae6e5`), XM-INV-FREEZE-QUEUE-UX (`220ba75`), XM-INV-TRIVY-REFRESH-FIX (`4c72520`), XM-INV-GATE-TMPDIR (`423f1be`) merged; identity bump `6eb8362` + `c467b30`. Backend full suite green, web typecheck/94 tests/build green, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag first created at `c467b30`.
- Task 2: `release/0.1.0-rc73-exact1` … `exact4` retained as failed evidence: the source-verification step's shared PostgreSQL sidecar (512 MiB tmpfs) filled up — its WAL alone measured ~320 MiB after the enlarged integration suite — and PostgreSQL aborted (SIGABRT, then "the database system is in recovery mode") while migration 0009 committed inside `internal/migrate`. Fix `7061d81` (`scripts/verify-postgres.ps1`: 2g tmpfs, `max_wal_size=256MB`, `min_wal_size=64MB`, `checkpoint_timeout=60s`); tag re-created once at `7061d81` before any transfer. `release/0.1.0-rc73-exact5`: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified.
- Task 3: transfer verified on the host; staging loaded nine images and verified tag and evidence signatures; fresh backup `invoice-20260902T213458Z` (RC72's was older than two hours; signing key on tmpfs, shredded after); `deploy/roll-forward.sh 7061d81…` ROLL FORWARD PASS with 18 containers on `0.1.0-rc73`, healthz 200, readyz 200. `CONSOLE_ASSERTION_ENABLED` stays unset. Deployment record `deployment-records/rc73-deploy-*`. Balance-anchor repair: dry-run next; apply only after the owner's approval.
