# RC96 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC96 was built at signed tag
> `v0.1.0-rc96-signed` (`43eb05a`) and was not rolled forward (its un-lagged
> differential left the released account proof-pending, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc100-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and stage RC96, whose only change is the rehearsal instrument for XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3, and use it to replay the 2026-09-04 catch-up burst forward-only on the pre-repair backup so the bounded evidence pass is finally proved or disproved on a real cash account:

- **`--reevaluate-evidence` no longer rewinds** `finalized_through` (RC95's rewind put accounts in a state production never enters and the bounded replay failed at commit on the cash accounts; `accounts_rewound` is gone from the report).
- **`--reproject-all` raises and never lowers** a captured job's `requested_through` (it used to overwrite it with `finalized_through`, erasing any backlog window a backup carried).
- **`--release-catchup <account>`** replays the RC87 post-deploy repair on the copy (clears `catchup_key_hmac`), and **`--finalization-window`** queues every finalization target through the window a finalization pass would have requested at the copy's last watermarks. Both rehearsal-only (superuser session on a restored copy).
- `deploy/rehearsal/shadow-eval.sh` passes the new flags and `--timeout` through; the static test covers their parsing.

**Architecture:** RC96 is a tools-image-only change over RC94, the release in production (RC95 was built, staged and backed up but not rolled forward; see its plan). **No migration, no behaviour change in production** (the knob stays unset). Whether RC96 rolls forward at all is decided after its differential: production runs none of the changed code.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` ("Differential rehearsal, take three") and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc95 (`ff84f48`) stay fixed; RC94 is the release in production and its evidence is immutable; RC95 is retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC96 uses only `v0.1.0-rc96-signed`, `releaseName=0.1.0-rc96`, nine exact `:0.1.0-rc96` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc96-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).
- The age identity is placed by the owner for the differential only and shredded by the runner on every exit path.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against the disposable PostgreSQL, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks — every exit 0.
- [ ] Create `v0.1.0-rc96-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC96 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions. Record whether the npm audit reached the service.

### Task 3: Rehearsal, then decide

- [ ] Sign exactly one strict-ready RC96 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC94, the live release, with `INVOICE_IMAGE_TAG=0.1.0-rc96`, `SOURCE_AGENT_VERSION=0.3.2`). Check the stage script's `PREV_SHA` against the running release first.
- [ ] **Differential rehearsal — the forward-only replay of the 2026-09-04 burst.** Backup `invoice-20260904T033226Z`, the rc96 tools image, `--reproject-all --release-catchup acdcdce9-c7f4-4cb4-9a02-ce527849a440 --finalization-window --timeout 90m`, twice: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require both verdicts `ready`, `accounts_released` 1 in both, `accounts_projected` above zero, the released account's `projection_version` strictly greater in the bounded report, `after.accounts` identical in every field but `projection_version`, and `evaluations_by_status` identical. Anything else: the bound stays off and the difference is understood before anything ships.
- [ ] Decide the roll-forward on the outcome. RC96 changes nothing production runs; if it rolls forward it does so with a fresh signed pre-deploy backup, `bash deploy/roll-forward.sh <sha>` with readyz 200 in the verify step, the roll-forward script's own `SHA=` checked first, and the RC93 canary counters for 30 minutes with the knob confirmed absent from the api container's environment.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `8d501c8` over `e0b6861` (the rehearsal instrument: rewind withdrawn, `--reproject-all` raises-never-lowers, `--release-catchup`, `--finalization-window`, `--timeout` passthrough) and `7d31f14` (RC95's execution record). Every gate 0 on `8d501c8` — backend build/vet, full unit and integration suites, agents build/vet/tests, web typecheck/tests/build, shadow static test, release-range gitleaks, the four failure-evidence scripts 0/0/0/0 — except the gate self-test, which refused the plan once, correctly: it paraphrased the verifier contract instead of carrying the literals. Fixed as a docs-only commit `43eb05a`; self-test 0 and release-range gitleaks 0 re-run on it. Tag `v0.1.0-rc96-signed` created on `43eb05a`, SSH signature verified, peels to `HEAD`; the derived roll-forward script's `SHA=` points at it; the stage script's `PREV_SHA` stays at RC94 (`254d99dd…`), the running release, because RC95 never rolled forward.
- Task 2: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc96-task2-gate-20260905T125504Z-2f6f`, pid 75952): binding `43eb05a…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc96-exact1`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`. Wall time 20:55 → 21:07 local.
- Transfer: images tar `7c5218da…`, source bundle `8849c82d…`, evidence tgz `c3ba85d8…`. As with RC95, the first scp was cut by a connection reset; the server was checked before retrying (ssh up, `/readyz` 200, uptime 95 days, six production containers, no reboot) and the transfer re-run end to end; all three remote checksums matched.
- Stage: `RC96-STAGED sha=43eb05a8919251e0b79ca66d91a943d172d8ed1d`, release dir `/root/invoice-system/app/releases/43eb05a…`; the stage script's `PREV_SHA` (RC94 `254d99dd…`) matched the running release. The rc96 tools image exposes `-reproject-all`, `-reevaluate-evidence`, `-evidence-batch-limit`, `-release-catchup`, `-finalization-window`, `-timeout`; production env still has no `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` line.

### Differential rehearsal — the instrument works, the frontier proof does not close on a frozen copy

Backup `invoice-20260904T033226Z` (the pre-repair state: `acdcdce9` still excluded, `finalized_through` 2026-09-01 12:14:30Z), rc96 tools image, `--reproject-all --release-catchup acdcdce9-… --finalization-window --timeout 90m`, `--evidence-batch-limit 0` (report `rehearsals/20260905T132946Z-3171158`) and `25` (`rehearsals/20260905T133541Z-3208870`). Migrations 0024 and 0025 applied to the copy by the migrate step.

| field | unbounded | bounded |
| --- | --- | --- |
| `accounts_released` / `accounts_windowed` / `accounts_enqueued` | 1 / 8 / 8 | 1 / 8 / 8 |
| `accounts_projected` / `pending_accounts` | 6 / 2 | 6 / 2 |
| pending | `40bd883d`, `acdcdce9`: `BALANCE_PROOF_PENDING`, window 2026-09-04T03:11:42Z | identical |
| `acdcdce9` `projection_version` / `finalized_through` | 10 → 10 / unchanged | 10 → 10 / unchanged |
| `after.accounts` diffs, `evaluations_by_status` | — | identical (no account differs; nothing was chunked) |
| verdict | ready | ready |

The two new operations did what they were built for — the key was cleared, every account was queued through the window a finalization pass would have requested (the minimum stream watermark 03:26:42Z minus the 900 s delay) — and the six accounts that were already current projected trivially. The released account never replayed: `ensureBalanceCarryForwardProofTx` covers each fact's visibility (the stream watermark it was ingested under) with a balances checkpoint or a published balances cycle whose ceiling lies inside the window, and the facts nearest the frontier were ingested under watermarks later than the window end. In production the next finalization pass widens the window and the proof closes; on a frozen copy there is no next pass, so a frontier window pends forever — the same shape as `40bd883d`'s captured job on every backup so far. `RC96-DIFF-ACCEPTANCE FAIL` (released account not chunked) is the instrument's failure, not the bound's; the bound was never exercised.

The repair is one flag: `--finalization-window-lag`, the window a finalization pass would have requested that much earlier, so an hour of lag leaves the three-day window whole but provable. That is RC97. Also found while adding it: RC96's `--reevaluate-evidence`-without-`--reproject-all` check had lost its `os.Exit` (it logged and went on); restored in RC97.

### Outcome

RC96 built, signed and staged, not rolled forward: nothing it changes is run by production. Production stays on RC94 with `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` unset. Rehearsal evidence: `deployment-records/rc96-rehearsal-*` on the server.
