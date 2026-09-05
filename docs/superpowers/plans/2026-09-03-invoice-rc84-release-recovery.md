# RC84 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC84 shipped at signed tag
> `v0.1.0-rc84-signed` (`adc67fb`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc95-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC84 — XM-INV-CONSOLE-ASSERT-ADMIN-ROLE: the console-assertion exchange checks the assertion's `roles` claim against the console's own configured administrator role (`CONSOLE_ASSERTION_ADMIN_ROLE`, defaulting to `OIDC_ADMIN_ROLE`) instead of this deployment's Keycloak realm role, and the principal it builds carries the invoice `AdminPolicy.Role` so the session it issues satisfies every admin route. The third CR-0006 canary (2026-09-03 16:38Z, on RC83) rejected every exchange with `roles does not include the configured administrator role`: the console signs a staff account's own roles verbatim (`admin`, `credential-admin`, `staff`) while the invoice side required `invoice-admin`.

**Architecture:** RC84 is a code-and-config roll-forward from RC83: backend only, one new optional environment variable, no migration, no evaluator or ingest change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The shadow evaluation is not required and is recorded as skipped. The RC84 release env adds `CONSOLE_ASSERTION_ADMIN_ROLE=admin` on top of the RC83 env; `OIDC_ADMIN_ROLE` stays `invoice-admin` and the transitional OIDC admin login keeps working throughout.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CONSOLE-ASSERT-ADMIN-ROLE.md`; `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`; `docs/handoffs/XM-INV-ASSERT-ORIGIN.md`; xingmang-platform `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc83 (`702990f`) stay fixed; RC83 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC84 uses only `v0.1.0-rc84-signed`, `releaseName=0.1.0-rc84`, nine exact `:0.1.0-rc84` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc84-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- The new environment variable is optional in compose (`:-` default), so no existing `.env.production` becomes invalid and `scripts/verify.ps1`'s required-variable fixture is unchanged.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc84-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC84 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC84 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env with `INVOICE_IMAGE_TAG=0.1.0-rc84`, `SOURCE_AGENT_VERSION=0.3.2`, `CONSOLE_ASSERTION_ADMIN_ROLE=admin`, and the console-assertion variables carried over from the RC83 env).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection, or migration change); record the skip.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>`, require readyz 200 in the verify step, confirm the api starts with `CONSOLE_ASSERTION_ADMIN_ROLE=admin`, and record deployment evidence beside the release.
- [ ] Assertion canary: open the console's Sub2API 支付与财务 → 开票 tab and require the embedded admin console to render signed in. Evidence: invoice `audit_events` rows with action `auth.console_assertion.exchanged` (three canaries so far produced only `auth.console_assertion.rejected`), and the transitional OIDC admin login still working.

Production remains blocked until the canary above and a 30-minute readiness watch bind RC84.

## Execution record (2026-09-03)

- Task 1: XM-INV-CONSOLE-ASSERT-ADMIN-ROLE (`44ca0e4`); identity bump `adc67fb`. Backend full suite green, agents module tests green, web typecheck/155 tests/build green, gitleaks 0, gate self-test 0, shadow static test 0. `scripts/verify.ps1` was also run on its own before the bump (exit 0) because this release changes `docker-compose.prod.yml`; the new variable is optional (`:-` default), so the required-variable fixture needed no change.
- Task 2: `release/0.1.0-rc84-exact1` first try: image gate 42, ordinary and strict verifiers 0, `SHA256SUMS.sig` verified; nine local image IDs matched the manifest.
- Task 3: the first transfer attempt failed at `scp` because the transfer script still listed a shadow-evaluation script this release does not use; the entry was removed and the transfer re-run (evidence signature, image IDs and host checksums all verified). Staging loaded nine images, verified tag and evidence signatures, release env `INVOICE_IMAGE_TAG=0.1.0-rc84`, `SOURCE_AGENT_VERSION=0.3.2`, `CONSOLE_ASSERTION_ADMIN_ROLE=admin`, `OIDC_ADMIN_ROLE` unchanged at `invoice-admin`. Shadow evaluation skipped by rule. Fresh signed pre-deploy backup `invoice-20260903T173436Z` (signing key on tmpfs, shredded). Roll-forward PASS 17:38Z–17:39Z: 18 containers on rc84, healthz/readyz 200; the running api container was inspected and confirmed to carry `CONSOLE_ASSERTION_ADMIN_ROLE=admin`. Deployment record `rc84-deploy-20260903T174057Z`.
- CR-0006 phase 2 step 4 (`XM-INV-IDENTITY-MIGRATE`) applied 17:40Z, in the same window and before any successful console login, as `docs/handoffs/XM-INV-IDENTITY-MIGRATE.md` section 4 requires: `invoice_users` `99ed401b-e78a-4883-b9bf-f4cb4ba1cf17` moved from `https://auth.solov.cc/realms/solov` / `cd680af8-925b-4060-ab8b-17e694935b3c` to `https://console.solov.cc` / `61c647ee-a88f-4069-b36c-646b5b5a8587`; one live session invalidated; 21 audit rows stay with the row; a re-run reports `ALREADY MIGRATED`. The tool refuses to run once a row exists at the target pair, so this had to precede the first successful exchange. Consequence recorded for the operator: a Keycloak OIDC invoice login would now mint an orphan identity, so the console entry point is the only one to use until step 5 turns `OIDC_ADMIN_LOGIN_ENABLED` off.
- Canary: the 30-minute readiness watch from 17:39Z finished clean -- 30 of 30 probes returned 200, zero error lines, zero reconcile errors, 27 usage cycles and 27 credits cycles complete with no sync failures. The product owner's assertion canary is the remaining step, and its evidence is the first `auth.console_assertion.exchanged` row in invoice `audit_events` (all three previous canaries produced only `auth.console_assertion.rejected`).
