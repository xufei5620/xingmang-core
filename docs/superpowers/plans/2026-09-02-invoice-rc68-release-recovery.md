# RC68 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, rehearse, canary, and deploy RC68 — the ledger anchored at the invoice policy start (XM-INV-POLICY-ANCHOR: migration 0016, POLICY_ANCHOR bootstrap, pre-policy backlog skipped at wake, one-time re-anchor of legacy accounts, EVENT_DEAD freeze), the platform username persisted on the session row (XM-INV-OBS-BUNDLE: migration 0017, silent-failure logging, zero-value sync display fix), the source-agent busy-cycle backoff (XM-INV-CYCLE-BACKOFF: 503 SOURCE_SCAN_CYCLE_BUSY instead of the 409 crash loop), and server-side platform scoping of every user-facing endpoint to the session's login platform (XM-INV-PLATFORM-SCOPE, CR-0003: cross-platform data is 404, a mixed-platform submission is rejected, the embed no longer depends on the `&platform=` URL parameter), and the embedded administrator mode for the platform console (XM-INV-ADMIN-EMBED, CR-0005: `/embed/admin/*` entries, per-platform admin scoping, popup OIDC/step-up, `/admin` framed only by `https://console.solov.cc`, height sync to the console).

**Operator steps carried by RC68 (not part of the image):** apply the updated `deploy/nginx/invoice.solov.cc.conf.template` on the host (new `/embed/admin/*` entries and `frame-ancestors https://console.solov.cc` on `/admin`), then `curl -I https://invoice.solov.cc/admin` must show that CSP; run `scripts/verify-web-security-headers.ps1` in release-bound mode against the RC68 web image.

**Architecture:** RC68 is the first roll-forward since RC53 that carries schema migrations (0016 policy anchor, 0017 session display name). `deploy/roll-forward.sh` runs the migrate one-shot as step [0/6] before the compose projects move; the cutover order stays idp → main → sources → restart api → restart ingest-proxy. The exit-42 pending-canary protocol is unchanged; the Trivy cache volume stays pre-seeded from digest-verified OCI artifacts. The design's rehearsal gate applies: the RC68 images run against an isolated restore of production backup `invoice-20260901T091600Z` before any production step.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc67 (`e391dc2`) stay fixed; RC67 shipped to production, and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC68 uses only `v0.1.0-rc68-signed`, `releaseName=0.1.0-rc68`, nine exact `:0.1.0-rc68` references, and one new `release/0.1.0-rc68-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited.
- Migrations 0016 and 0017 are forward-only; the restore rehearsal must show them applying cleanly on the restored 0015 schema and a second migrate run being a no-op.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts, build/vet, full unit suite, integration suite against a disposable PostgreSQL 18 (both migrations applied by the harness), web typecheck/tests, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc68-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block, bind worktree/tag/HEAD, select the first unused RC68 exactN, run the image gate and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions.

### Task 3: Rehearsal on an isolated restore

- [ ] Transfer the nine RC68 images, then run `deploy/backup/restore-drill.sh` against backup `invoice-20260901T091600Z` with `INVOICE_IMAGE_TAG=0.1.0-rc68` (offline age identity copied into `/dev/shm` for the drill only and shredded afterwards); require the drill to pass, migrations 0016/0017 to apply, and a second migrate run to be a no-op.
- [ ] On the restored database, wake sub2api user 34 (account `40bd883d…`) and record: anchor checkpoint chosen, cutover balance, released vs `PRE_POLICY_SKIPPED` counts, projection wall time, lot 3347 `consumed_cash_minor`, and the legacy lots' display state; compare against the manual expectation before signing off.

### Task 4: Production

- [ ] Sign exactly one strict-ready RC68 directory, take a fresh pre-deploy backup, unpack the release beside the images, and run `bash deploy/roll-forward.sh <sha>` (migrate one-shot, idp → main → sources, restart api, restart ingest-proxy, 18 containers on the tag, healthz/readyz 200); record deployment evidence beside the release.

Production remains blocked until the rehearsal record is reviewed and the credentialed human canary (real platform login showing the platform username and the anchored consumption for a legacy account) binds RC68.
