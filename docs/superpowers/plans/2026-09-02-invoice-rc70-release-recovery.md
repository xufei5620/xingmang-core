# RC70 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, canary, and deploy RC70 — the pre-anchor usage incident fix (XM-INV-PREANCHOR-USAGE: usage/credit facts dated at or before a `POLICY_ANCHOR` account's cutover are skipped with an audit row instead of freezing `SOURCE_GAP`; projection failures are logged before an event is marked failed or dead; the `invoice-eligibility-repair` tool in the tools image resolves the 2026-09-02 incident freezes and requeues its dead events) plus the owner-ruled removal of the administrator login entry from the user-facing web (XM-INV-HIDE-ADMIN-LOGIN, when merged before the tag).

**Architecture:** RC70 is a code-only roll-forward from RC69: no schema migrations, no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts (refresh with `scripts/refresh-trivy-cache.ps1` when its `NextUpdate` has passed). The roll-forward's readiness verify is expected to fail until the approved repair is applied: production readiness refuses while dead source events exist, and only the repair removes them.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` (section 2.6); `docs/handoffs/XM-INV-PREANCHOR-USAGE.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc69 (`43bdc59`) stay fixed; RC69 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC70 uses only `v0.1.0-rc70-signed`, `releaseName=0.1.0-rc70`, nine exact `:0.1.0-rc70` references, and one new `release/0.1.0-rc70-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- The repair tool runs on production only in `--dry-run` first; `--apply` requires the owner's approval recorded in the platform acceptance log (given 2026-09-02 in chat) and the operator id written into every resolved freeze.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc70-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC70 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Production and repair

- [ ] Sign exactly one strict-ready RC70 directory, transfer only its nine manifest-bound images, take a fresh pre-deploy backup (the RC69 backup is older than two hours; offline signing key on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, and record deployment evidence beside the release.
- [ ] Run `invoice-eligibility-repair` from the RC70 tools image in `--dry-run`; compare the summary against the incident (about 106 `SOURCE_GAP`, 100 `EVENT_DEAD`, 101–102 requeued events); then `--apply` with the approved operator id; confirm readyz returns 200, the requeued events are processed as `pre_anchor_skipped` audit rows, and the accounts are `active` again.

Production remains blocked until the credentialed human canary (a previously frozen account's projection completing, its usage after the anchor allocating normally, the user-facing web showing no administrator login entry) binds RC70.
