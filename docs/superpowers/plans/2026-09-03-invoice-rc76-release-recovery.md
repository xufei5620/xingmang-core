# RC76 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC76 shipped at signed tag
> `v0.1.0-rc76-signed` (`17945dc`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc77-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, rehearse, canary, and deploy RC76 — the RC75 regression hotfix (XM-INV-BLIP-SOFTFAIL: a blip confirmation whose post-synthesis rebuild does not reconcile softfails and rebaselines instead of erroring; at most three consecutive rebaselines before a single `SOURCE_GAP` freeze) and the readiness false-positive fix (XM-INV-READY-LEASE: live-lease processing rows and the 30-second reclaim grace are not "stuck").

**Architecture:** RC76 is a code-only roll-forward from RC75: no schema migrations (0019 stays), no deploy-file changes. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). New for evaluator-changing releases: a shadow evaluation against a restored copy of the latest production backup (`deploy/rehearsal/shadow-eval.sh`, slice XM-INV-SHADOW-EVAL) must run before Task 3 when the tool has landed; if it has not, the plan records why it was skipped and the acceptance line watches the projection worker log for 30 minutes after roll-forward instead.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-BLIP-SOFTFAIL.md`; `docs/handoffs/XM-INV-READY-LEASE.md`; `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` (sections 2.8–2.9)

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc75 (`37636ca`) stay fixed; RC75 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC76 uses only `v0.1.0-rc76-signed`, `releaseName=0.1.0-rc76`, nine exact `:0.1.0-rc76` references, and one new `release/0.1.0-rc76-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- Evaluator-change discipline (2026-09-03): every evaluator branch returns a defined outcome; per-account isolation tests stay green; shadow evaluation before roll-forward when available.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc76-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC76 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Shadow evaluation: if `deploy/rehearsal/shadow-eval.sh` exists on the transferred source, restore the latest signed backup into an isolated network on the host and run the RC76 tools image's projection once; require exit 0 (no new freeze categories, no projection errors) and attach the report beside the release; otherwise record the skip and the compensating 30-minute worker-log watch.
- [ ] Sign exactly one strict-ready RC76 directory, transfer only its nine manifest-bound images, stage, reuse the latest pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (the projection worker completing batches without `balance blip confirmation … did not reconcile` errors for 30 minutes, readiness staying 200 through two proof-pending attempts) binds RC76.
