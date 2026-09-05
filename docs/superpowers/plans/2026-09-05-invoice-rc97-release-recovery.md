# RC97 Release Implementation Plan

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
