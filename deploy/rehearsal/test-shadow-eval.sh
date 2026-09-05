#!/usr/bin/env bash
# test-shadow-eval.sh -- static tests for deploy/rehearsal/shadow-eval.sh and
# shadow-eval-lib.sh. Exercises argument parsing (every path that fails
# before shadow-eval.sh ever calls Docker) and the bash-side report
# comparison logic against fixture JSON. Starts no Docker container/network
# and needs no database.
set -Eeuo pipefail
umask 077

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
shadow_eval="$script_dir/shadow-eval.sh"
# shellcheck source=deploy/rehearsal/shadow-eval-lib.sh
source "$script_dir/shadow-eval-lib.sh"

failures=0
fail() {
  failures=$((failures+1))
  echo "FAIL: $1" >&2
}

fixtures_dir=$(mktemp -d)
cleanup() { rm -rf -- "$fixtures_dir"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Argument parsing: every one of these must fail (exit 2) or, for --help,
# succeed (exit 0) before shadow-eval.sh reaches its `for command in age
# docker ...` availability check, so none of these need Docker/age/etc.
# installed. Real BACKUP_DIR/BACKUP_ALLOWED_SIGNERS_FILE/AGE_IDENTITY_FILE
# are deliberately left unset for the flag-validation cases: the flag check
# happens first and must reject before those env vars are ever consulted.
# ---------------------------------------------------------------------------

assert_exit() {
  local expected=$1 label=$2
  shift 2
  local actual
  set +e
  ( unset BACKUP_DIR BACKUP_ALLOWED_SIGNERS_FILE AGE_IDENTITY_FILE REHEARSAL_ROOT
    bash "$shadow_eval" "$@" >/dev/null 2>&1 )
  actual=$?
  set -e
  if [[ "$actual" != "$expected" ]]; then
    fail "$label: expected exit $expected, got $actual"
  fi
}

assert_exit 2 "missing --image-tag"
assert_exit 2 "unknown flag" --bogus-flag
assert_exit 2 "--image-tag missing value" --image-tag
assert_exit 2 "--image-tag wrong shape (no rc)" --image-tag 0.1.0
assert_exit 2 "--image-tag wrong shape (uppercase RC)" --image-tag 0.1.0-RC5
assert_exit 2 "--image-tag wrong shape (extra text)" --image-tag 0.1.0-rc5-signed
assert_exit 2 "--max-rounds zero" --image-tag 0.1.0-rc5 --max-rounds 0
assert_exit 2 "--max-rounds non-numeric" --image-tag 0.1.0-rc5 --max-rounds abc
assert_exit 2 "--max-rounds too large" --image-tag 0.1.0-rc5 --max-rounds 99999
assert_exit 2 "--batch-limit zero" --image-tag 0.1.0-rc5 --batch-limit 0
assert_exit 2 "--batch-limit too large" --image-tag 0.1.0-rc5 --batch-limit 100
assert_exit 2 "--evidence-batch-limit non-numeric" --image-tag 0.1.0-rc5 --evidence-batch-limit abc
assert_exit 2 "--evidence-batch-limit too large" --image-tag 0.1.0-rc5 --evidence-batch-limit 100000
assert_exit 2 "--evidence-batch-limit missing value" --image-tag 0.1.0-rc5 --evidence-batch-limit
# A well-formed flag set passes argument validation and stops at the first
# required-environment check, whose `:?` expansion exits 1, not 2.
assert_exit 1 "--reevaluate-evidence is parsed and stops at the env checks" --image-tag 0.1.0-rc5 --reproject-all --reevaluate-evidence
# An explicit --backup's shape is validated with the other flags -- before
# any environment or tool-availability check -- specifically so this needs
# neither BACKUP_DIR nor age/docker/etc. installed.
assert_exit 2 "--backup wrong shape" --image-tag 0.1.0-rc5 --backup not-a-backup-name
assert_exit 0 "--help" --help

# A well-formed --image-tag with BACKUP_DIR unset must fail on the missing
# env var (bash's ${VAR:?msg} expansion), not silently pass.
assert_exit 1 "missing BACKUP_DIR env" --image-tag 0.1.0-rc5

echo "argument parsing: ok"

# ---------------------------------------------------------------------------
# Report comparison logic, against fixture JSON matching
# backend/cmd/eligibility-shadow's exact json.MarshalIndent(report, "", "  ")
# output shape (2-space indent, Go struct field order, an empty slice
# inlined as "[]" and a populated one always expanded one element per line).
# See shadow-eval-lib.sh's header comment for why this repo hand-parses that
# fixed shape instead of depending on a JSON library.
# ---------------------------------------------------------------------------

write_fixture() {
  local name=$1
  cat >"$fixtures_dir/$name"
}

# No change at all: both snapshots empty, no errors -> ready.
write_fixture no-freezes-ready.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "max_rounds": 200,
  "rounds_run": 0,
  "queue_drained": true,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "ready",
  "verdict_reason": "no new freeze reason categories and no projection errors versus the post-restore baseline"
}
JSON

# Identical non-empty reason set before/after -> ready (unchanged, not new).
write_fixture same-reason-ready.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "max_rounds": 200,
  "rounds_run": 2,
  "queue_drained": true,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 3
      }
    ],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 40
      }
    ],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "ready",
  "verdict_reason": "no new freeze reason categories and no projection errors versus the post-restore baseline"
}
JSON

# A new freeze-reason category appears after the candidate ran -> not_ready.
write_fixture new-reason-not-ready.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "max_rounds": 200,
  "rounds_run": 3,
  "queue_drained": true,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 3
      }
    ],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 3
      },
      {
        "freeze_reason": "UNKNOWN_NEGATIVE_BALANCE",
        "open": 1
      }
    ],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": [
    "UNKNOWN_NEGATIVE_BALANCE"
  ],
  "has_projection_errors": false,
  "verdict": "not_ready",
  "verdict_reason": "new freeze reason categories appeared after the candidate ran"
}
JSON

# Round errors alone (no new freeze reason) -> not_ready.
write_fixture round-errors-not-ready.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "max_rounds": 200,
  "rounds_run": 1,
  "queue_drained": false,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 1,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": [
    "eligibility projection job failed: some transient error"
  ],
  "new_freeze_reasons": null,
  "has_projection_errors": true,
  "verdict": "not_ready",
  "verdict_reason": "the candidate produced projection errors"
}
JSON

# Failed accounts alone (no round_errors, no new reasons) -> not_ready.
write_fixture failed-accounts-not-ready.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "max_rounds": 200,
  "rounds_run": 5,
  "queue_drained": true,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 1,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": [
    {
      "external_account_id": "acct-1",
      "last_error_code": "PROJECTION_FAILED",
      "attempt_count": 4,
      "updated_at": "2026-09-03T12:05:00Z"
    }
  ],
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": true,
  "verdict": "not_ready",
  "verdict_reason": "the candidate produced projection errors"
}
JSON

# A tooling/execution failure -- not a real report at all. Matches
# shadow-eval.sh's exact literal shape when invoice-eligibility-shadow
# produces nothing (see its own comment on that write).
write_fixture tooling-failure.json <<'JSON'
{
  "tooling_failure": true,
  "reason": "eligibility-shadow produced no report",
  "tool_exit_code": 1,
  "log_file": "/root/invoice-system/rehearsals/20260903T070721Z-551778/eligibility-shadow.log"
}
JSON

assert_new_reasons() {
  local file=$1 expected=$2 label=$3
  local actual
  actual=$(shadow_eval_new_freeze_reasons "$fixtures_dir/$file" | paste -sd ',' -)
  if [[ "$actual" != "$expected" ]]; then
    fail "$label: expected new reasons '$expected', got '$actual'"
  fi
}

assert_verdict() {
  local file=$1 expected_verdict=$2 expected_exit=$3 label=$4
  local actual_verdict actual_exit
  set +e
  actual_verdict=$(shadow_eval_verdict_exit_code "$fixtures_dir/$file")
  actual_exit=$?
  set -e
  if [[ "$actual_verdict" != "$expected_verdict" || "$actual_exit" != "$expected_exit" ]]; then
    fail "$label: expected verdict=$expected_verdict exit=$expected_exit, got verdict=$actual_verdict exit=$actual_exit"
  fi
}

assert_has_errors() {
  local file=$1 expected=$2 label=$3
  local actual
  actual=$(shadow_eval_has_errors "$fixtures_dir/$file")
  if [[ "$actual" != "$expected" ]]; then
    fail "$label: expected has_errors=$expected, got $actual"
  fi
}

assert_new_reasons no-freezes-ready.json "" "empty before/after (inline [])"
assert_verdict no-freezes-ready.json ready 0 "empty before/after (inline [])"
assert_has_errors no-freezes-ready.json false "empty before/after (inline [])"

assert_new_reasons same-reason-ready.json "" "same reason, growing count"
assert_verdict same-reason-ready.json ready 0 "same reason, growing count"

assert_new_reasons new-reason-not-ready.json "UNKNOWN_NEGATIVE_BALANCE" "new reason appears"
assert_verdict new-reason-not-ready.json not_ready 3 "new reason appears"

assert_new_reasons round-errors-not-ready.json "" "round errors alone"
assert_has_errors round-errors-not-ready.json true "round errors alone"
assert_verdict round-errors-not-ready.json not_ready 3 "round errors alone"

assert_new_reasons failed-accounts-not-ready.json "" "failed accounts alone"
assert_has_errors failed-accounts-not-ready.json true "failed accounts alone"
assert_verdict failed-accounts-not-ready.json not_ready 3 "failed accounts alone"

echo "report comparison logic: ok"

# ---------------------------------------------------------------------------
# A tooling/execution failure must never be folded into "ready" (0) or
# "not_ready" (3) -- shadow_eval_report_is_valid rejects it, and
# shadow_eval_verdict_exit_code must report a distinct "execution_failure"
# outcome (exit 1) instead of computing anything from its (absent)
# before/after freeze data. This is the exact regression a real production
# run surfaced: the tools container failed before printing anything, and
# an earlier version of this tooling did not clearly distinguish that from
# a real verdict.
# ---------------------------------------------------------------------------
if shadow_eval_report_is_valid "$fixtures_dir/no-freezes-ready.json"; then :; else
  fail "shadow_eval_report_is_valid rejected a genuine valid report"
fi
if shadow_eval_report_is_valid "$fixtures_dir/tooling-failure.json"; then
  fail "shadow_eval_report_is_valid accepted a tooling-failure marker as a genuine report"
fi

set +e
verdict_output=$(shadow_eval_verdict_exit_code "$fixtures_dir/tooling-failure.json")
verdict_exit=$?
set -e
if [[ "$verdict_output" != "execution_failure" || "$verdict_exit" != 1 ]]; then
  fail "tooling-failure fixture: expected output=execution_failure exit=1, got output='$verdict_output' exit=$verdict_exit"
fi

failure_summary=$(shadow_eval_human_summary "$fixtures_dir/tooling-failure.json")
for needle in "EXECUTION FAILURE" "eligibility-shadow produced no report" "eligibility-shadow.log"; do
  if [[ "$failure_summary" != *"$needle"* ]]; then
    fail "tooling-failure human summary missing expected text: $needle"
  fi
done
if [[ "$failure_summary" == *"open freezes"* ]]; then
  fail "tooling-failure human summary should not render freeze/error counts from a non-report"
fi
echo "tooling-failure handling: ok"

# ---------------------------------------------------------------------------
# Human summary: must not crash and must mention the key facts.
# ---------------------------------------------------------------------------
summary=$(shadow_eval_human_summary "$fixtures_dir/new-reason-not-ready.json")
for needle in "invoice-20260903T011358Z" "0.1.0-rc77" "UNKNOWN_NEGATIVE_BALANCE" "SOURCE_GAP=3"; do
  if [[ "$summary" != *"$needle"* ]]; then
    fail "human summary missing expected text: $needle"
  fi
done
echo "human summary: ok"

# ---------------------------------------------------------------------------
# shadow_eval_parse_config_user: the statically-testable half of resolving
# the tools container's runtime uid/gid for the database-url secret file
# (see shadow-eval.sh's resolve_tools_container_ids and its own comments --
# a real production run hit "permission denied" reading that secret before
# this resolution existed, because the file was root-owned while the tools
# container runs as a non-root, non-root-owned uid).
# ---------------------------------------------------------------------------
assert_parse_config_user() {
  local input=$1 expected_exit=$2 expected_output=$3 label=$4
  local actual_output actual_exit
  set +e
  actual_output=$(shadow_eval_parse_config_user "$input")
  actual_exit=$?
  set -e
  if [[ "$actual_exit" != "$expected_exit" || "$actual_output" != "$expected_output" ]]; then
    fail "$label: expected exit=$expected_exit output='$expected_output', got exit=$actual_exit output='$actual_output'"
  fi
}

# The real, current backend/Dockerfile pin for the tools stage.
assert_parse_config_user "10001:10001" 0 "10001 10001" "uid:gid pin (the real Dockerfile value)"
assert_parse_config_user "10001" 0 "10001 10001" "bare uid, gid defaults to uid"
assert_parse_config_user "0:0" 0 "0 0" "root uid:gid (caller must separately refuse this)"
assert_parse_config_user "0" 0 "0 0" "bare root uid"
assert_parse_config_user "" 1 "" "empty Config.User (defaults to root) must not be treated as numeric"
assert_parse_config_user "app" 1 "" "named user must fall back to asking the image"
assert_parse_config_user "10001:" 1 "" "trailing colon with no group must not parse"
assert_parse_config_user ":10001" 1 "" "leading colon with no uid must not parse"
assert_parse_config_user "10001:app" 1 "" "numeric uid with a named group must not parse"
echo "shadow_eval_parse_config_user: ok"

# ---------------------------------------------------------------------------
# shadow_eval_migrate_docker_args: the testable argument-construction half of
# the invoice-migrate step (XM-INV-SHADOW-EVAL migration-set fix -- a real
# production run hit "required migration 0020_eligibility_auto_reconcile.sql
# is not applied" because the restored backup was still at the running
# release's schema). backend/cmd/migrate/main.go takes no command-line flags
# at all: its connection comes from the DATABASE_URL_FILE environment
# variable, unlike invoice-eligibility-shadow's --database-url-file flag --
# the single most likely copy-paste mistake this test guards against.
# ---------------------------------------------------------------------------
mapfile -t migrate_args < <(shadow_eval_migrate_docker_args "invoice-shadow-fixture-net" "invoice-system-tools:0.1.0-rc77" "/work/database-url")
migrate_args_joined=$(printf '\x1f%s' "${migrate_args[@]}")$'\x1f'

assert_migrate_arg_present() {
  local expected=$1 label=$2
  if [[ "$migrate_args_joined" != *$'\x1f'"$expected"$'\x1f'* ]]; then
    fail "shadow_eval_migrate_docker_args: missing expected argument '$expected' ($label)"
  fi
}
assert_migrate_arg_absent() {
  local unexpected=$1 label=$2
  if [[ "$migrate_args_joined" == *"$unexpected"* ]]; then
    fail "shadow_eval_migrate_docker_args: unexpectedly present '$unexpected' ($label)"
  fi
}

assert_migrate_arg_present "--pull" "no accidental image pull"
assert_migrate_arg_present "--rm" "cleans itself up"
assert_migrate_arg_present "--network" "network flag"
assert_migrate_arg_present "invoice-shadow-fixture-net" "the given network value"
assert_migrate_arg_present "--read-only" "read-only root filesystem"
assert_migrate_arg_present "--cap-drop" "cap-drop flag"
assert_migrate_arg_present "ALL" "drops every capability"
assert_migrate_arg_present "--security-opt" "security-opt flag"
assert_migrate_arg_present "no-new-privileges:true" "no privilege escalation"
assert_migrate_arg_present "--env" "env flag for the connection"
assert_migrate_arg_present "DATABASE_URL_FILE=/run/secrets/database-url" "invoice-migrate reads its connection from exactly this env var (backend/cmd/migrate/main.go)"
assert_migrate_arg_present "--entrypoint" "entrypoint flag"
assert_migrate_arg_present "/usr/local/bin/invoice-migrate" "the migrate binary, not eligibility-shadow"
assert_migrate_arg_present "invoice-system-tools:0.1.0-rc77" "the candidate tools image"
assert_migrate_arg_present "type=bind,src=/work/database-url,dst=/run/secrets/database-url,readonly" "reuses the same secret bind-mount as invoice-eligibility-shadow"

assert_migrate_arg_absent "--database-url-file" "invoice-migrate takes no command-line flags at all -- unlike invoice-eligibility-shadow"
assert_migrate_arg_absent "MIGRATION_MODE" "must not set MIGRATION_MODE=verify -- the rehearsal needs migrate.Up (apply), not the read-only verify mode"
assert_migrate_arg_absent "APP_ENV" "must not set APP_ENV=production -- this is a throwaway rehearsal database, and production mode enforces a strict ELIGIBILITY_START_AT precondition"
echo "shadow_eval_migrate_docker_args: ok"

# ---------------------------------------------------------------------------
# migrations_applied: the report field recording what shadow-eval.sh's
# invoice-migrate step newly applied (or "none").
# ---------------------------------------------------------------------------
write_fixture migrations-applied-populated.json <<'JSON'
{
  "generated_at": "2026-09-03T12:00:00Z",
  "backup_label": "invoice-20260903T011358Z",
  "candidate_image_tag": "0.1.0-rc77",
  "migrations_applied": [
    "0020_eligibility_auto_reconcile.sql"
  ],
  "max_rounds": 200,
  "rounds_run": 0,
  "queue_drained": true,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "ready",
  "verdict_reason": "no new freeze reason categories and no projection errors versus the post-restore baseline"
}
JSON

migrations_applied_extracted=$(_shadow_eval_migrations_applied "$fixtures_dir/migrations-applied-populated.json" | paste -sd ',' -)
if [[ "$migrations_applied_extracted" != "0020_eligibility_auto_reconcile.sql" ]]; then
  fail "_shadow_eval_migrations_applied: expected the one populated migration name, got '$migrations_applied_extracted'"
fi
migrations_summary=$(shadow_eval_human_summary "$fixtures_dir/migrations-applied-populated.json")
if [[ "$migrations_summary" != *"migrations applied:  0020_eligibility_auto_reconcile.sql"* ]]; then
  fail "human summary did not render the applied migration name"
fi

# A report with migrations_applied absent entirely (an older-shaped fixture,
# and also the null case) must render "none", not crash or print blank.
none_extracted=$(_shadow_eval_migrations_applied "$fixtures_dir/no-freezes-ready.json" | paste -sd ',' -)
if [[ -n "$none_extracted" ]]; then
  fail "_shadow_eval_migrations_applied: expected nothing extracted when the key is absent, got '$none_extracted'"
fi
none_summary=$(shadow_eval_human_summary "$fixtures_dir/no-freezes-ready.json")
if [[ "$none_summary" != *"migrations applied:  none"* ]]; then
  fail "human summary did not fall back to 'none' when no migrations were applied"
fi
echo "migrations_applied handling: ok"

# ---------------------------------------------------------------------------
# Regression test for the exact disagreement a real production run (RC78)
# hit: the whole restore/migrate/drain chain succeeded, the tool's own
# ExitCode said "ready", but shadow-eval.sh's independently recomputed bash
# verdict said "not_ready" and treated the disagreement as a
# rehearsal-tooling failure. Root cause: "failed_accounts": [] (an empty,
# non-nil array -- toReportFailedAccounts' own bug, since fixed) was not
# recognized as "no failures" by shadow_eval_has_errors, which checked only
# for the literal `null`. This is the real captured report (only UUIDs and
# counts; nothing sensitive) -- copied verbatim, not hand-simplified, so this
# test exercises the actual JSON layout the Go program produces, not an
# idealized fixture.
# ---------------------------------------------------------------------------
write_fixture rc78-real-shadow-eval.json <<'JSON'
{
  "generated_at": "2026-09-03T08:26:23.060830505Z",
  "backup_label": "invoice-20260903T050943Z",
  "candidate_image_tag": "0.1.0-rc78",
  "migrations_applied": [
    "0020_eligibility_auto_reconcile.sql"
  ],
  "max_rounds": 200,
  "rounds_run": 1,
  "queue_drained": true,
  "before": {
    "accounts": [
      {
        "external_account_id": "058bcc7a-b48b-4b6c-99cb-e96894d20736",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "40bd883d-26fa-4938-b8c8-0f51c8b88686",
        "eligibility_status": "frozen",
        "open_freezes": 159
      },
      {
        "external_account_id": "561ea459-1011-42e2-b882-4f75542eda4e",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "6706ea6a-c3c1-4dd6-945a-f7517f8be781",
        "eligibility_status": "frozen",
        "open_freezes": 13
      },
      {
        "external_account_id": "98cce4c8-a03c-4b55-9049-61b650db2d0e",
        "eligibility_status": "frozen",
        "open_freezes": 68
      },
      {
        "external_account_id": "acdcdce9-c7f4-4cb4-9a02-ce527849a440",
        "eligibility_status": "frozen",
        "open_freezes": 1
      },
      {
        "external_account_id": "bcceeda6-b221-4389-b5d4-45e14c4fead2",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "f2c6515c-208c-4831-9abd-17b9d092a674",
        "eligibility_status": "active",
        "open_freezes": 0
      }
    ],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 79
      },
      {
        "freeze_reason": "UNKNOWN_NEGATIVE_BALANCE",
        "open": 158
      },
      {
        "freeze_reason": "USAGE_EXCEEDS_LEDGER",
        "open": 4
      }
    ],
    "evaluations_by_status": [
      {
        "evaluation_status": "matched",
        "count": 1800
      },
      {
        "evaluation_status": "negative_frozen",
        "count": 157
      },
      {
        "evaluation_status": "positive_blip_ignored",
        "count": 8
      },
      {
        "evaluation_status": "positive_classified_non_cash",
        "count": 6
      },
      {
        "evaluation_status": "source_gap_frozen",
        "count": 79
      }
    ]
  },
  "before_projection_health": {
    "queued": 1,
    "failed": 1,
    "processing": 0,
    "oldest_pending": "2026-09-03T05:09:43.950097Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [
      {
        "external_account_id": "058bcc7a-b48b-4b6c-99cb-e96894d20736",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "40bd883d-26fa-4938-b8c8-0f51c8b88686",
        "eligibility_status": "frozen",
        "open_freezes": 159
      },
      {
        "external_account_id": "561ea459-1011-42e2-b882-4f75542eda4e",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "6706ea6a-c3c1-4dd6-945a-f7517f8be781",
        "eligibility_status": "frozen",
        "open_freezes": 13
      },
      {
        "external_account_id": "98cce4c8-a03c-4b55-9049-61b650db2d0e",
        "eligibility_status": "frozen",
        "open_freezes": 74
      },
      {
        "external_account_id": "acdcdce9-c7f4-4cb4-9a02-ce527849a440",
        "eligibility_status": "frozen",
        "open_freezes": 1
      },
      {
        "external_account_id": "bcceeda6-b221-4389-b5d4-45e14c4fead2",
        "eligibility_status": "active",
        "open_freezes": 0
      },
      {
        "external_account_id": "f2c6515c-208c-4831-9abd-17b9d092a674",
        "eligibility_status": "active",
        "open_freezes": 0
      }
    ],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 85
      },
      {
        "freeze_reason": "UNKNOWN_NEGATIVE_BALANCE",
        "open": 158
      },
      {
        "freeze_reason": "USAGE_EXCEEDS_LEDGER",
        "open": 4
      }
    ],
    "evaluations_by_status": [
      {
        "evaluation_status": "matched",
        "count": 1800
      },
      {
        "evaluation_status": "negative_frozen",
        "count": 157
      },
      {
        "evaluation_status": "positive_blip_ignored",
        "count": 26
      },
      {
        "evaluation_status": "positive_classified_non_cash",
        "count": 6
      },
      {
        "evaluation_status": "source_gap_frozen",
        "count": 85
      }
    ]
  },
  "after_projection_health": {
    "queued": 1,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 1,
    "oldest_proof_pending": "2026-09-03T08:26:23.076659Z"
  },
  "failed_accounts": [],
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "ready",
  "verdict_reason": "no new freeze reason categories and no projection errors versus the post-restore baseline"
}
JSON

assert_verdict rc78-real-shadow-eval.json ready 0 "RC78 real report (failed_accounts: [])"
assert_new_reasons rc78-real-shadow-eval.json "" "RC78 real report"
assert_has_errors rc78-real-shadow-eval.json false "RC78 real report (empty [] failed_accounts must not read as an error)"

rc78_deltas=$(shadow_eval_freeze_deltas "$fixtures_dir/rc78-real-shadow-eval.json")
if [[ "$rc78_deltas" != *"SOURCE_GAP 79 -> 85 (+6)"* ]]; then
  fail "shadow_eval_freeze_deltas: expected the SOURCE_GAP 79 -> 85 (+6) delta line, got:
$rc78_deltas"
fi
if [[ "$rc78_deltas" != *"UNKNOWN_NEGATIVE_BALANCE 158 -> 158 (0)"* ]]; then
  fail "shadow_eval_freeze_deltas: expected an unchanged-reason delta line, got:
$rc78_deltas"
fi

rc78_summary=$(shadow_eval_human_summary "$fixtures_dir/rc78-real-shadow-eval.json")
if [[ "$rc78_summary" != *"SOURCE_GAP 79 -> 85 (+6)"* ]]; then
  fail "human summary did not render the RC78 SOURCE_GAP delta"
fi

# A companion fixture derived from the same real report, with a genuinely
# new freeze-reason category added to "after" -- both the growing
# pre-existing SOURCE_GAP count (still not a regression on its own) and the
# new category must be handled correctly together.
write_fixture rc78-real-plus-new-reason-not-ready.json <"$fixtures_dir/rc78-real-shadow-eval.json"
sed -i \
  -e 's/"open": 85$/"open": 85\n      },\n      {\n        "freeze_reason": "AMBIGUOUS_EVENT_ORDER",\n        "open": 1/' \
  -e 's/"verdict": "ready"/"verdict": "not_ready"/' \
  -e 's/no new freeze reason categories and no projection errors versus the post-restore baseline/new freeze reason categories appeared after the candidate ran/' \
  "$fixtures_dir/rc78-real-plus-new-reason-not-ready.json"

assert_verdict rc78-real-plus-new-reason-not-ready.json not_ready 3 "RC78 real report plus a genuinely new reason"
assert_new_reasons rc78-real-plus-new-reason-not-ready.json "AMBIGUOUS_EVENT_ORDER" "RC78 real report plus a genuinely new reason"
echo "RC78 regression: ok"


# XM-INV-SHADOW-EVAL-VACUOUS: the bash verdict must reach the same
# conclusion as report.go's EvaluateReadiness about a run that was asked to
# reproject every account and projected none. The two implementations are
# compared against each other at rehearsal time, so a condition only one of
# them knows would surface as a tooling failure rather than as the blocked
# release it should be.

# The RC88 report's exact shape: identical before/after, no errors, drained
# in one round -- and not one account projected.
write_fixture vacuous-reproject-all.json <<'JSON'
{
  "generated_at": "2026-09-05T02:00:00Z",
  "backup_label": "invoice-20260904T184003Z",
  "candidate_image_tag": "0.1.0-rc92",
  "migrations_applied": null,
  "max_rounds": 200,
  "rounds_run": 1,
  "queue_drained": true,
  "reproject_all_requested": true,
  "accounts_enqueued": 8,
  "accounts_projected": 0,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 12
      }
    ],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 8,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 12
      }
    ],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 8,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "not_ready",
  "verdict_reason": "a full reprojection was requested but no account was projected, so this run proves nothing about the candidate"
}
JSON

# The same run, done for real. Note rounds_run is 1 here too: eight accounts
# fit in one batch of twenty-five, so the round count cannot distinguish
# these two fixtures and accounts_projected is the only field that does.
write_fixture reproject-all-projected.json <<'JSON'
{
  "generated_at": "2026-09-05T02:00:00Z",
  "backup_label": "invoice-20260904T184003Z",
  "candidate_image_tag": "0.1.0-rc92",
  "migrations_applied": null,
  "max_rounds": 200,
  "rounds_run": 1,
  "queue_drained": true,
  "reproject_all_requested": true,
  "accounts_enqueued": 8,
  "accounts_projected": 8,
  "before": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 12
      }
    ],
    "evaluations_by_status": []
  },
  "before_projection_health": {
    "queued": 8,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "after": {
    "accounts": [],
    "open_freezes_by_reason": [
      {
        "freeze_reason": "SOURCE_GAP",
        "open": 12
      }
    ],
    "evaluations_by_status": []
  },
  "after_projection_health": {
    "queued": 0,
    "failed": 0,
    "processing": 0,
    "oldest_pending": "0001-01-01T00:00:00Z",
    "proof_pending": 0,
    "oldest_proof_pending": "0001-01-01T00:00:00Z"
  },
  "failed_accounts": null,
  "round_errors": null,
  "new_freeze_reasons": null,
  "has_projection_errors": false,
  "verdict": "ready",
  "verdict_reason": "no new freeze reason categories and no projection errors versus the post-restore baseline"
}
JSON

assert_vacuous() {
  local file=$1 expected=$2 label=$3
  local actual
  actual=$(shadow_eval_run_was_vacuous "$fixtures_dir/$file")
  if [[ "$actual" != "$expected" ]]; then
    fail "$label: expected vacuous=$expected, got $actual"
  fi
}

assert_vacuous vacuous-reproject-all.json true "reproject-all that projected nothing"
assert_verdict vacuous-reproject-all.json not_ready 3 "reproject-all that projected nothing"
assert_new_reasons vacuous-reproject-all.json "" "reproject-all that projected nothing"

assert_vacuous reproject-all-projected.json false "reproject-all that projected every account"
assert_verdict reproject-all-projected.json ready 0 "reproject-all that projected every account"

# A report written before these fields existed carries neither key, so
# _shadow_eval_scalar returns empty for both and the condition must not
# fire. Reports already on disk from earlier rehearsals stay readable.
assert_vacuous no-freezes-ready.json false "pre-existing report without the new fields"
assert_vacuous rc78-real-shadow-eval.json false "RC78 real report without the new fields"
echo "reproject-all vacuity: ok"

if (( failures > 0 )); then
  echo "test-shadow-eval.sh: $failures failure(s)" >&2
  exit 1
fi
echo "test-shadow-eval.sh: all checks passed"
