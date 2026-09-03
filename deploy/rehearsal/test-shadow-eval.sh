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

if (( failures > 0 )); then
  echo "test-shadow-eval.sh: $failures failure(s)" >&2
  exit 1
fi
echo "test-shadow-eval.sh: all checks passed"
