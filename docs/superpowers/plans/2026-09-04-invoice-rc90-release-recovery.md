# RC90 Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC90 — four fixes, two of them for accounts that could not recover on their own:

- **XM-INV-SETTABLE-INVOICE-MINIMUM, second half.** RC89 relaxed the ¥200 floor in the migration, in `adminsettings` and in both frontend guards, and claimed that was all of them. It was not: `domain.MinimumRequestMinor` carried the same double meaning and six guards compared against it. The failure was worse than "no effect" — saving ¥5.00 committed to `admin_settings` and was then refused by `SetMinimumRequestMinor`, so the handler returned `LEDGER_POLICY_REFRESH_FAILED` with the row already written. The database said ¥5.00, the running process still said ¥200, and a restart could not reconcile them because the invoice-creation path re-checks the same floor every request. Production is in exactly that split state now.
- **XM-INV-PREANCHOR-BALANCE.** A `POLICY_ANCHOR` account's balance evidence dated before its own `cutover_at` is evaluated against an empty projection window, so `expected` is 0, the whole reported balance reads as an unexplained positive difference, and the account freezes `SOURCE_GAP` forever — the repair tool reopens the checkpoint and the next projection re-freezes it identically. Production account `98cce4c8` (external user 2092) has four such checkpoints.
- **XM-INV-RESOLVED-FREEZE-UNBLOCKS.** `block_state`'s "latest evaluation is not good" arm did not know that an operator had already resolved the freeze that evidence produced, so an idle account could never leave 对账中暂不可开票 — it produces no newer evidence, so the answered verdict stays "latest" forever. Production account 2820 has been in that state since 2026-09-03.
- **XM-INV-EMBED-LEGIBILITY.** The operator tables render at 9px headers and 10px cells, and the embedded layout shrank the page chrome further (an 8px eyebrow). Raised to legible sizes; embedding now changes the shape of the navigation and nothing about the size of the type.

**Architecture:** RC90 is a backend-and-web roll-forward from RC89: no migration, no authorization change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order. **The shadow evaluation is required and must not be skipped**: two of these change what the evaluator selects and how `block_state` is computed. Its known limitation stands (`docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md` — it drains an already-empty queue and exercises no projection), so the execution record must state how many rounds it actually ran rather than quoting the verdict alone, and the account-level outcome is verified on production after the deploy. The release env carries over from RC89 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** the four commits, plus `docs/handoffs/XM-INV-PREANCHOR-USAGE.md` for the fact-side precedent XM-INV-PREANCHOR-BALANCE mirrors.

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc89 (`0d56abf`) stay fixed; RC89 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC90 uses only `v0.1.0-rc90-signed`, `releaseName=0.1.0-rc90`, nine exact `:0.1.0-rc90` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc90-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs. RC88's roll-forward carried RC87's `SHA=` and RC89's carried three wrong hashes across three scripts; the rename pass rewrites `rcNN` strings and nothing else.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies** — each resolves paths relative to its own location), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc90-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC90 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC90 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC89 with `INVOICE_IMAGE_TAG=0.1.0-rc90`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] **Shadow evaluation, required.** Run it, require verdict `ready`, and record the rounds actually run. A `ready` after one round against an empty queue is not evidence for an evaluator change and must be reported as such rather than quoted as a pass.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Record deployment evidence beside the release.
- [ ] Post-deploy, confirm the running process now reports the configured ¥5.00 minimum, not ¥200 — the split state production is in today is the thing this release exists to end.
- [ ] Post-deploy, confirm account `98cce4c8` (2092) has no open freeze and does not re-acquire one on the next projection round; its four pre-anchor checkpoints must remain unevaluated rather than being evaluated to a difference.
- [ ] Post-deploy, confirm account 2820 leaves 对账中暂不可开票 without any new evidence arriving, on the strength of its 2026-09-03 resolution alone.
- [ ] Post-deploy, confirm the operator tables read at the larger type and the embedded eyebrow is no longer 8px.

Production remains blocked until a 30-minute readiness watch binds RC90. Watch the `balances` and `credits` streams' batch commits during the window as well as readyz.

## Known and deliberately not in this release

- `XM-INV-CATCHUP-BURST-BACKPRESSURE` and `XM-INV-SHADOW-EVAL-VACUOUS` remain open.
- Account 2820's underlying ¥0.50 is real upstream money with no credit event behind it — the same invisibility as an administrator-granted top-up, which the product owner has chosen to handle manually. This release stops that answered question from blocking the account; it does not explain the ¥0.50.
