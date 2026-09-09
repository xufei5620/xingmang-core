# RC95 Release Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC95 was built at signed tag
> `v0.1.0-rc95-signed` (`ff84f48`) and was not rolled forward (its rewind-based
> differential failed on the cash accounts, see its execution record); its evidence is
> immutable. Continue only with
> `docs/superpowers/plans/2026-09-05-invoice-rc100-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task.

**Goal:** Build, strictly verify, sign, and deploy RC95, which ships the last fix of the 2026-09-04 stall's ranking **switched off**, and uses its own rehearsal to decide whether it may ever be switched on:

- **XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3, behind `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`.** With a bound, a projection job cuts its window at the as_of of the limit-th pending balance-evidence item, publishes that, and requeues itself for the rest; unbounded (`0`, the production default in this release) is byte-for-byte the previous behaviour. The rehearsal tool takes the same knob as `--evidence-batch-limit` and records it in the report.

**Architecture:** RC95 is a backend roll-forward from RC93 (RC94 was built, staged and rehearsed but not rolled forward: its differential rehearsal turned out inert, see its plan) with **no migration** and **no behaviour change in production** (the knob defaults to `0`). The change is in one job function plus the rehearsal tool and scripts. No evaluator, projection or allocation logic changes. **The shadow evaluation is run three times: the ordinary `--reproject-all` pass, and the differential pair** (see Task 3). Source agent stays 0.3.2. Release env carries over from RC93 unchanged — `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` is deliberately not set.

**Tech Stack:** PowerShell 7.5+, Git signatures, gitleaks 8.30.1, Docker/Compose, Trivy 0.74.0.

**Spec:** `docs/handoffs/XM-INV-CATCHUP-BURST-BACKPRESSURE.md` (the fix 3 section) and the runbook's "Bounded evidence pass and the differential rehearsal".

## Constraints

- Signed tags rc53 (`9bbae4a`) through rc94 (`254d99d`) stay fixed; RC93 is the release in production and its evidence is immutable; RC94 is retained as built-but-undeployed evidence.
- RC49–RC52 anchors/tags remain immutable.
- RC95 uses only `v0.1.0-rc95-signed`, `releaseName=0.1.0-rc95`, nine exact `:0.1.0-rc95` references, `SourceAgentVersion=0.3.2`, and one new `release/0.1.0-rc95-exactN`.
- Toolchain/environment failures re-run into a fresh exactN under the SAME release name; failed exactN directories are retained evidence and never edited. Detached gates are launched from PowerShell.
- Every embedded commit hash in a derived server script is checked before it runs.
- The npm advisory waiver must not be used if the audit can reach the service. Record which happened.
- No targeted `go test` while the full suite runs (shared test database).

### Task 1: Source identity

- [ ] From `K:\发票\wt-XM-INV-AUTOLOGIN`, verify PowerShell 7.5+, all four failure-evidence scripts (run from `K:\发票\wt-XM-INV-SEC-RC49`, using **that worktree's own copies**), build/vet for backend and agents, full unit suite, integration suite against a disposable PostgreSQL 18 (loopback timeouts re-run in isolation; assertion failures stop), agents module tests, web typecheck/tests/build, `deploy/rehearsal/test-shadow-eval.sh`, gate self-test, and release-range gitleaks; capture every native exit immediately and require `0`.
- [ ] Create `v0.1.0-rc95-signed` only if absent, verify it, and require the fully qualified tag to peel to `HEAD`.

### Task 2: Image evidence

- [ ] In one block (run through `scripts/run-detached.ps1` from PowerShell), bind worktree/tag/HEAD, select the first unused RC95 exactN, run the image gate with `-SourceAgentVersion 0.3.2` and require exit `42`, then require ordinary and strict verifier exits `0` and `0` without manually parsing manifest decisions; retry into a fresh exactN only when every failed backend package passes in isolation immediately afterwards.

### Task 3: Rehearsal and production

- [ ] Sign exactly one strict-ready RC94 directory, transfer only its nine manifest-bound images plus the signed source bundle and evidence, and stage (release env carried over from RC93 (RC94 was never live) with `INVOICE_IMAGE_TAG=0.1.0-rc95`, `SOURCE_AGENT_VERSION=0.3.2`, and **no** `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`).
- [ ] **Differential rehearsal — the acceptance of fix 3, this time with the bound engaged.** The newest signed backup, `--reproject-all --reevaluate-evidence`, twice with the rc95 tools image: `--evidence-batch-limit 0` and `--evidence-batch-limit 25`. Require, in both reports, `evaluations_cleared` > 0 and equal and `accounts_rewound` == the number of POLICY_ANCHOR accounts (4 at the time of writing); **the bounded run's `projection_version` strictly greater than the unbounded run's for every rewound account** (the proof the bound chunked — RC94's pair failed exactly this, silently); both verdicts `ready`, both 8 of 8 with `pending_accounts` null; `evaluations_by_status` identical; `after.accounts` identical in every field but `projection_version`. Record both report paths. A quantity difference does not block this release (the knob is off in production) but blocks ever switching it on, and is filed as a finding.
- [ ] Take a fresh signed pre-deploy backup, then run `bash deploy/roll-forward.sh <sha>` and require readyz 200 in the verify step. Check the roll-forward script's own `SHA=` first. Record deployment evidence beside the release.
- [ ] Post-deploy: 30-minute readiness watch with the RC93 counters (lock timeouts, cycle deferrals, late balances cycles, busy events); confirm the api runs with the knob unset (`ELIGIBILITY_EVIDENCE_BATCH_LIMIT` absent from the container env).

Production remains blocked until a 30-minute readiness watch binds RC95.

## Known and deliberately not in this release

- Switching the bound on. That is a follow-up env change gated on the differential rehearsal above, with its own canary.
- A continuously-consuming account is still blocked from invoicing while any projection job is queued; account 12 is still blocked by a negative upstream balance. Both remain under the reasoning recorded in RC90.

## Execution record (2026-09-05)

- Task 1: identity bump `ff84f48`; every gate 0, web 185 tests, four failure-evidence scripts 0/0/0/0; gate self-test 0 on the first run. Tag `v0.1.0-rc95-signed` created and verified, peeling to `HEAD`; the derived roll-forward script's `SHA=` checked against the tag commit before anything ran. RC94 was rolled forward first at the owner's request (a clean canary, bound off), so RC95 rolls forward from RC94; its backup script points at RC94's release directory and image tag, checked by hand.
- Task 2: detached image gate from PowerShell (`scripts/run-detached.ps1`, run dir `rc95-task2-gate-20260905T105732Z-6726`, pid 24744): binding `ff84f48d…`, preflight 0 on attempt 1, release dir `release\0.1.0-rc95-exact1`, `IMAGE-GATE-EXIT=42`, ordinary/strict verifiers 0/0, evidence audit line `found 0 vulnerabilities`. Wall time 18:57 → 19:19 local.
- Transfer: images tar `d8457375…`, source bundle `934ac96e…`, evidence tgz `5e36659c…`. The first scp was cut by a connection reset after 399 MB of the 615 MB images tar; the server was checked before retrying (ssh up, `/readyz` 200, uptime 95 days — no reboot, production untouched) and the transfer re-run end to end; all three remote checksums matched.
- Stage: `RC95-STAGED sha=ff84f48d57f672e299b457fd4b67eb16c3a66c96`, release dir `/root/invoice-system/app/releases/ff84f48d…`; the stage script's embedded `PREV_SHA` (RC94 `254d99dd…`) matched the running release. The rc95 tools image (`c2727c2d3116`) exposes `-reproject-all`, `-reevaluate-evidence`, `-evidence-batch-limit`; production env has no `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` line (bound stays off through this roll-forward).
- Backup (before the differential and the roll-forward): `invoice-20260905T113030Z`, signed manifest verified, exit 0; taken from RC94's release directory and image tag (`254d99dd…`, `0.1.0-rc94`), i.e. the running release. The signing key stays on tmpfs for the ceremony; the age identity is placed by the owner only for the differential and shredded by the runner's exit trap.

### Differential rehearsal — the rewind design fails on real cash accounts

Backup `invoice-20260905T113030Z`, rc95 tools image, `--reproject-all --reevaluate-evidence`, `--evidence-batch-limit 0` (report `rehearsals/20260905T114247Z-2534047`) and `25` (`rehearsals/20260905T115143Z-2587628`).

| field | unbounded | bounded |
| --- | --- | --- |
| `accounts_rewound` | 4 | 4 |
| `evaluations_cleared` | 2923 | 2923 |
| `accounts_projected` / `pending_accounts` | 8 / null | 13 / 2 (`40bd883d`, `acdcdce9`: `PROJECTION_FAILED` ×3) |
| `rounds_run` / `round_errors` | 1 / 0 | 7 / 3 |
| bounded `projection_version` > unbounded | — | `6706ea6a`, `98cce4c8` (the two rewound accounts without cash) |
| `after.accounts` diffs (excl. `projection_version`) | — | 2 (the two failed accounts only) |
| `evaluations_by_status` identical | — | no (the two failed accounts' evaluations were left half-remade) |
| verdict | ready | **not_ready** (`wallet consumption state lacks matching usage allocations`, SQLSTATE P0001, three times) |

The rewind engaged (4/4) and the bound engaged; on the two rewound accounts with no cash the bounded replay finished with quantities identical to the single pass. On the two cash accounts every chunk failed at commit on the deferred constraint trigger `consumption_allocations_mirror_guard` (migration 0011): a chunk's rebuild deletes **all** of the account's `consumption_allocations` and re-inserts only those for usage up to the chunk boundary, while the lot loader (`buildEligibilityProjectionTx`) takes only lots with `completed_at <= through` — so a lot funded after the first boundary keeps the consumption state the restored copy already carried (non-zero, from production's full projection) and now has no allocations: "state lacks matching allocations".

That state — a published boundary behind lots that already carry consumption — is one production never enters: `completeEligibilityProjectionJob` clamps `requested` up to `finalized_through`, `finalized_through` only ever moves by `GREATEST`, and a lot's consumption state is written only by a projection whose `through` covered it (after which `finalized_through` covers it too). The re-anchor migration path, the one legitimate backward move, resets allocations, lot states and `consumed_cash_minor` to zero in the same transaction. The RC95 rewind moved the boundary without that reset, and a reset would not save it either: `acdcdce9` has an issued invoice, and a partial replay from cutover computes consumption below `reserved+issued`, which the lot update refuses (`ErrConflict`) — again a state production cannot reach, because issuance follows finalized consumption.

Conclusion: the bounded evidence pass was **not** disproved — forward-only chunking keeps every invariant by construction, and it held where it engaged — but it was not proved on cash accounts either, and the rewind cannot be the instrument. RC95 was **not rolled forward** (its only change over RC94 is that instrument); production stays on RC94 with the bound off. The faithful differential is forward-only: a backup taken while the 2026-09-04 backlog was really pending (`invoice-20260904T065429Z` first), its real job queue drained twice (no `--reproject-all`, whose enqueue overwrites every job's `requested_through` with `finalized_through` and would erase the backlog window; no rewind), acceptance = both ready, `accounts_projected > 0`, bounded `projection_version` greater on at least one account, quantities and `evaluations_by_status` identical.

### The 06:54 backup has no backlog either

`invoice-20260904T065429Z`, the earliest backup after the burst, was tried forward-only with the rc95 tools image (real queue, no `--reproject-all`, no rewind; reports `rehearsals/20260905T122243Z-2765654` and `20260905T122835Z-…`): every account's `finalized_through` already stood at 06:27–06:33Z — production had caught up by then — and the queue held only the two cash accounts' jobs, three to six minutes wide and `BALANCE_PROOF_PENDING` on the frozen copy. `accounts_projected` 0 and 0; nothing to compare. The backlog window existed only between the RC87 post-deploy repair (~03:58Z, `catchup_key_hmac` cleared for `acdcdce9`) and the replay that landed at 04:16:42Z, and no backup was taken inside it. The pre-repair backup `invoice-20260904T033226Z` is the incident's true starting state — the account still excluded, `finalized_through` at 2026-09-01 12:14Z, three days of evidence pending — and reproducing the burst from it needs the repair and the finalization window replayed on the copy: RC96's `--release-catchup` and `--finalization-window`.

### Outcome

RC95 built, signed, staged and backed up, but not rolled forward: its only change over RC94 is the rewind, and the rewind is withdrawn. Production stays on RC94 with `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` unset.
