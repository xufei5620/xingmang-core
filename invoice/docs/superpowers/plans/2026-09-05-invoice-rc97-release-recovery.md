# RC97 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC97 was built at signed tag
> `v0.1.0-rc97-signed` (`e0a41d4`) and was not rolled forward (its lagged
> differential left the released account proof-pending, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc100-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and stage RC97, whose only change is the rehearsal instrument for XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3 (one flag and one restored exit over RC97), and use it to replay the 2026-09-04 catch-up burst forward-only on the pre-repair backup so the bounded evidence pass is finally proved or disproved on a real cash account:

- **`--reevaluate-evidence` no longer rewinds** `finalized_through` (RC95's rewind put accounts in a state production never enters and the bounded replay failed at commit on the cash accounts; `accounts_rewound` is gone from the report).
- **`--reproject-all` raises and never lowers** a captured job's `requested_through` (it used to overwrite it with `finalized_through`, erasing any backlog window a backup carried).
- **`--release-catchup <account>`** replays the RC87 post-deploy repair on the copy (clears `catchup_key_hmac`), and **`--finalization-window`** queues every finalization target through the window a finalization pass would have requested at the copy's last watermarks. Both rehearsal-only (superuser session on a restored copy).
- **`--finalization-window-lag <duration>`** derives that window from watermarks that much earlier. RC97's pair showed the instrument working (key cleared, eight accounts windowed, both runs ready) but the released account never replayed: a frontier window's carry-forward proof cannot close on a frozen copy, because the facts nearest the frontier were ingested under watermarks later than the window end and no balances cycle inside the window covers them. An hour of lag leaves the three-day window whole but provable.
- The `--reevaluate-evidence`-without-`--reproject-all` check gets its `os.Exit` back (RC97 logged and went on).
- `deploy/rehearsal/shadow-eval.sh` passes the new flags, `--finalization-window-lag` and `--timeout` through; the static test covers their parsing.

**Architecture:** RC97 is a tools-image-only change over RC94, the release in production (RC95 and RC97 were built and staged but not rolled forward; see their plans). **No migration, no behaviour change in production** (the knob stays unset). Whether RC97 rolls forward at all is decided after its differential: production runs none of the changed code.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` ("Differential rehearsal, take three") and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc97 (`43eb05a`) stay fixed; RC94 is the release in production and its evidence is immutable; RC95 and RC97 are retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC97 uses only `v0.1.0-rc97-signed`, `releaseName=0.1.0-rc97`, nine exact `:0.1.0-rc97` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc97-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).
- The age identity is placed by the owner for the differential only and shredded by the runner on every exit path.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against the disposable PostgreSQL, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks — every exit 0.
- [ ] Create `v0.1.0-rc97-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC97 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions. Record whether the npm audit reached the service.

### Task 3: Rehearsal, then decide

- [ ] Sign exactly one strict-ready RC97 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC94, the live release, with `INVOICE_IMAGE_TAG=0.1.0-rc97`, `SOURCE_AGENT_VERSION=0.3.2`). Check the stage script's `PREV_SHA` against the running release first.
- [ ] **Differential rehearsal — the forward-only replay of the 2026-09-04 burst.** Backup `invoice-20260904T033226Z`, the rc97 tools image, `--reproject-all --release-catchup acdcdce9-c7f4-4cb4-9a02-ce527849a440 --finalization-window --finalization-window-lag 1h --timeout 90m`, twice: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require both verdicts `ready`, `accounts_released` 1 in both, `accounts_projected` above zero, the released account's `projection_version` strictly greater in the bounded report, `after.accounts` identical in every field but `projection_version`, and `evaluations_by_status` identical. Anything else: the bound stays off and the difference is understood before anything ships.
- [ ] Decide the roll-forward on the outcome. RC97 changes nothing production runs; if it rolls forward it does so with a fresh signed pre-deploy backup, `bash deploy/roll-forward.sh <sha>` with readyz 200 in the verify step, the roll-forward script's own `SHA=` checked first, and the RC93 canary counters for 30 minutes with the knob confirmed absent from the api container's environment.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `e0a41d4` over `e4e2aa2` (`--finalization-window-lag`, the restored `os.Exit`, passthrough, static test, docs) and `dace2d2` (RC96's execution record). Every gate 0 on `e0a41d4`: backend build/vet, full unit and integration suites, agents build/vet/tests, web typecheck/tests/build, release-range gitleaks, gate self-test, shadow static test, the four failure-evidence scripts 0/0/0/0. (A self-test started by hand seconds after the bump, while the detached gate was starting, exited 1 once; re-run by hand and inside the gate it exited 0 — recorded, not explained.) Tag `v0.1.0-rc97-signed` created on `e0a41d4`, SSH signature verified, peels to `HEAD`; the derived roll-forward script's `SHA=` points at it; the stage script's `PREV_SHA` stays at RC94 (`254d99dd…`), the running release.
- Task 2: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc97-task2-gate-20260905T135219Z-ed74`, pid 22296): binding `e0a41d4…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc97-exact1`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`. Wall time 21:52 → 22:05 local.
- Transfer: first attempt complete (no reset this time); all three remote checksums matched. Stage: `RC97-STAGED sha=e0a41d43eac32fd9a3f04222e6e35d26be3293bc`, release dir `/root/invoice-system/app/releases/e0a41d4…`; the stage script's `PREV_SHA` (RC94 `254d99dd…`) matched the running release. The rc97 tools image exposes `-finalization-window-lag` beside RC96's flags; production env still has no `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` line. The age identity was placed from the operator workstation by the assistant (the owner's standing arrangement from this evening) and is shredded by the runner's exit trap.

### Differential rehearsal — an hour of lag was the wrong instrument

Backup `invoice-20260904T033226Z`, rc97 tools image, `--reproject-all --release-catchup acdcdce9-… --finalization-window --finalization-window-lag 1h --timeout 90m`, `--evidence-batch-limit 0` (report `rehearsals/20260905T140950Z-3406188`) and `25` (`rehearsals/20260905T141548Z-3442289`). Both `ready`, released 1/1, windowed 8/8, projected 6/6, `after.accounts` and `evaluations_by_status` identical — and `acdcdce9` still `BALANCE_PROOF_PENDING`, now at window 2026-09-04T02:11:42Z (the lag applied), `projection_version` 10 → 10. `RC97-DIFF-ACCEPTANCE FAIL`; the bound was not exercised.

Two read-only probes of the restored copy (a copy of `shadow-eval.sh` cut before the tool step, running SQL through `docker exec psql`, torn down by the same cleanup; the age identity placed and shredded around each) replaced the guess with numbers. The account's 1,399 usage facts since its `finalized_through` were ingested continuously — visibility lag p50 1 min, p90 3 min, max 22 min — not backfilled; 550 reconciliation checkpoints, none evaluated; balances cycles every ~65 s up to 03:32:07Z. The proof fails on the last slice of the window: the facts just before the window end are seen a few seconds to minutes later, and `ensureBalanceCarryForwardProofTx` needs a balances cycle ceiling inside the window at or after that visibility. RC96's window (03:11:42Z) contained a fact seen at 03:12:03Z; RC97's (02:11:42Z) contained one seen at 02:11:25Z, and the cycles closed at 02:10:45Z and 02:11:50Z. Production asks again a minute later with a wider window; a frozen copy asks once. The latest self-consistent ceilings — every fact before them already seen, with a final ingest batch and a prior checkpoint — were 03:06:41Z (five minutes below the un-lagged window), 03:19:51Z below the minimum watermark, 01:50:44Z below the lagged window.

RC98 adds `--finalization-window-provable`, which cuts each account's window to exactly that ceiling with the proof's own visibility set (usage and credit `stream_watermark_at`, cash lots' payments cycle ceilings), and a test that a fact seen after the next cycle pushes the cut back one cycle.

### Outcome

RC97 built, signed and staged, not rolled forward: nothing it changes is run by production. Production stays on RC94 with `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` unset. Rehearsal evidence and both probe outputs: `deployment-records/rc97-rehearsal-*` on the server.
