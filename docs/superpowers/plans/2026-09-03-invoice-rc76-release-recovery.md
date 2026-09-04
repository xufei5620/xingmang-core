# RC76 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC76 shipped at signed tag
> `v0.1.0-rc76-signed` (`17945dc`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-04-invoice-rc89-release-recovery.md`.

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

- [x] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet, full unit suite, integration suite against a disposable PostgreSQL 18, web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [x] Create `v0.1.0-rc76-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [x] In one block (run through `scripts/run-detached.ps1`), bind worktree/tag/HEAD, select the first unused RC76 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [x] Shadow evaluation: if `deploy/rehearsal/shadow-eval.sh` exists on the transferred source, restore the latest signed backup into an isolated network on the host and run the RC76 tools image's projection once; require exit 0 (no new freeze categories, no projection errors) and attach the report beside the release; otherwise record the skip and the compensating 30-minute worker-log watch.
- [x] Sign exactly one strict-ready RC76 directory, transfer only its nine manifest-bound images, stage, reuse the latest pre-deploy backup if it is under two hours old (otherwise take a new one with the offline backup signing key mounted on tmpfs for the run only, `RELEASE_METADATA_FILE` pointing at the flat `evidence/release-manifest.json` of the running release), run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, and record deployment evidence beside the release.

Production remains blocked until the credentialed human canary (the projection worker completing batches without `balance blip confirmation … did not reconcile` errors for 30 minutes, readiness staying 200 through two proof-pending attempts) binds RC76.

## Execution record (2026-09-03)

- Task 1: XM-INV-BLIP-SOFTFAIL merged (`e9ac4e2`), XM-INV-READY-LEASE merged (`578b6b2`), identity bump (`17945dc`). Backend full suite green, web typecheck/94 tests/build green, gitleaks clean, four failure-evidence verifiers 0, gate self-test 0. Tag `v0.1.0-rc76-signed` -> `17945dc`.
- Task 2: `release/0.1.0-rc76-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; all nine local image IDs matched the manifest.
- Task 3: shadow evaluation skipped as planned (`deploy/rehearsal/shadow-eval.sh` is not in this tag; it lands with RC77/RC78) and replaced by the 30-minute worker-log watch. Transfer verified on the host; staging loaded nine images and verified tag and evidence signatures; backup `invoice-20260903T050943Z` reused (81 minutes old, no migration); `deploy/roll-forward.sh 17945dc...` ROLL FORWARD PASS 06:30Z with 18 containers on `0.1.0-rc76`, healthz 200, readyz 200. Deployment record `rc76-deploy-20260903T063302Z`.
- Canary: 30 minutes from 06:32Z, projection errors 0, reconcile errors 0. readyz was 503 from 06:41Z to 07:02Z: the recreated Sub2API usage agent ran a reconcile rescan (cycle 06:31Z-06:56Z, 3,327 batches) during which the usage watermark could not advance past the 15-minute freshness rule; every Sub2API lot showed `source_unavailable` and submissions were refused with `SOURCE_NOT_READY` for that window. Not an RC76 defect; root cause (in-memory reconcile schedule plus cutover rewind in the agent, no readiness grace) is tracked as XM-INV-AGENT-RESTART-GRACE and ships with RC78. RC77 (`a0efc92`, signed and image-gated) is deliberately not deployed alone so production restarts only once more.
