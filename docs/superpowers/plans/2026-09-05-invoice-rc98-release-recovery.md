# RC98 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC98 was built at signed tag
> `v0.1.0-rc98-signed` (`c8878de`) and was not rolled forward (its provable-window
> differential PASSED -- fix 3 proved on the incident data, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc99-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and stage RC98, whose only change is the rehearsal instrument for XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3 (one flag and one restored exit over RC98), and use it to replay the 2026-09-04 catch-up burst forward-only on the pre-repair backup so the bounded evidence pass is finally proved or disproved on a real cash account:

- **`--reevaluate-evidence` no longer rewinds** `finalized_through` (RC95's rewind put accounts in a state production never enters and the bounded replay failed at commit on the cash accounts; `accounts_rewound` is gone from the report).
- **`--reproject-all` raises and never lowers** a captured job's `requested_through` (it used to overwrite it with `finalized_through`, erasing any backlog window a backup carried).
- **`--release-catchup <account>`** replays the RC87 post-deploy repair on the copy (clears `catchup_key_hmac`), and **`--finalization-window`** queues every finalization target through the window a finalization pass would have requested at the copy's last watermarks. Both rehearsal-only (superuser session on a restored copy).
- **`--finalization-window-lag <duration>`** derives that window from watermarks that much earlier. RC98's pair showed the instrument working (key cleared, eight accounts windowed, both runs ready) but the released account never replayed: a frontier window's carry-forward proof cannot close on a frozen copy, because the facts nearest the frontier were ingested under watermarks later than the window end and no balances cycle inside the window covers them. An hour of lag leaves the three-day window whole but provable.
- The `--reevaluate-evidence`-without-`--reproject-all` check gets its `os.Exit` back (RC98 logged and went on).
- `deploy/rehearsal/shadow-eval.sh` passes `--finalization-window-provable` through beside the RC96/RC98 flags; the static test covers its parsing.

**Architecture:** RC98 is a tools-image-only change over RC94, the release in production (RC95 and RC98 were built and staged but not rolled forward; see their plans). **No migration, no behaviour change in production** (the knob stays unset). Whether RC98 rolls forward at all is decided after its differential: production runs none of the changed code.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` ("Differential rehearsal, take three") and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc98 (`43eb05a`) stay fixed; RC94 is the release in production and its evidence is immutable; RC95 and RC98 are retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC98 uses only `v0.1.0-rc98-signed`, `releaseName=0.1.0-rc98`, nine exact `:0.1.0-rc98` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc98-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).
- The age identity is placed by the owner for the differential only and shredded by the runner on every exit path.

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against the disposable PostgreSQL, agents module tests, web typecheck/tests/build, gate self-test, shadow static test, release-range gitleaks — every exit 0.
- [ ] Create `v0.1.0-rc98-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC98 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions. Record whether the npm audit reached the service.

### Task 3: Rehearsal, then decide

- [ ] Sign exactly one strict-ready RC98 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC94, the live release, with `INVOICE_IMAGE_TAG=0.1.0-rc98`, `SOURCE_AGENT_VERSION=0.3.2`). Check the stage script's `PREV_SHA` against the running release first.
- [ ] **Differential rehearsal — the forward-only replay of the 2026-09-04 burst.** Backup `invoice-20260904T033226Z`, the rc98 tools image, `--reproject-all --release-catchup acdcdce9-c7f4-4cb4-9a02-ce527849a440 --finalization-window --finalization-window-provable --timeout 90m`, twice: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require both verdicts `ready`, `accounts_released` 1 in both, `accounts_projected` above zero, the released account's `projection_version` strictly greater in the bounded report, `after.accounts` identical in every field but `projection_version`, and `evaluations_by_status` identical. Anything else: the bound stays off and the difference is understood before anything ships.
- [ ] Decide the roll-forward on the outcome. RC98 changes nothing production runs; if it rolls forward it does so with a fresh signed pre-deploy backup, `bash deploy/roll-forward.sh <sha>` with readyz 200 in the verify step, the roll-forward script's own `SHA=` checked first, and the RC93 canary counters for 30 minutes with the knob confirmed absent from the api container's environment.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `c8878de` over `43a7c4e` (`--finalization-window-provable`, passthrough, static test, docs) and `9d00243` (RC97's execution record). Every gate 0 on `c8878de`: backend build/vet, full unit and integration suites, agents build/vet/tests, web typecheck/tests/build, release-range gitleaks, gate self-test, shadow static test, the four failure-evidence scripts 0/0/0/0. Tag `v0.1.0-rc98-signed` created on `c8878de`, SSH signature verified, peels to `HEAD`; the derived roll-forward script's `SHA=` points at it; the stage script's `PREV_SHA` stays at RC94 (`254d99dd…`), the running release.
- Task 2: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc98-task2-gate-20260905T145134Z-b636`, pid 112608): binding `c8878de…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc98-exact1`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`. Wall time 22:51 → 23:08 local.
- Transfer: the first scp was cut by a connection reset (the third today); the server was checked before retrying (ssh up, `/readyz` 200, six production containers, uptime 95 days) and the transfer re-run end to end; all three remote checksums matched. Stage: `RC98-STAGED sha=c8878dee5943787b710e53786cc50d9bc8160cec`; the stage script's `PREV_SHA` (RC94 `254d99dd…`) matched the running release. The rc98 tools image exposes `-finalization-window-provable` beside RC96/RC97's flags; production env still has no `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` line. The age identity was placed from the operator workstation by the assistant under the owner's standing arrangement and is shredded by the runner's exit trap.

### Differential rehearsal — the bound is proved on the incident's own data

Backup `invoice-20260904T033226Z` (the pre-repair state of the 2026-09-04 catch-up burst: `acdcdce9` still excluded, `finalized_through` 2026-09-01 12:14:30Z, three days of evidence pending), rc98 tools image, `--reproject-all --release-catchup acdcdce9-… --finalization-window --finalization-window-provable --timeout 90m`, `--evidence-batch-limit 0` (report `rehearsals/20260905T151810Z-3798120`) and `25` (`rehearsals/20260905T152416Z-3834837`). Migrations 0024 and 0025 applied to the copy.

| field | unbounded | bounded (25) |
| --- | --- | --- |
| verdict / round errors | ready / 0 | ready / 0 |
| `accounts_released` / `accounts_windowed` / `accounts_projected` | 1 / 8 / 7 | 1 / 8 / 28 |
| `rounds_run` | 1 | 22 |
| `acdcdce9` `finalized_through` | 2026-09-01T12:14:30Z → **2026-09-04T03:06:41Z** | identical |
| `acdcdce9` `consumed_cash_minor` | 546 → 5790 | identical |
| `acdcdce9` `projection_version` | 10 → 14 | 10 → **35** |
| `evaluations_by_status` | matched 2534 → 3060, negative_frozen 12 → 13, positive_blip_ignored 220, positive_classified_non_cash 10, source_gap_frozen 12 | identical |
| `after.accounts` (all fields but `projection_version`) | — | identical, per account |
| pending | `40bd883d` (its captured frontier job, `BALANCE_PROOF_PENDING`) | identical |

The window ended at 03:06:41Z, the ceiling the probes had named; the released account replayed its three days — 526 evaluations made and one negative freeze, `consumed_cash_minor` 546 → 5790 — once in a single pass and once in 22 chunks of at most 25 pending items, with every published quantity and every evaluation identical between the two. `RC98-DIFF-ACCEPTANCE PASS`. That is the acceptance the handoff's fix 3 section asked for, on the incident's own account and evidence rather than a fixture.

### Outcome

RC98 built, signed and staged, not rolled forward: nothing it changes is run by production, and the instrument lives in the signed tag and the staged release. Production stays on RC94 with `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` unset. Switching the bound on is a separate environment change with its own 30-minute canary, not made tonight. Rehearsal evidence: `deployment-records/rc98-rehearsal-20260905T153149Z` on the server.
