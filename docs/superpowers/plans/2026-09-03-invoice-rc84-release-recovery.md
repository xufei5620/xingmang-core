# RC84 Release Implementation Plan

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
