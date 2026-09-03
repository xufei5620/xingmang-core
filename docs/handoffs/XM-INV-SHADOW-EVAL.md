# XM-INV-SHADOW-EVAL: release-rehearsal shadow evaluation of the eligibility projection worker

- **status:** implemented and self-tested locally; gates below. Not run against a real production
  backup or a real server (see "Not run").
- **branch:** `ai/claude/XM-INV-SHADOW-EVAL` (based on `ai/claude/XM-INV-AUTOLOGIN` at `0177e72`),
  worktree `K:/发票/wt-XM-INV-SHADOW-EVAL`.
- **commits:**
  - `6fe4145` feat(eligibility-shadow): add release-rehearsal projection driver and reporting queries
  - `2fb6331` feat(rehearsal): add shadow-eval.sh orchestration and its static test
  - `d1f0468` test(eligibility-shadow): pin the Go/bash report JSON-shape contract
  - plus the commit adding this handoff and the `docs/PRODUCTION-RUNBOOK.md` section 11.2 it
    describes (this file's own commit is not self-referenceable by hash from inside itself; see
    `git log --oneline 0177e72..HEAD` on this branch for the exact, current commit list)

## Summary

XM-INV-SHADOW-EVAL adds a release-rehearsal tool: before a candidate RC is rolled forward, restore
the database component of the newest signed production backup into a throwaway, isolated PostgreSQL
container, run the candidate's own eligibility-projection worker
(`ProcessEligibilityProjectionJobs`/`EligibilityProjectionHealth`) against that restored copy until
its queue drains, and report the delta in open freezes and projection errors versus the state
immediately after restore. This is exactly the class of incident XM-INV-BLIP-SOFTFAIL fixed after
the fact (a projection batch's evaluator error looping a whole account's retries) -- this tool exists
to catch that kind of regression against real production data before a release ships, not just in a
synthetic integration-test fixture.

Three pieces:

1. `backend/cmd/eligibility-shadow` -- a new tools-image binary. Opens the restored database, drains
   `eligibility_projection_jobs` (via the exact same exported worker entry points production uses,
   never a re-implementation of the evaluator), takes a snapshot immediately after restore ("before")
   and again after draining ("after"), and prints one JSON report to stdout.
2. `backend/internal/postgresstore/eligibility_shadow_report.go` -- three new read-only, lock-free
   Store methods the report needs (per-account eligibility status + open freezes by reason,
   evaluation outcomes by status keeping only each checkpoint/proof's latest evaluation, and the
   count of currently-claimable projection jobs). None of these run on any production hot path.
3. `deploy/rehearsal/shadow-eval.sh` -- the orchestrator. Verifies the backup's signed manifest,
   restores only the database dump (streamed directly from `age` into `pg_restore` over a pipe, never
   touching disk) into a throwaway container on an isolated Docker network, runs the tools image's
   `invoice-eligibility-shadow`, writes the report, tears everything down, and exits 0/3/2/1 per the
   task's release-blocking contract. `deploy/rehearsal/shadow-eval-lib.sh` independently recomputes
   the same readiness verdict from the published JSON in pure bash/awk; `shadow-eval.sh` compares its
   own recomputation against the tool's own exit status and treats a disagreement as a
   rehearsal-tooling failure in its own right, not just trusting either side.

`docs/PRODUCTION-RUNBOOK.md` section 11.2 documents when to run it, the exact server command, how to
read the report, and the literal bullet to add to a future RC plan's Task 1.

## Design decisions and deviations worth flagging

Flagging these here rather than only in a final self-review, per this session's own standing
practice.

1. **Scope narrower than a full restore drill, deliberately.** `restore-drill.sh` validates the
   *entire* signed backup set (database, documents, all ten source-state directories, both cutover
   pairs, metadata, optional Keycloak dump) and needs the field keyring plus fourteen source
   spool/cutover/balance-snapshot keys to do it. Eligibility projection touches only the `invoice`
   database's own tables -- never documents, never source-state, never any encrypted field -- so
   `shadow-eval.sh` restores and checks only the database component's line in the signed manifest.
   It does **not** certify the backup as a whole; the standard restore drill (run separately, already
   part of the normal backup pipeline) still owns that. This means `shadow-eval.sh` needs only
   `BACKUP_ALLOWED_SIGNERS_FILE` and `AGE_IDENTITY_FILE`, not the field keyring or the ten source
   keys `restore-drill.sh` requires.
2. **No `jq` dependency.** `deploy/backup/backup.sh` and `deploy/backup/restore-drill.sh` -- the two
   scripts this one mirrors most closely -- both deliberately keep their tool list to
   widely-available coreutils/OpenSSH/age/Docker; neither uses `jq`. `jq` is also not installed on
   this development machine, so a `jq` dependency would have been untestable here even though the
   production server likely has it (via the Keycloak provisioning scripts). `shadow-eval-lib.sh`
   instead hand-parses `backend/cmd/eligibility-shadow`'s exact, deterministic
   `json.MarshalIndent(report, "", "  ")` output shape (2-space indent, fixed Go struct field order,
   non-empty arrays always expanded one element per line, empty arrays inlined as `[]`, nil slices as
   `null`). That contract is pinned on the Go side by
   `TestReportJSONShapeMatchesShadowEvalLibAssumptions` in `report_test.go` -- if a future change to
   `Report`'s field order or serialization ever breaks one of these properties, that test fails
   loudly instead of the bash parser silently misreading the report.
3. **Independent bash recomputation of the verdict, not just the tool's exit code.** The task brief's
   exit-code contract (0/3/2/other) is release-blocking, so `shadow-eval.sh` does not simply
   propagate `invoice-eligibility-shadow`'s own process exit status -- it recomputes the same
   ready/not-ready decision itself from the published JSON (`shadow_eval_verdict_exit_code` in
   `shadow-eval-lib.sh`) and fails the whole rehearsal (exit 1, a tooling failure, not a release
   verdict) if the two disagree. This matches this repo's existing double-checked-gate style (e.g.
   `scripts/verify.ps1`'s many independent re-derivations) and means a bug in either implementation
   alone cannot silently wave a regressing release through.
4. **"Queue drained" means nothing is *currently claimable*, not that every row eventually
   succeeded.** `EligibilityProjectionClaimableCount` mirrors
   `ProcessEligibilityProjectionJobs`' own claim-query `WHERE` clause exactly (by hand -- the claim
   query is a single `UPDATE ... FROM` CTE and cannot itself be reused as a read-only count).
   `queue_drained: true` means the loop stopped because nothing was immediately actionable at that
   moment, which can be true even while rows remain in `status='failed'` (5-minute backoff) or
   `BALANCE_PROOF_PENDING` (up to a 10-minute backoff) -- those still show up in `failed_accounts`
   and the after-snapshot's freeze/evaluation counts, and still correctly drive the verdict. The
   runbook section calls out `queue_drained: false` (hit `--max-rounds` while still finding claimable
   work) as inconclusive, not a pass, and recommends a re-run with a higher `--max-rounds`.
5. **No field keyring, no `--apply`, no freeze resolution.** Unlike `eligibility-repair`, this tool
   never resolves a freeze or writes an encrypted resolution note -- it only drives the ordinary
   projection worker, which itself only ever *creates* freezes (never resolves them) and never
   touches an encrypted column. So it needs no field keyring at all, and there is no `--apply`/dry-run
   distinction to design around.
6. **Evaluation-status counting deliberately covers only `balance_checkpoint_evaluations`'s
   integration-test path fully; `balance_carry_forward_proofs`' FK chain
   (`source_economic_scan_cycles`/`source_ingest_batches`) was judged too heavy to fixture for a
   secondary reporting feature's test, and is exercised only via the `UNION ALL` query construction
   itself, not a seeded integration test.** Flagged as a real, if minor, test gap below.
7. **Runbook section title is bilingual (`### 11.2 影子评估 / shadow evaluation ...`)** to satisfy the
   task brief's literal Chinese section name while keeping the section's own prose in English, matching
   `PRODUCTION-RUNBOOK.md`'s existing convention of being essentially all-English (it contains no other
   prose in Chinese; the handful of pre-existing CJK characters in the file are literal path segments
   and config values, not prose).
8. **`shadow-eval.sh` mounts its own tiny tmpfs work directory (falling back to a plain `mktemp -d`
   with a warning if `mount` is unprivileged/unavailable) even though the only file it ever writes
   there is a throwaway, non-secret, container-local restore password** (`restore-drill-only`,
   matching `restore-drill.sh`'s own convention) -- the decrypted database dump itself is streamed
   directly from `age` into `pg_restore` over a pipe and never touches disk at all. The tmpfs mount
   and `shred -u` before removal satisfy the task's explicit "shred any tmpfs key material"
   requirement as defense in depth, even though there is no real secret in play.

## Server command line to run a rehearsal

```bash
BACKUP_DIR=/root/invoice-system/backups \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
AGE_IDENTITY_FILE=/offline/backup-age-identity.txt \
  bash deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rcNN
```

Optional: `--backup invoice-TIMESTAMP` (defaults to the newest signed backup under `BACKUP_DIR`),
`--max-rounds N` (default 200), `--batch-limit N` (default 25). See
`docs/PRODUCTION-RUNBOOK.md` section 11.2 for the full "when" / "how to read the report" writeup and
the RC-plan Task 1 bullet to add for any RC that changes the evaluator/projection.

## Files changed

- `backend/cmd/eligibility-shadow/main.go` -- CLI entry point: flag parsing, database open/migration
  verify, the drain loop (claimable-count check, then `ProcessEligibilityProjectionJobs`, capped by
  `--max-rounds`), snapshot-before/after, JSON report to stdout, human log to stderr, exit code.
- `backend/cmd/eligibility-shadow/report.go` -- `Report`/`Snapshot`/`FreezeCount`/`EvaluationCount`/
  `FailedAccount`/`ProjectionHealth` JSON types, `EvaluateReadiness` (pure comparison logic) and
  `ExitCode`.
- `backend/cmd/eligibility-shadow/report_test.go` -- unit tests for `EvaluateReadiness`/`ExitCode`
  (no database) plus `TestReportJSONShapeMatchesShadowEvalLibAssumptions` (the Go↔bash shape
  contract, see design decision 2 above).
- `backend/internal/postgresstore/eligibility_shadow_report.go` -- `EligibilityShadowSnapshot`,
  `EligibilityProjectionClaimableCount`, `EligibilityShadowFailedJobs`.
- `backend/internal/postgresstore/eligibility_shadow_report_integration_test.go` -- four integration
  tests against a real PostgreSQL instance (account/freeze snapshot correctness, latest-evaluation-only
  counting, claimable-count across five job states, failed-jobs listing).
- `backend/Dockerfile` -- new `invoice-eligibility-shadow` build line and COPY-list entry in the tools
  stage; the two literal substrings `scripts/verify.ps1` checks
  (`/out/invoice-oidc-preflight ./cmd/oidc-preflight` and `/out/invoice-oidc-preflight /usr/local/bin/`)
  are unchanged.
- `deploy/rehearsal/shadow-eval.sh` -- the orchestrator (see Summary).
- `deploy/rehearsal/shadow-eval-lib.sh` -- the bash-side report-comparison/summary functions, sourced
  by both `shadow-eval.sh` and `test-shadow-eval.sh`.
- `deploy/rehearsal/test-shadow-eval.sh` -- static test: every argument-parsing failure path, the
  comparison logic against fixture JSON (including the empty-array edge case), and the human summary.
- `docs/PRODUCTION-RUNBOOK.md` -- new section 11.2 (see Summary).
- `docs/handoffs/XM-INV-SHADOW-EVAL.md` -- this document.

**Not touched:** any Sub2API/NewAPI source ([[no-upstream-source-changes]]), `deploy/backup/backup.sh`
or `deploy/backup/restore-drill.sh` (read only, per the task brief), `deploy/docker-compose.prod.yml`
or any other Compose file (this tool never runs via Compose and needs no new service), any migration,
`backend/cmd/api`, `backend/internal/postgresstore/consumption.go` or any other existing evaluator/
projection code (read only, per the task brief -- this tool drives the existing worker, it does not
change it), release identity files, `release/`, `contracts/`, `web/`.

## Tests run

From `backend/`, with `INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_shadow`
(dedicated per-worktree database, created via `docker exec invoice-test-pg psql -U postgres -c
'CREATE DATABASE invoice_test_shadow'` -- it did not already exist and needed manual creation; the
first attempt at the full gate command below hit its default 10-minute `go test` timeout purely from
every integration test in the package retrying a 10-second connection to a database that did not
exist yet, not from any real hang):

```
go build ./...                                                              # exit 0
go vet ./...                                                                # exit 0
go test -p 1 -count=1 -timeout 30m ./cmd/eligibility-shadow/... ./internal/postgresstore/...
```

Both packages `ok`: `invoice-system/backend/cmd/eligibility-shadow` (0.3s, 13 tests including
`TestReportJSONShapeMatchesShadowEvalLibAssumptions`) and `invoice-system/backend/internal/postgresstore`
(128.0s, every pre-existing test in the package plus the four new ones -- no regression).

Two real bugs were caught and fixed by these tests before they passed (not something to silently
gloss over):

- `eligibility_shadow_report_integration_test.go`'s freeze-row insert reused one placeholder
  (`$2`) for both a `uuid` column and a `text` column in the same statement, which Postgres's
  parameter-type inference rejects as "inconsistent types deduced for parameter $2" -- fixed by
  giving the two uses separate placeholders.
- The same file's checkpoint-evaluation fixture used `reconciliation_status='matched'` without
  setting `expected_service_units`/`difference_service_units`, violating
  `balance_reconciliation_checkpoints_check6` (those two columns must be non-NULL for any status
  other than `cutover_baseline`/`pending_finalization`) -- fixed by adding both columns to the insert.

`gofmt -l` against the staged git blobs (this machine's Windows checkout normalizes line endings to
CRLF on disk; gofmt against the raw working-tree file can give a false positive independent of the
content actually committed -- see this session's own toolchain notes) for every new/changed `.go`
file: zero files listed, clean, both for the initial commits and again after adding the shape-pinning
test.

`bash -n` on all three shell scripts: clean.

```
bash deploy/rehearsal/test-shadow-eval.sh
```
prints `argument parsing: ok`, `report comparison logic: ok`, `human summary: ok`,
`test-shadow-eval.sh: all checks passed` (exit 0).

```
pwsh -NoProfile -File scripts/test-release-image-gate.ps1
```
prints `Release image gate offline/static fixtures passed.` (exit 0) -- the Dockerfile COPY-list
change did not break this gate.

```
/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="0177e72..HEAD" .
```
`no leaks found` (run once after the first two commits, and confirmed clean again after the final
commit below).

## Not run

- **A real rehearsal against a real signed backup on the production server.** This session has no
  server access. `shadow-eval.sh`'s restore/decrypt/signature-verification/teardown mechanics closely
  mirror `restore-drill.sh`'s already-production-proven equivalents (same `age`/`ssh-keygen -Y
  verify`/tmpfs-postgres/cleanup-trap patterns, same `docker-cleanup-state.sh` helper), and its own
  argument-parsing paths are covered by `test-shadow-eval.sh`, but the actual restore-into-throwaway-
  container-then-drain-the-queue end-to-end flow has not been exercised against real data. Recommend
  a dry run against the current production backup, on the server, before the RC-plan bullet in section
  11.2 is treated as load-bearing for a real release decision.
- **`scripts/verify.ps1` in full.** The task asked to check its Dockerfile-literal substring
  assertions specifically (confirmed via direct inspection: both required substrings are present and
  unbroken), not to run the whole script, which renders the full production Compose stack against a
  hard-coded `$productionEnv` and would need a production-shaped secrets/environment this worktree
  does not have.
- Integration coverage for the `balance_carry_forward_proofs`/`balance_carry_forward_evaluations`
  branch of `EligibilityShadowSnapshot`'s evaluation-status query (see design decision 6) -- only the
  `balance_checkpoint_evaluations` branch has a seeded integration test; the proof branch's much
  heavier FK chain (`source_economic_scan_cycles`, `source_ingest_batches`) was judged not worth
  fixturing for this secondary reporting feature within this slice's scope. The `UNION ALL` query
  itself is simple enough that this is a real but small gap, not a likely source of a wrong report.

## Risks / things to sign off on

1. `EligibilityProjectionClaimableCount`'s `WHERE` clause is hand-kept in sync with
   `ProcessEligibilityProjectionJobs`' claim-query CTE rather than sharing one source of truth (the
   claim query is a single `UPDATE ... FROM` and cannot itself be reused as a read-only count). A
   future change to the claim query's condition needs the matching change made here too, or
   `queue_drained` could silently stop meaning what this handoff says it means. Flagged prominently in
   both functions' doc comments.
2. Design decision 6 above (partial evaluation-status test coverage).
3. This tool was designed and tested entirely against the AUTOLOGIN-line schema at `0177e72`
   (migrations through `0019_balance_blip_repair.sql`). It calls `migrate.Verify` before doing
   anything else, so a schema mismatch against whatever the candidate release's own migrations bring
   fails closed with a clear error rather than a wrong report -- but this combination (a schema-
   changing RC's projection logic, rehearsed via this tool) has not itself been exercised.
4. No new float amounts, no logged/persisted secrets beyond the throwaway restore-only container
   password already discussed, no `contracts/` changes, no schema/migration changes, no admin-OIDC
   changes, no touch to any file outside this slice's stated scope -- checked.

## Follow-ups (recommended, not blocking)

1. Run one real rehearsal on the server against the current production backup, with a currently-loaded
   RC's own image tag, before treating the section 11.2 RC-plan bullet as load-bearing.
2. If a future slice wants full coverage of the carry-forward-proof evaluation branch, factor out (or
   reuse, if one already exists elsewhere by then) a full `source_economic_scan_cycles`/
   `source_ingest_batches`/`balance_carry_forward_proofs` fixture helper.
3. If `ProcessEligibilityProjectionJobs`' claim-query condition ever changes, update
   `EligibilityProjectionClaimableCount` in the same change (see risk 1).
