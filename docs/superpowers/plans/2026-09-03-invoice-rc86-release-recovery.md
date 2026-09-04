# RC86 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC86 — three things: XM-INV-LEDGER-ACCOUNT-EMAIL (the operator 用户账本 and 资格冻结队列 lists, and the freeze detail, label each account with its verified email under the upstream numeric ID, rendering nothing when the account has none), XM-INV-ASSERT-STEPUP (an expired administrator step-up renews through the console assertion instead of the Keycloak step-up route that CR-0006 phase 2 step 5 unregistered), and the startup assertion that the runtime database role cannot `UPDATE console_assertion_nonces`.

XM-INV-ASSERT-STEPUP is the urgent half: the product owner hit the dead step-up card in production about forty minutes after step 5 closed the Keycloak login, and until this ships the only way past it is to log out and let the auto-login handshake mint a new session.

**Architecture:** RC86 is a backend-and-web roll-forward from RC85: no migration, no evaluator change, no authorization change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order (migrate no-op → idp → main → sources → restart api → restart ingest-proxy). The shadow evaluation is not required and is recorded as skipped. The release env carries over from RC85 unchanged, including `CONSOLE_ASSERTION_ADMIN_ROLE=admin` and `OIDC_ADMIN_LOGIN_ENABLED=false`.

The startup assertion is deliberately shipping one release *after* the revoke that satisfies it: `deploy/postgres/harden-runtime-role.sql` moved production to `t|t|f|f` in RC85's post-deploy permissions replay, so the api will pass the new check at boot. Shipping both in one release would have made the api refuse to start until an operator ran that job by hand.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-LEDGER-ACCOUNT-EMAIL.md`; `docs/handoffs/XM-INV-ASSERT-STEPUP.md`; `docs/CONFIGURATION.md` section 8 (mandatory production assertions)

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc85 (`f520976`) stay fixed; RC85 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC86 uses only `v0.1.0-rc86-signed`, `releaseName=0.1.0-rc86`, nine exact `:0.1.0-rc86` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc86-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- The startup assertion must not be relaxed to get the release out: if the api refuses to start, the correct response is to replay the permissions job, not to remove the check.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc86-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC86 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC86 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC85 with `INVOICE_IMAGE_TAG=0.1.0-rc86`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection, or migration change); record the skip.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. The api starting at all is itself the evidence for the new privilege assertion; confirm `console_assertion_nonces` still reads `t|t|f|f` afterwards. Record deployment evidence beside the release.
- [ ] Confirm in the embedded admin console that 用户账本 and 资格冻结队列 show an email under the numeric ID for accounts that have one, and show the ID alone for the account that does not. Production has 7 of 8 bound accounts with a verified address, so both branches are observable.
- [ ] Confirm the step-up renewal: leave the embedded console idle past the ten-minute MFA freshness window and require it to recover on its own, with a new `auth.console_assertion.exchanged` audit row and no visit to the dead Keycloak step-up route.

Production remains blocked until a 30-minute readiness watch binds RC86.
