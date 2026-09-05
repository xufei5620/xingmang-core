# RC91 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC91 shipped at signed tag
> `v0.1.0-rc91-signed` (`eb467a6`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc99-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC91 — the third and final layer of a change two previous releases each declared finished, plus the diagnostic gap that let it hide:

- **XM-INV-SETTABLE-INVOICE-MINIMUM, layer three.** `invoice_requests.amount_minor >= 20000` has sat in migration 0001 since the beginning. With the setting at ¥5.00, production account 34 showed 可开票 ¥5.00, every application check passed, and the INSERT was refused by this constraint. Migration 0025 relaxes it to `> 0`; the real minimum stays in `admin_settings` and is enforced in the application, where a violation returns 422 naming the number it violated.
- **XM-INV-UNCLASSIFIED-500-SILENT.** A raw database error matches none of `handleDomainError`'s sentinels, so it became a bare HTTP 500 — and `writeError` does not log, so the api's log for that window held four lines and none was about the request. The failure was visible only because the edge nginx recorded the status. That arm now logs the error it could not classify; the wire message stays generic.

**Architecture:** RC91 is a backend roll-forward from RC90 carrying **one migration, 0025**, which relaxes a CHECK constraint and changes no data. No web change, no authorization change, source agent unchanged at 0.3.2. The shadow evaluation is skipped by rule — nothing here touches the evaluator, the projection or allocation. The release env carries over from RC90 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** the commit, plus migration 0025's own header.

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc90 (`0a074a8`) stay fixed; RC90 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC91 uses only `v0.1.0-rc91-signed`, `releaseName=0.1.0-rc91`, nine exact `:0.1.0-rc91` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc91-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs. RC88 shipped one wrong hash, RC89 three.
- The npm advisory waiver added in RC90 is available but must not be used if the audit can reach the service. Record which happened.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc91-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC91 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC91 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC90 with `INVOICE_IMAGE_TAG=0.1.0-rc91`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection or allocation change); record the skip and the reason.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Confirm migration 0025 applied: `invoice_requests_amount_minor_check` reads `CHECK (amount_minor > 0)`.
- [ ] **The verification this release exists for:** have account 34 submit a ¥5.00 invoice request and require it to succeed. Zero invoice requests have ever been created in this system; this would be the first. If it fails, the api log must now name the reason — that is the second half of this release.
- [ ] Confirm a request below the configured minimum is still refused, with a 422 that names the minimum rather than a 500.

Production remains blocked until a 30-minute readiness watch binds RC91.

## Known and deliberately not in this release

- A continuously-consuming account is still blocked from invoicing while any projection job is queued. Two read-only investigations established that the obvious discriminator (`requested_through` vs `finalized_through`) is unsound — the jobs table holds one merged row per account — and that a forward-advancing reprojection *can* lower a settled lot via backdated `UNKNOWN_POSITIVE` synthesis. No change is made on a premise that has been disproven.
- Account 12 remains blocked by a negative upstream balance. The magnitude of a negative balance is structurally unrepresentable (`CHECK (NOT balance_negative OR balance_service_units = 0)`), so "the negative is fully explained by the recorded overage" cannot be established in code. Its recovery path is a top-up, which RC88's carry-forward now settles automatically.
- `XM-INV-CATCHUP-BURST-BACKPRESSURE` and `XM-INV-SHADOW-EVAL-VACUOUS` remain open.

## Execution record (2026-09-05)

- Task 1: identity bump `eb467a6`; every gate 0, web 185 tests, four failure-evidence scripts 0/0/0/0. Tag `v0.1.0-rc91-signed` created and verified, peeling to `HEAD`.
- Task 2: `release/0.1.0-rc91-exact1`, first attempt, no retries. Binding bound to `eb467a68…`, loopback preflight passed on attempt 1, image gate 42, ordinary verifier 0, strict transfer-ready verifier 0.
  - **The audit ran for real.** RC90's waiver was available and was deliberately not used: the advisory service answered again from this machine (`found 0 vulnerabilities`, exit 0) when probed before the gate, so the gate script clears `INVOICE_RELEASE_AUDIT_WAIVER_BASELINE_TAG` rather than setting it, and the evidence carries the real result with no waiver warning line. The waiver is for an unreachable service, not a convenient one.
- Task 3: staged; signed pre-deploy backup `invoice-20260904T184003Z`; roll-forward PASS, 18 containers on rc91, healthz/readyz 200. Every embedded hash in the four derived server scripts was checked before running.
- Shadow evaluation: skipped by rule — no evaluator, projection or allocation change.

### Post-deploy verification

| what | result |
| --- | --- |
| migration 0025 | applied; production `invoice_requests_amount_minor_check` now reads `CHECK ((amount_minor > 0))` |
| the configured minimum | `admin_settings.minimum_request_minor` = 500, unchanged by the deploy |
| account 34 (`40bd883d`) | `active`, `POLICY_ANCHOR`, zero open freezes, **zero queued projection jobs**; one WALLET_CASH lot, cap ¥5.00, cash consumed ¥5.00, reserved 0, issued 0 → ¥5.00 invoiceable |
| `invoice_requests` | still 0 rows — the next submission will be this system's first invoice |
| the new log line | `unclassified request failure` count 0 over the first ten minutes; the canary script now reports it every run |

### What this release does not settle

The end-to-end submission itself. The three layers that refused it are now all relaxed and the local reproduction that produced the exact production error passes as a regression test, but no request has actually been created in production. That is the owner's test to run, and it is the only thing that turns "the constraint is gone" into "invoicing works".

Also unchanged: a continuously-consuming account is still blocked while any projection job is queued (account 34 happens to have none right now, which is why it is testable at all), and account 12 is still blocked by a negative upstream balance whose magnitude is structurally unstored. Both remain under the reasoning recorded in RC90.
