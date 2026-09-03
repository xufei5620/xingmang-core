# XM-INV-SHADOW-EVAL: release-rehearsal shadow evaluation of the eligibility projection worker

- **status:** implemented and self-tested locally; gates below. Three real server runs have now
  happened. The first found, and an update fixed, a secret-file permission error in the tools
  container plus an empty/confusing `shadow-eval.json` left behind on that failure path. The second
  (with the permission fix in place) confirmed the credential now reads correctly, and found a second
  real problem -- the restored backup's schema was behind the candidate's own migration set -- fixed
  by adding an `invoice-migrate` step before the projection worker runs. The third (all three earlier
  fixes in place) ran the **entire chain successfully end to end** -- restore, migrate, drain, verdict
  -- but caught a genuine bug: the Go tool's own verdict said `"ready"` while `shadow-eval.sh`'s
  independently recomputed bash verdict said `not_ready`, and the disagreement itself was (correctly,
  per its own design) treated as a rehearsal-tooling failure. Root cause and fix are in this update;
  see "Not run" for the full detail on all three runs. This is the fix the previous two runs were
  waiting to be confirmed by; no outstanding known-fix is unverified as of this update, but a fourth
  real run remains prudent before the rehearsal is treated as fully load-bearing.
- **branch:** `ai/claude/XM-INV-SHADOW-EVAL` (based on `ai/claude/XM-INV-AUTOLOGIN` at `0177e72`),
  worktree `K:/发票/wt-XM-INV-SHADOW-EVAL`.
- **commits:**
  - `6fe4145` feat(eligibility-shadow): add release-rehearsal projection driver and reporting queries
  - `2fb6331` feat(rehearsal): add shadow-eval.sh orchestration and its static test
  - `d1f0468` test(eligibility-shadow): pin the Go/bash report JSON-shape contract
  - `196f810` docs(shadow-eval): production runbook section and handoff
  - `d8e27e2` fix(rehearsal): chown the tools-container secret to its actual runtime uid
  - `fdf3d2f` fix(rehearsal): mark tooling failures explicitly so a verdict can't hide one
  - `81bcbeb` fix(rehearsal): run the candidate's invoice-migrate before the projection worker
  - plus a follow-up commit that fixes the failed_accounts nil-vs-empty-array disagreement (third
    real run's finding) and adds per-reason delta rendering -- this file's own commit is not
    self-referenceable by hash from inside itself; see `git log --oneline 0177e72..HEAD` on this
    branch for the exact, current commit list

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
  `--max-rounds`), snapshot-before/after, JSON report to stdout, human log to stderr, exit code. New,
  from the second real run's fix: `--migrations-applied` (comma-separated, parsed by the new
  `parseMigrationsApplied`) echoed into the report.
- `backend/cmd/eligibility-shadow/report.go` -- `Report`/`Snapshot`/`FreezeCount`/`EvaluationCount`/
  `FailedAccount`/`ProjectionHealth` JSON types, `EvaluateReadiness` (pure comparison logic) and
  `ExitCode`. New: `Report.MigrationsApplied []string`. Fixed (third real run's finding):
  `toReportFailedAccounts` now returns `nil`, not an initialized-but-empty slice, for zero failed
  accounts -- the actual root cause of the Go/bash verdict disagreement, see Summary/Not run.
- `backend/cmd/eligibility-shadow/report_test.go` -- unit tests for `EvaluateReadiness`/`ExitCode`
  (no database) plus `TestReportJSONShapeMatchesShadowEvalLibAssumptions` (the Go↔bash shape
  contract, see design decision 2 above, now also pinning `MigrationsApplied`'s null-vs-populated
  marshaling), three `parseMigrationsApplied` unit tests, and two new regression tests,
  `TestToReportFailedAccountsEmptyInputIsNil`/`TestToReportFailedAccountsPopulatedInput`.
- `backend/internal/postgresstore/eligibility_shadow_report.go` -- `EligibilityShadowSnapshot`,
  `EligibilityProjectionClaimableCount`, `EligibilityShadowFailedJobs`.
- `backend/internal/postgresstore/eligibility_shadow_report_integration_test.go` -- four integration
  tests against a real PostgreSQL instance (account/freeze snapshot correctness, latest-evaluation-only
  counting, claimable-count across five job states, failed-jobs listing).
- `backend/Dockerfile` -- new `invoice-eligibility-shadow` build line and COPY-list entry in the tools
  stage; the two literal substrings `scripts/verify.ps1` checks
  (`/out/invoice-oidc-preflight ./cmd/oidc-preflight` and `/out/invoice-oidc-preflight /usr/local/bin/`)
  are unchanged.
- `deploy/rehearsal/shadow-eval.sh` -- the orchestrator (see Summary). Post-first-real-run fixes:
  `resolve_tools_container_ids` resolves the tools image's runtime uid/gid and the database-url secret
  file/work directory are `chown`ed to it; an empty report is overwritten with an explicit
  `tooling_failure` marker instead of left as a 0-byte file, and `shadow_eval_report_is_valid` is
  checked as a second, independent guard before the verdict logic runs. Post-second-real-run fix: a new
  `invoice-migrate` step (via `shadow_eval_migrate_docker_args`) runs between the restore and
  `invoice-eligibility-shadow`, diffs `schema_migrations` before/after via `docker exec ... psql`
  (reusing the identical technique `restore-drill.sh` already uses for its own migration-state
  comparison), and passes the result as `--migrations-applied`; a new shared
  `write_tooling_failure_marker` function (factored out of the pre-existing empty-report handling)
  writes the same marker for a failed migrate step. See "Not run" below for all three incidents these
  fix.
- `deploy/rehearsal/shadow-eval-lib.sh` -- the bash-side report-comparison/summary functions, sourced
  by both `shadow-eval.sh` and `test-shadow-eval.sh`; also now `shadow_eval_parse_config_user` (the
  pure/testable half of the uid/gid resolution above), `shadow_eval_report_is_valid` (checked by
  both `shadow_eval_verdict_exit_code`, which now returns a distinct `"execution_failure"`/exit 1
  instead of ever computing ready/not_ready from a non-report, and `shadow_eval_human_summary`, which
  renders a clear execution-failure explanation instead of blank/nonsensical freeze counts),
  `shadow_eval_migrate_docker_args` (the testable argument-construction half of the new migrate step),
  and `_shadow_eval_migrations_applied` (rendered into the human summary as "migrations applied").
  Fixed (third real run's finding, defense in depth alongside the Go fix above):
  `shadow_eval_has_errors` now recognizes either the literal `null` or the inlined `[]` for both
  `round_errors` and `failed_accounts`; `round_error_count`/`failed_account_count`
  (`shadow_eval_human_summary`) and `_shadow_eval_migrations_applied`'s opening-bracket markers are now
  anchored to end-of-line, matching `_shadow_eval_freeze_block`'s original hardening, so none of them
  can misread an empty inlined array as unclosed. New: `shadow_eval_freeze_deltas` (per-reason
  before/after/delta rendering, informational only, rendered into `shadow_eval_human_summary` as a new
  "freeze reason deltas" line).
- `deploy/rehearsal/test-shadow-eval.sh` -- static test: every argument-parsing failure path, the
  comparison logic against fixture JSON (including the empty-array edge case), the human summary,
  `shadow_eval_parse_config_user`'s numeric/named/malformed `Config.User` cases,
  `shadow_eval_report_is_valid`/`shadow_eval_verdict_exit_code`/`shadow_eval_human_summary`'s handling
  of a tooling-failure-marker fixture, `shadow_eval_migrate_docker_args`'s exact argument list (in
  particular, that it never emits the `--database-url-file` flag `invoice-migrate` does not support),
  and `migrations_applied` rendering/fallback-to-"none". New: the real RC78 `shadow-eval.json` copied
  in verbatim as fixture `rc78-real-shadow-eval.json` (exit `0`/`"ready"`, delta rendering), plus a
  derived `rc78-real-plus-new-reason-not-ready.json` (exit `3`/`"not_ready"`).
- `docs/PRODUCTION-RUNBOOK.md` -- new section 11.2 (see Summary).
- `docs/handoffs/XM-INV-SHADOW-EVAL.md` -- this document.

**Not touched:** any Sub2API/NewAPI source ([[no-upstream-source-changes]]), `deploy/backup/backup.sh`
or `deploy/backup/restore-drill.sh` (read only, per the task brief), `deploy/docker-compose.prod.yml`
or any other Compose file (this tool never runs via Compose and needs no new service), any migration,
`backend/cmd/api`, `backend/cmd/migrate` (read only -- its connection/mode contract was read and
mirrored exactly by `shadow_eval_migrate_docker_args`, never modified), `backend/internal/migrate`,
`backend/internal/postgresstore/consumption.go` or any other existing evaluator/
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

### Follow-up: the tools-container secret-file permission fix

No Go file changed for this fix (`resolve_tools_container_ids`/`shadow_eval_parse_config_user` and
their `chown` calls are entirely bash), so `go build`/`go vet`/`go test` were not re-run -- there is
nothing in this fix they could catch that `bash -n` and `test-shadow-eval.sh` do not already cover.
Re-ran, on the same worktree, after this fix:

- `bash -n` on all three shell scripts: clean.
- `bash deploy/rehearsal/test-shadow-eval.sh`: `argument parsing: ok`, `report comparison logic: ok`,
  `human summary: ok`, `shadow_eval_parse_config_user: ok` (new: numeric uid:gid, bare uid, root
  uid:gid, empty/named/malformed `Config.User` cases), `test-shadow-eval.sh: all checks passed`
  (exit 0).
- `/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="0177e72..HEAD" .`: `no leaks found`.

### Follow-up: the tooling-failure-marker / verdict-guard fix

Also entirely bash; same reasoning as above for not re-running the Go gates. Re-ran, on the same
worktree, after this fix:

- `bash -n` on all three shell scripts: clean.
- `bash deploy/rehearsal/test-shadow-eval.sh`: `argument parsing: ok`, `report comparison logic: ok`,
  `tooling-failure handling: ok` (new: `shadow_eval_report_is_valid` accepts a genuine report and
  rejects the tooling-failure-marker fixture; `shadow_eval_verdict_exit_code` on that fixture prints
  `execution_failure` and returns exit 1, not 0 or 3; `shadow_eval_human_summary` on it renders a clear
  "EXECUTION FAILURE" explanation naming the log file, and does not render any "open freezes" text),
  `human summary: ok`, `shadow_eval_parse_config_user: ok`, `test-shadow-eval.sh: all checks passed`
  (exit 0).
- `/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="0177e72..HEAD" .`: `no leaks found`.

### Follow-up: the invoice-migrate step

Unlike the two fixes above, this one changes Go code (`Report.MigrationsApplied`,
`parseMigrationsApplied`, the new flag), so the full gate list applies again. From `backend/`, with
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_shadow`:

```
go build ./...                                                              # exit 0
go vet ./...                                                                # exit 0
go test -p 1 -count=1 -timeout 15m ./cmd/eligibility-shadow/... ./internal/postgresstore/...
```

Both packages `ok` (`cmd/eligibility-shadow` 0.25s, including three new `parseMigrationsApplied` tests
and the extended `TestReportJSONShapeMatchesShadowEvalLibAssumptions`; `internal/postgresstore`
109.9s, unaffected by this fix -- no regression). `gofmt -l` against the staged blobs: clean.

- `bash -n` on all three shell scripts: clean.
- `bash deploy/rehearsal/test-shadow-eval.sh`: all prior groups still `ok`, plus new
  `shadow_eval_migrate_docker_args: ok` (asserts the exact argument list, in particular that
  `--database-url-file` -- the flag `invoice-eligibility-shadow` takes but `invoice-migrate` does not
  support at all -- never appears, and that `DATABASE_URL_FILE=/run/secrets/database-url` does) and
  `migrations_applied handling: ok` (extraction from a populated fixture, and fallback to "none" when
  the field is absent/null). `test-shadow-eval.sh: all checks passed` (exit 0).
- `/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="0177e72..HEAD" .`: run after
  committing this fix; see the commit list at the top of this document for confirmation it came back
  clean (this session always re-runs gitleaks after every commit and would not have reported the
  commit id in its reply to the team lead otherwise).

### Follow-up: the failed_accounts nil-vs-empty-array disagreement fix (third real run)

Reproduced first, per the task brief: sourced `deploy/rehearsal/shadow-eval-lib.sh` and called
`shadow_eval_verdict_exit_code` directly against the real `shadow-eval.json` the team lead copied to
`H:\temp\claude\...\scratchpad\rc78-rehearsal\`, confirming `not_ready`/exit 3 against the unmodified
library -- matching the disagreement exactly -- before making any change.

Changes Go code again (`toReportFailedAccounts`), so the full gate list applies:

```
go build ./...                                                              # exit 0
go vet ./...                                                                # exit 0
go test -p 1 -count=1 -timeout 15m ./cmd/eligibility-shadow/... ./internal/postgresstore/...
```

Both packages `ok` (`cmd/eligibility-shadow` includes the two new
`TestToReportFailedAccounts*` regression tests; `internal/postgresstore` ~113s, unaffected -- no
regression). `gofmt -l` against the staged blobs: clean.

- `bash -n` on all three shell scripts: clean.
- Reproduced the fix directly against the real report before touching the test suite: sourced the
  fixed `shadow-eval-lib.sh` and re-ran `shadow_eval_verdict_exit_code`/`shadow_eval_has_errors`/
  `shadow_eval_freeze_deltas`/`shadow_eval_human_summary` against the same real
  `shadow-eval.json` -- now `ready`/exit 0, `has_errors: false`, and the summary renders
  `SOURCE_GAP 79 -> 85 (+6)`.
- `bash deploy/rehearsal/test-shadow-eval.sh`: every prior group still `ok`, plus new
  `RC78 regression: ok` (the real report as a fixture: exit `0`/`"ready"`, no new reasons, `has_errors:
  false`, the rendered `SOURCE_GAP 79 -> 85 (+6)` and an unchanged-reason `(0)` delta line both present;
  a derived variant with one genuinely new freeze reason spliced in via `sed`, independently verified
  to still be valid JSON before use: exit `3`/`"not_ready"`, `new_freeze_reasons` containing exactly
  that one new reason). `test-shadow-eval.sh: all checks passed` (exit 0).
- `/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="0177e72..HEAD" .`: run after
  committing this fix; see the commit list at the top of this document for confirmation.

## Not run

- **A real rehearsal against a real signed backup on the production server, still not completed.**
  This session has no server access. **Update, first real run:** the team lead ran
  `shadow-eval.sh` on the server against the RC77 tools image and the newest signed backup.
  Signature verification, the isolated PostgreSQL container start, and the streamed `age`/`pg_restore`
  all worked correctly -- but the `invoice-eligibility-shadow` tools container then failed immediately:
  `ERROR read database credential error="open /run/secrets/database-url: permission denied"` (exit 1,
  "eligibility-shadow produced no report"). Teardown and the tmpfs shred both worked correctly even on
  this failure path. **Root cause:** the database-url secret file was written root-owned, mode 0400,
  inside a root-owned work directory, and bind-mounted read-only into a container that
  `backend/Dockerfile`'s tools stage runs as a non-root, non-root-owned uid (`USER 10001:10001`) --
  root-only-readable is not readable by that uid. **Fixed** (new commit below):
  `resolve_tools_container_ids` in `shadow-eval.sh` resolves the tools image's actual numeric
  uid/gid (reading `docker image inspect --format '{{.Config.User}}'` when it is already numeric --
  the common case, since the Dockerfile pins it literally -- falling back to asking the image itself
  via `id` when it is empty or a name), refuses to proceed if that resolves to root, `chown`s the
  secret file to that uid before mounting it, and `chown`/`chmod 0710`s the enclosing work directory
  to that gid as defense in depth. The team lead confirmed on the server that
  `docker image inspect invoice-system-tools:0.1.0-rc77` reports `Config.User` as the literal
  `"10001:10001"` (numeric, no container-start fallback needed in practice) -- the id-fallback path is
  kept anyway since it is cheap (one short-lived `--rm` container, only exercised if `Config.User` is
  ever empty or a name) and untested against a real image. The pure uid/gid-parsing half
  (`shadow_eval_parse_config_user` in `shadow-eval-lib.sh`) is now covered by
  `test-shadow-eval.sh`; the `chown`/`chmod` calls themselves and the actual container read are still
  unverified against a real server, so **a second real rehearsal run is still needed** before the
  RC-plan bullet in section 11.2 is treated as load-bearing for a real release decision.
  `shadow-eval.sh`'s restore/decrypt/signature-verification/teardown mechanics otherwise closely mirror
  `restore-drill.sh`'s already-production-proven equivalents (same `age`/`ssh-keygen -Y
  verify`/tmpfs-postgres/cleanup-trap patterns, same `docker-cleanup-state.sh` helper), and this first
  real run's clean signature/restore/teardown behavior is itself evidence for that.

  **Second finding from the same run, also fixed:** the team lead separately noticed that the
  rehearsal directory (`/root/invoice-system/rehearsals/20260903T070721Z-551778/`) still contained a
  `shadow-eval.json` file even though the tool produced no report -- `>"$report_json"` creates the
  redirection target the instant the `docker run` command starts, regardless of what (if anything) it
  writes, so this was an empty (0-byte) file. `shadow-eval.sh`'s own `[[ ! -s "$report_json" ]]` check
  already correctly exited 1 *before* ever calling the summary/verdict functions on it -- so this
  specific run was never at risk of misreporting an infrastructure failure as a verdict -- but an empty
  file left on disk named exactly like a real report is a misleading, confusing artifact on its own,
  and the invariant "a tooling failure can never become a verdict" was previously enforced only by that
  one call-site check, not by the summary/verdict functions themselves. Both are now fixed: (1)
  `shadow-eval.sh` overwrites the empty `report_json` with an explicit
  `{"tooling_failure": true, "reason": ..., "tool_exit_code": ..., "log_file": ...}` marker before
  exiting 1, so the on-disk artifact is self-describing instead of an empty mystery file; (2) a new
  `shadow_eval_report_is_valid` in `shadow-eval-lib.sh` (a report must carry a top-level `"verdict"` key
  and no `"tooling_failure"` marker) is now checked by both `shadow_eval_verdict_exit_code` (returns
  the distinct `"execution_failure"`/exit 1 instead of ever computing ready/not_ready from it) and
  `shadow_eval_human_summary` (prints a clear execution-failure explanation instead of blank/nonsensical
  freeze counts), and `shadow-eval.sh` itself now also checks it explicitly right after the
  structural-JSON sanity check, as a second, independent layer on top of the original emptiness check.
  Covered by three new `test-shadow-eval.sh` cases against a fixture matching this exact marker shape.

  **Second real run, with `d8e27e2`'s scripts:** the team lead ran `shadow-eval.sh` again. The
  permission fix was confirmed: the tools container now reads the credential and connects. It failed
  one step later: `ERROR database migration set mismatch error="required migration
  0020_eligibility_auto_reconcile.sql is not applied"` (exit 1, empty `shadow-eval.json` -- this run
  predates `fdf3d2f`, so the marker logic above was not yet on the server for it to use). **Root
  cause:** the restored production backup is at the *running* release's migration set (through 0019),
  while the candidate tools image (which brings migration 0020) requires that migration to be applied
  before `backend/cmd/eligibility-shadow`'s own `migrate.Verify` precondition passes. This is the
  normal case for any candidate that ships a migration, exactly as `deploy/roll-forward.sh` always runs
  `invoice-migrate` before anything else against real production. **Fixed** (new commit below):
  `shadow-eval.sh` now runs the candidate tools image's `invoice-migrate` entrypoint against the
  isolated database -- same network, same read-only bind-mount of the database-url secret, same
  non-root uid handling -- immediately after the restore and before `invoice-eligibility-shadow`,
  reading `backend/cmd/migrate/main.go`'s actual connection contract exactly (`DATABASE_URL_FILE`
  environment variable; it takes no command-line flags at all, unlike
  `invoice-eligibility-shadow`'s `--database-url-file`), with `APP_ENV`/`MIGRATION_MODE` deliberately
  left unset (skipping the production-only `ELIGIBILITY_START_AT` precondition and selecting the
  default apply -- not verify-only -- mode). It diffs `schema_migrations` before and after that step
  (via `docker exec ... psql`, the same technique `restore-drill.sh` already uses for its own
  migration-state comparison) and passes the newly-applied migration names into
  `invoice-eligibility-shadow`'s new `--migrations-applied` flag, which the report now carries as a
  top-level `migrations_applied` field (`null`/"none" when the backup was already current) -- so the
  verdict is explicitly "candidate schema + candidate evaluator against production data". A failed
  migrate step writes the same `tooling_failure` marker (fixed above) and exits 1, never a verdict. The
  argument-construction half (`shadow_eval_migrate_docker_args`) and the new report field are both
  covered by new `test-shadow-eval.sh`/Go test cases; the actual `invoice-migrate` invocation against a
  real schema mismatch is still unverified against a real server, so **a third real rehearsal run is
  still needed** before the RC-plan bullet in section 11.2 is treated as load-bearing.

  **Third real run, with `81bcbeb`'s scripts (RC78 tools image, all three fixes so far):** the entire
  chain worked end to end -- signed backup restore, `invoice-migrate` applied
  `0020_eligibility_auto_reconcile.sql`, `invoice-eligibility-shadow` drained the queue in 1 round with
  0 round errors and 0 failed accounts, and its own process verdict was `"ready"` (open freezes before
  `SOURCE_GAP=79`/`UNKNOWN_NEGATIVE_BALANCE=158`/`USAGE_EXCEEDS_LEDGER=4`, after `SOURCE_GAP=85`, the
  other two unchanged -- `SOURCE_GAP` growing from ordinary source-stream lag, not a new category).
  But `shadow-eval.sh` then printed `bash-recomputed verdict (not_ready) disagrees with the tool's own
  verdict (ready) -- treating this as a rehearsal-tooling failure` and exited 1. The team lead copied
  the real artifacts (`shadow-eval.json`, `shadow-eval-summary.txt`, `eligibility-shadow.log`,
  `invoice-migrate.log`) locally for reproduction.

  **Root cause, reproduced locally** by sourcing `shadow-eval-lib.sh` and calling
  `shadow_eval_verdict_exit_code` directly against the real `shadow-eval.json`: the report contained
  `"failed_accounts": []` -- an empty, but **non-nil**, array -- not the literal `null`.
  `shadow_eval_has_errors` checked only for the literal `"failed_accounts": null`, so it read this as
  "there is a failure" and returned `true`, which `shadow_eval_verdict_exit_code` then turned into
  `not_ready`. Tracing further found the actual bug was on the **Go side**:
  `toReportFailedAccounts` (`report.go`) unconditionally built its result via
  `make([]FailedAccount, 0, len(failed))`, which is a non-nil slice even when `failed` has zero
  elements -- unlike `RoundErrors`, which is only ever `append`ed to and so stays genuinely `nil`
  until something is added. `json.MarshalIndent` renders a non-nil empty slice as the inline `[]` and
  a nil slice as `null` -- two different, both individually reasonable-looking, JSON shapes for "no
  failures", and the bash side only recognized one of them.

  **Fixed**, on both sides, so the two independent implementations agree on the one documented
  definition ("a candidate is `ready` iff there are no new freeze-reason categories versus the
  post-restore baseline and no projection errors; per-reason count deltas are informational and do
  not change the verdict"), not just patched around each other's symptom:
  - **Go (the actual root cause):** `toReportFailedAccounts` now returns `nil`, not
    `make([]FailedAccount, 0, 0)`, when its input is empty -- bringing it in line with the
    nil-means-none contract every other optional `[]T` report field (`RoundErrors`,
    `MigrationsApplied`) already follows. New regression tests:
    `TestToReportFailedAccountsEmptyInputIsNil` (both a nil and an empty-non-nil input must produce
    nil output) and `TestToReportFailedAccountsPopulatedInput` (a real entry still round-trips
    correctly).
  - **Bash (defense in depth, so a future regression in either implementation alone cannot repeat
    this):** `shadow_eval_has_errors` now recognizes either the literal `null` or the inlined `[]` for
    both `round_errors` and `failed_accounts`. Two further latent bugs of the identical shape, caught
    while auditing every other place this file parses a `[`-prefixed key, were fixed alongside it even
    though neither had actually misfired yet: `shadow_eval_human_summary`'s `round_error_count`/
    `failed_account_count` awk extraction and `_shadow_eval_migrations_applied` all matched their
    opening-bracket marker as a bare substring (`/"key": \[/`) rather than anchored to end-of-line
    (`/"key": \[$/`) -- meaning an empty inlined `"key": [],` would have been misread as an *unclosed*
    array, silently consuming lines until the next unrelated closing-bracket-shaped line was found
    (exactly the mechanism `_shadow_eval_freeze_block` was already hardened against, from the start,
    for `open_freezes_by_reason` -- this pass brings the other three array-parsing sites to the same
    standard).
  - **New: per-reason delta rendering**, per the task brief's explicit ask that a per-reason count
    change stay informational, visible, and clearly not a verdict factor.
    `shadow_eval_freeze_deltas` prints one line per freeze reason appearing in either snapshot
    ("`REASON before -> after (+delta)`", e.g. `SOURCE_GAP 79 -> 85 (+6)`), rendered into
    `shadow_eval_human_summary` as a new "freeze reason deltas (informational, does not affect the
    verdict)" line and into `docs/PRODUCTION-RUNBOOK.md` section 11.2's field description.
  - **Regression coverage using the real report:** the real `shadow-eval.json` from this run (only
    UUIDs and counts, nothing sensitive) is copied verbatim into `test-shadow-eval.sh` as fixture
    `rc78-real-shadow-eval.json` -- exercising the actual JSON layout the Go program produces, not an
    idealized hand-written one -- asserting exit `0`/`"ready"` and the rendered `SOURCE_GAP 79 -> 85
    (+6)` delta text. A second fixture, `rc78-real-plus-new-reason-not-ready.json`, is the same real
    report with one genuinely new freeze-reason category spliced in (`sed`, verified to produce valid
    JSON), asserting exit `3`/`"not_ready"` -- so a real "everything is fine" report and a real report
    with one real regression both take the exact path they should.

  This closes every fix this rehearsal's development runs have surfaced so far; a fourth real run is
  still recommended (see Follow-ups) before treating it as fully load-bearing, but there is no
  currently-known unverified fix outstanding.
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
3. **Resolved and confirmed by the second and third real runs.** This tool was designed and tested
   entirely against the AUTOLOGIN-line schema at `0177e72` (migrations through
   `0019_balance_blip_repair.sql`). Originally this risk noted that `backend/cmd/eligibility-shadow`'s
   own `migrate.Verify` precondition would fail closed on a schema mismatch rather than produce a wrong
   report -- a real production run (the second) confirmed exactly that failure mode (`required
   migration 0020_eligibility_auto_reconcile.sql is not applied`), which turned out to be the
   *expected*, not exceptional, case: the restored backup is normally behind the candidate's migration
   set, since the candidate has not shipped yet. `shadow-eval.sh` was then fixed to run the candidate's
   own `invoice-migrate` first (see Summary), and the third real run confirmed the fix directly:
   `invoice-migrate` applied `0020_eligibility_auto_reconcile.sql` and `migrate.Verify` passed, so
   `invoice-eligibility-shadow` proceeded normally. No longer an open risk for this specific migration;
   a future RC bringing its own new migration exercises the same already-proven path.
4. No new float amounts, no logged/persisted secrets beyond the throwaway restore-only container
   password already discussed, no `contracts/` changes, no schema/migration changes, no admin-OIDC
   changes, no touch to any file outside this slice's stated scope -- checked.
5. **New, from the first real run's fix.** `resolve_tools_container_ids`' `chown` calls (both the
   secret file and the work directory) require the whole script to run as root on the server, same as
   every other privileged operation it already does (the tmpfs `mount`, `restore-drill.sh`'s own
   `chown -R 65532:65532` calls on its analogous per-container key copies). Not a new assumption, but
   worth calling out since it is the first place *this* script relies on it for a `chown` specifically.
   If `Config.User` on a future tools image is ever a bare name instead of numeric, the fallback path
   (`docker run --rm --entrypoint id <image> -u/-g`) starts one extra short-lived container per
   rehearsal to answer that -- confirmed on the server (`docker image inspect
   invoice-system-tools:0.1.0-rc77`) that the current image never takes this path, `Config.User` is
   literally `10001:10001`; the fallback itself is still untested against a real image, only
   unit-tested for its numeric-parsing half.
6. **New, from the tooling-failure-marker fix.** The `log_file` value embedded in the
   `{"tooling_failure": true, ...}` marker JSON (`shadow-eval.sh`) is not escaped for arbitrary
   characters -- it is always a path this script itself constructed
   (`$rehearsal_root/$stamp/eligibility-shadow.log`, where `$stamp` is a `date`/`$$`-derived value this
   script controls), so this is safe under the same "operator controls their own environment, not
   adversarial" trust model the rest of this tool already assumes for path inputs (e.g.
   `BACKUP_DIR`/`REHEARSAL_ROOT`), but would need proper JSON string escaping if this marker's shape
   is ever extended to embed less-trusted text (such as raw program output) directly.
7. **New, from the invoice-migrate fix.** `migrations_applied_csv` is computed by `comm -13` over two
   `schema_migrations` snapshots, joined with `paste -sd ',' -`; if a migration file name ever
   legitimately contained a comma (none do today -- they are fixed, reviewed, numbered `.sql` file
   names) it would corrupt the CSV join and, downstream, `_shadow_eval_migrations_applied`'s
   line-per-element parsing. Not enforced by any check in this rehearsal itself; relies on the existing
   migration-file-naming convention holding. `shadow_eval_migrate_docker_args`'s `chown`/non-root
   requirements are the same as `resolve_tools_container_ids`' (risk 5) since it reuses the identical
   secret file. The `invoice-migrate` invocation itself (as opposed to its argument construction) has
   now been confirmed against a real schema mismatch by the third real run.
8. **New, from the third real run's fix.** The nil-vs-empty-array disagreement was fixed at its actual
   root (Go) plus, as defense in depth, in bash -- but this pattern (a `[]T` field that is documented to
   be "nil means none" yet gets built via an unconditional `make([]T, 0, n)`) is a class of bug, not a
   one-off: any *future* field added to `Report` with the same intended contract needs the same
   nil-when-empty care in its own conversion function, and `TestReportJSONShapeMatchesShadowEvalLibAssumptions`
   only pins the marshaling shape of fields it already knows about -- it cannot catch a same-shaped bug
   in a field added later unless that field's own test is added too (as
   `TestToReportFailedAccountsEmptyInputIsNil` now does for this one). No automated check enforces this
   convention across all current and future fields at once.

## Follow-ups (recommended, not blocking)

1. Run a fourth real rehearsal on the server (after all four fixes so far -- the permission fix, the
   tooling-failure-marker/verdict-guard fix, the invoice-migrate step, and the
   nil-vs-empty-array/delta-rendering fix) before treating the section 11.2 RC-plan bullet as fully
   load-bearing. Three consecutive real runs have each found and had fixed a distinct real bug; a clean
   fourth run would be the first evidence the chain is actually stable end to end.
2. If a future slice wants full coverage of the carry-forward-proof evaluation branch, factor out (or
   reuse, if one already exists elsewhere by then) a full `source_economic_scan_cycles`/
   `source_ingest_batches`/`balance_carry_forward_proofs` fixture helper.
3. If `ProcessEligibilityProjectionJobs`' claim-query condition ever changes, update
   `EligibilityProjectionClaimableCount` in the same change (see risk 1).
4. If a future field is added to `Report` with a "nil means none" contract (matching `RoundErrors`/
   `FailedAccounts`/`MigrationsApplied`), add its own empty-input regression test at the same time (see
   risk 8) -- do not rely solely on the shape-pinning test noticing after the fact.
