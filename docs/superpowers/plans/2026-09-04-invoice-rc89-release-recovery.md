# RC89 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC89 shipped at signed tag
> `v0.1.0-rc89-signed` (`0d56abf`) and is deployed; its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc99-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC89 — four defects the product owner found working in the embedded admin console, three of them things the console showed wrongly rather than computed wrongly:

- **XM-INV-EMBED-LOOP.** The embedded layout was `min-height: 100vh`, but inside the frame `100vh` is whatever height the console just applied from this page's own reported height. The document could therefore never be shorter than the frame, and every measurement came back two pixels taller — the iframe's own borders. The owner's console logged 650, 651, 653, 655 … 761 and climbing, and the frame kept its inner scrollbar all the way to the console's 4000px clamp. RC87 introduced this and its 1s poll turned an occasional wobble into a continuous climb.
- **XM-INV-LEDGER-SETTLING-STATE.** A queued projection job reported `block_state=not_invoiceable_pending_reconciliation`, which claims the books and the source disagree. Jobs are enqueued every finalization cycle, so healthy accounts flickered into that label all day: the owner's screenshot showed two of eight accounts carrying it while the database had none in that `eligibility_status` at all.
- **XM-INV-FREEZE-TRIGGER-VISIBLE.** Four open `SOURCE_GAP` freezes on one account rendered as four identical rows. The only field that told them apart — which balance checkpoint tripped each — was withheld from the wire, while the account ledger's own detail already renders the same pair into its `block_reason` sentence for the same operator.
- **XM-INV-SETTABLE-INVOICE-MINIMUM.** The ¥200 minimum invoice amount was meant to be a setting whose default is ¥200, but ¥200 was also an absolute floor in five places, so it could only ever move upward and a small-amount test was impossible.

**Architecture:** RC89 is a backend-and-web roll-forward from RC88 carrying **one migration, 0024**, which only relaxes a CHECK constraint (`minimum_request_minor >= 20000` → `> 0`) and leaves the column DEFAULT and every existing row untouched. No authorization change, source agent unchanged at 0.3.2. `deploy/roll-forward.sh` keeps the cutover order, applying 0024 first. The shadow evaluation is not required — no evaluator, projection or allocation logic changes here — and is recorded as skipped, with the RC88 finding that it cannot currently exercise a projection anyway (`docs/handoffs/XM-INV-SHADOW-EVAL-VACUOUS.md`) noted rather than relied on. The release env carries over from RC88 unchanged.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-OVERAGE-CARRY-FORWARD.md` (shipped in RC88, context only); the four slices above are documented in their commits and in `docs/ELIGIBILITY-OPERATIONS.md`'s ledger section.

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc88 (`0eb602d`) stay fixed; RC88 is the release in production and its evidence is immutable.
- RC49–RC52 anchors/tags remain immutable.
- RC89 uses only `v0.1.0-rc89-signed`, `releaseName=0.1.0-rc89`, nine exact `:0.1.0-rc89` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc89-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Migration 0024 must remain reversible in effect: it widens a constraint and changes no data, so a roll-back to RC88's binary keeps working against it as long as the stored value is still ≥ 20000. Confirm the production value before deploying, and say so in the execution record.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies** — each script resolves paths relative to its own location, so invoking the AUTOLOGIN copies against that tree fails with "exact directory namespace drifted"), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc89-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC89 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC89 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (load images, verify tag and evidence signatures, prepare the release env carried over from RC88 with `INVOICE_IMAGE_TAG=0.1.0-rc89`, `SOURCE_AGENT_VERSION=0.3.2`).
- [ ] Shadow evaluation: skipped by rule (no evaluator, projection or allocation change); record the skip and the reason.
- [ ] Take a fresh signed pre-deploy backup (signing key on tmpfs for the run only, shredded after), then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. **Check the roll-forward script's own `SHA=` before running it**: RC88's was mechanically derived from RC87's and still carried RC87's commit, which would have redeployed the old release under a new banner. Record deployment evidence beside the release.
- [ ] Confirm migration 0024 applied and that `admin_settings.minimum_request_minor` is unchanged at 20000.
- [ ] Post-deploy, confirm in the embedded console that the frame no longer shows an inner scrollbar and that its posted height settles instead of climbing — the failure signature was a `xm-embed` height message every second, each 2px larger than the last.
- [ ] Post-deploy, confirm an account with a queued projection job now reads 结算中 rather than 对账中暂不可开票, and that the 资格冻结 queue shows a distinct trigger on each of account 2092's four rows.
- [ ] Post-deploy, confirm the settings page accepts a minimum below ¥200 (the owner wants ¥5 to test invoicing) and that the public page then honours it.

Production remains blocked until a 30-minute readiness watch binds RC89. Watch the `balances` and `credits` streams' batch commits during the window as well as readyz: RC87's canary was broken by projection/ingest contention, and RC88's recorded one transient credits deadlock.

## Known and deliberately not in this release

- Account `98cce4c8` (external user 2092) stays frozen. Its POLICY_ANCHOR bootstrapped 21 hours after its own first post-policy checkpoint, stranding four checkpoints before its `cutover_at` where they evaluate against an expected balance of 0 and re-freeze immediately after any repair. The mechanism behind the late bootstrap is not yet established and is being investigated separately; guessing at a fix here would be the third wrong hypothesis on that account.
- `XM-INV-CATCHUP-BURST-BACKPRESSURE` and `XM-INV-SHADOW-EVAL-VACUOUS` remain open.

## Execution record (2026-09-04)

- Task 1: identity bump `f5e2795`. The gate refused the first full run, correctly: relaxing the ¥200 floor broke two fixtures that used `adminsettings.MinimumMinor` as "the amount a fresh install starts at" and `¥199.99` as "an invalid value". Both were true only while the floor and the default were the same number, which is exactly the conflation this release separates — so the fixtures were realigned to say what they mean (`DefaultMinimumRequestMinor` for the seed, a non-positive value for the invalid case) rather than the assertions weakened. Second run: every gate 0, web 185 tests. Four failure-evidence scripts 0/0/0/0.
- Task 2: `release/0.1.0-rc89-exact1`, image gate 42, verifiers 0 and 0.
- Task 3: staged; signed pre-deploy backup `invoice-20260904T092019Z`; roll-forward PASS, 18 containers on rc89, healthz/readyz 200. Migration 0024 applied; `admin_settings.minimum_request_minor` unchanged at 20000, constraint now `CHECK (minimum_request_minor > 0)`.
  - **Caught before running:** the derived `rc89-rollforward.sh` carried RC87's `SHA=`, `rc89-server-stage.sh`'s `PREV_SHA` had been mangled into a placeholder, and `rc89-backup.sh` still pointed at RC87's release directory and image tag. The rename pass rewrites `rcNN` strings and nothing else; every embedded hash must be checked by hand. RC88 had the same class of defect in one script, RC89 in three.
- Shadow evaluation: skipped by rule (no evaluator or allocation change).
- Post-deploy: the embedded frame stopped growing (the owner confirmed the inner scrollbar was gone), a queued projection job reported 结算中 rather than a reconciliation dispute, and the freeze queue rendered a distinct trigger on each of account 2092's four rows.

### What RC89 got wrong, found the same evening

RC89 claimed five places enforced the ¥200 floor and that all five were relaxed. There was a sixth layer: `domain.MinimumRequestMinor`, consulted by six guards across `application`, `ledger` and `postgresstore`. The claim was made by counting the places this slice had touched rather than by grepping the constant.

The consequence was not "the setting had no effect". Saving ¥5.00 committed to `admin_settings` and was then refused by `SetMinimumRequestMinor`, so the API returned `LEDGER_POLICY_REFRESH_FAILED` with the row already written: the database said ¥5.00 and the running process still said ¥200. That split state was reported to the product owner as harmless — "nothing can be invoiced right now anyway" — which was wrong. Any api restart re-reads settings at boot, and RC89's binary refuses to start on a value below its floor. The next backup quiesced the api for a consistent snapshot, it would not come back, and production served 502 for about twenty minutes until the setting was reverted to 20000. Recovering also needed the ingest-proxy restarted to re-resolve the recreated api's address; that step exists in `deploy/roll-forward.sh` for precisely this reason and had to be run by hand here.

The correct advice at the time was "this split state must be closed now, because the next restart will not come back", not "leave it". RC90 closes it: the database CHECK (`> 0`) and the code floor (`MinimumRequestFloorMinor = 1`) now agree exactly, so a value the database accepts can never be one the binary refuses.
