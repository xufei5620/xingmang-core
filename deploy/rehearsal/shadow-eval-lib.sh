#!/usr/bin/env bash
# shadow-eval-lib.sh -- sourced by both shadow-eval.sh (the orchestrator) and
# test-shadow-eval.sh (its static test, no Docker).
#
# The functions below independently recompute XM-INV-SHADOW-EVAL's readiness
# verdict from a published shadow-eval.json report, mirroring
# EvaluateReadiness in backend/cmd/eligibility-shadow/report.go. This is
# deliberate duplication, not laziness: shadow-eval.sh uses its own
# recomputation -- not backend/cmd/eligibility-shadow's own process exit code
# -- to decide whether a release-blocking rehearsal passed, so a bug in
# either implementation alone cannot silently wave through a regressing
# release (shadow-eval.sh also compares the two and fails loudly if they
# disagree; see its own comments).
#
# This intentionally avoids a `jq` dependency: deploy/backup/backup.sh and
# deploy/backup/restore-drill.sh -- the two existing scripts this tool
# mirrors -- both deliberately keep their required-tool list to widely
# available coreutils/OpenSSH/age/Docker, and jq is not one of them (see
# each script's own "for command in ..." line). Rather than a general JSON
# parser, the functions below exploit that the report is produced by exactly
# one program this repo controls (backend/cmd/eligibility-shadow), whose
# `encoding/json` MarshalIndent output has a fixed, deterministic shape:
# 2-space indent per nesting level, struct fields always in Go declaration
# order, and -- critically -- an array with any elements is always expanded
# one element per line, never inlined, while an empty (non-nil) one inlines
# as "[]" and a nil one marshals as the literal `null`.
# backend/cmd/eligibility-shadow/report_test.go's
# TestReportJSONShapeMatchesShadowEvalLibAssumptions pins exactly this
# contract against the real Report struct; if a future change to its field
# order, an added custom MarshalJSON, or a switch away from MarshalIndent
# ever breaks one of these properties, that test fails loudly long before a
# rehearsal run would produce a silently wrong verdict. test-shadow-eval.sh's
# own fixtures below additionally exercise this file's parsing logic itself
# against literal JSON text in that same shape.

# shadow_eval_parse_config_user <config-user-string>
# Given a Docker image's `Config.User` field, prints "<uid> <gid>" and
# returns 0 when it is already a numeric uid[:gid] pinned literally in the
# Dockerfile (gid defaults to uid when the ":gid" part is absent); prints
# nothing and returns 1 for anything else (empty, meaning root by default,
# or a name such as "app") -- callers must resolve that case by asking the
# image itself (see shadow-eval.sh's resolve_tools_container_ids, of which
# this is the container-free, statically-testable half). This exists
# because backend/Dockerfile's tools stage sets `USER 10001:10001`
# literally, so the common case never needs to start a container just to
# learn its own configured user -- but the parsing itself must not assume
# that pin never changes.
shadow_eval_parse_config_user() {
  local config_user=$1
  if [[ "$config_user" =~ ^([0-9]+)(:([0-9]+))?$ ]]; then
    printf '%s %s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[3]:-${BASH_REMATCH[1]}}"
    return 0
  fi
  return 1
}

# _shadow_eval_freeze_block <report.json> <occurrence: 1|2>
# Prints "freeze_reason<TAB>open" once per element of the Nth
# "open_freezes_by_reason" array in the file (1 = the "before" snapshot's
# array, 2 = "after"'s -- Report declares Before ahead of After, so its
# array always serializes first; report_test.go pins this).
_shadow_eval_freeze_block() {
  local report=$1 want=$2
  awk -v want="$want" '
    # An empty slice ([]FreezeCount{} of length zero) marshals inline as
    # "...": [], on one line -- nothing to capture, and critically no
    # separate closing-bracket line will follow, so it must not set active.
    /"open_freezes_by_reason": \[\],?$/ {
      seen++
      active = 0
      next
    }
    /"open_freezes_by_reason": \[$/ {
      seen++
      active = (seen == want)
      next
    }
    active && /^ {4}\]/ { active = 0; next }
    active && /"freeze_reason":/ {
      reason = $0
      sub(/^ *"freeze_reason": *"/, "", reason)
      sub(/",? *$/, "", reason)
      next
    }
    active && /"open":/ {
      openval = $0
      sub(/^ *"open": */, "", openval)
      sub(/,? *$/, "", openval)
      print reason "\t" openval
    }
  ' "$report"
}

# shadow_eval_new_freeze_reasons <report.json>
# Prints one freeze_reason per line, sorted: every reason with open>0 in the
# "after" snapshot that was not already present with open>0 in the "before"
# snapshot (taken immediately after restore, before the candidate's
# projection ran anything). A pre-existing reason's open count growing is
# not itself a new category.
shadow_eval_new_freeze_reasons() {
  local report=$1
  local before_file after_file
  before_file=$(mktemp)
  after_file=$(mktemp)
  _shadow_eval_freeze_block "$report" 1 | awk -F'\t' '($2+0)>0{print $1}' | sort -u >"$before_file"
  _shadow_eval_freeze_block "$report" 2 | awk -F'\t' '($2+0)>0{print $1}' | sort -u >"$after_file"
  comm -13 "$before_file" "$after_file"
  rm -f "$before_file" "$after_file"
}

# shadow_eval_has_errors <report.json>
# Prints "true" or "false": whether the report recorded any round error
# (ProcessEligibilityProjectionJobs' own first-error-per-round contract) or
# any account left behind in eligibility_projection_jobs status='failed'
# (the durable per-account trace). Report.go's documented contract is that
# both fields are nil-until-appended, which MarshalIndent renders as the
# literal `null` -- but a real production run (RC78) hit a genuinely clean
# rehearsal (Go's own ExitCode said "ready") independently recomputed here
# as "not_ready", because toReportFailedAccounts (before its own fix) broke
# that contract and returned a non-nil empty slice, which marshals as the
# inline "[]" instead of `null`. Both are now fixed -- the Go side to
# actually honor its own nil-means-none contract (see report.go's comment on
# toReportFailedAccounts), and this check, as defense in depth, to recognize
# EITHER `null` or an empty "[]" as "nothing here", so a future regression
# in either implementation alone cannot silently mis-verdict a rehearsal.
shadow_eval_has_errors() {
  local report=$1
  if grep -qE '^  "round_errors": (null|\[\]),?$' "$report" && grep -qE '^  "failed_accounts": (null|\[\]),?$' "$report"; then
    printf 'false\n'
  else
    printf 'true\n'
  fi
}

# shadow_eval_report_is_valid <report.json>
# Returns 0 only when the file looks like a genuine
# backend/cmd/eligibility-shadow report (carries a top-level "verdict" key
# and no "tooling_failure" marker), 1 otherwise. A real production run
# reached the point where invoice-eligibility-shadow itself never got to
# print anything (a "read database credential: permission denied" error,
# fixed separately) -- shadow-eval.sh now writes an explicit
# `{"tooling_failure": true, ...}` marker into shadow-eval.json for exactly
# that case rather than leaving an empty file behind. Both
# shadow_eval_verdict_exit_code and shadow_eval_human_summary check this
# first and refuse to compute a verdict or render freeze/error counts from
# anything that fails it: an infrastructure/tooling problem must never be
# silently folded into "not_ready" (which would misrepresent it as the
# *candidate* regressing) or into "ready" (which would misrepresent it as
# nothing wrong at all).
shadow_eval_report_is_valid() {
  local report=$1
  grep -qE '^  "verdict": ' "$report" && ! grep -qE '^  "tooling_failure": true,?$' "$report"
}

# shadow_eval_verdict_exit_code <report.json>
# Prints "ready" or "not_ready" on stdout and returns 0 for ready, 3 for
# not_ready -- the same release-blocking contract as
# backend/cmd/eligibility-shadow's own process exit code (see ExitCode in
# report.go) -- for a genuine report (shadow_eval_report_is_valid). For
# anything else, prints "execution_failure" and returns 1, matching
# shadow-eval.sh's own script-level exit-code contract ("1: any other
# execution failure") rather than misusing 0 or 3.
shadow_eval_verdict_exit_code() {
  local report=$1
  local new_reasons has_errors
  if ! shadow_eval_report_is_valid "$report"; then
    printf 'execution_failure\n'
    return 1
  fi
  new_reasons=$(shadow_eval_new_freeze_reasons "$report")
  has_errors=$(shadow_eval_has_errors "$report")
  if [[ -n "$new_reasons" || "$has_errors" == "true" ]]; then
    printf 'not_ready\n'
    return 3
  fi
  printf 'ready\n'
  return 0
}

# _shadow_eval_scalar <report.json> <top-level key>
# Prints one top-level scalar field's value, unquoted, trailing comma
# stripped. Only used for keys that are unique at the top level of Report
# (generated_at, backup_label, candidate_image_tag, max_rounds, rounds_run,
# queue_drained, verdict, verdict_reason) -- never a key that also occurs
# nested (e.g. "queued" inside *_projection_health), which this would not
# disambiguate.
_shadow_eval_scalar() {
  local report=$1 key=$2
  grep -m1 -E "^  \"$key\": " "$report" | sed -E "s/^  \"$key\": \"?([^\",]*)\"?,?\$/\\1/"
}

# _shadow_eval_migrations_applied <report.json>
# Prints one migration file name per line from the top-level
# "migrations_applied" array (empty when it is the literal `null` --
# nothing was newly applied because the restored backup was already at the
# candidate's migration set). Same shape convention as round_errors/
# failed_accounts; see report_test.go's
# TestReportJSONShapeMatchesShadowEvalLibAssumptions for the pinned
# null-vs-populated marshaling this depends on. The opening-bracket marker
# is anchored to end-of-line ("\[$") specifically so an empty-but-non-nil
# "[]" (which this field never actually produces today, since
# parseMigrationsApplied returns nil for an empty flag -- but round_errors/
# failed_accounts' own history is exactly why this is not assumed) is never
# misread as an unclosed array, silently swallowing everything up to the
# next line that happens to match the closing-bracket pattern.
_shadow_eval_migrations_applied() {
  local report=$1
  awk '/"migrations_applied": \[$/{c=1;next} c&&/^ {2}\],?$/{exit} c&&/^ {4}"/{v=$0; sub(/^ {4}"/,"",v); sub(/",?$/,"",v); print v}' "$report"
}

# shadow_eval_migrate_docker_args <network> <tools_image> <secret_bind_source>
# Prints, one argument per line, the exact `docker run` argument list for
# the candidate tools image's invoice-migrate step against the restored,
# isolated database -- read-only, non-root-capable (the caller is
# responsible for having already chowned secret_bind_source to the tools
# image's own runtime uid, same as for invoice-eligibility-shadow), on the
# rehearsal's own network, reusing the same database-url secret bind-mount
# shadow-eval.sh already prepared for invoice-eligibility-shadow.
#
# backend/cmd/migrate/main.go takes no command-line flags at all: its
# connection comes from the DATABASE_URL_FILE environment variable, and its
# migrations directory from MIGRATIONS_DIR (already set by
# backend/Dockerfile's tools stage, `ENV MIGRATIONS_DIR=/app/migrations`, so
# not repeated here). This deliberately does not set APP_ENV or
# MIGRATION_MODE: leaving APP_ENV unset skips its production-only
# ELIGIBILITY_START_AT precondition (validateMigrationEligibilityPolicy --
# this is a throwaway rehearsal database, never production), and leaving
# MIGRATION_MODE unset selects its default apply mode (migrate.Up, not the
# read-only migrate.Verify) -- exactly what this rehearsal needs: bring the
# restored production schema up to the candidate's own migration set before
# driving its projection worker, the same as deploy/roll-forward.sh's own
# migrate step does against real production before anything else runs.
shadow_eval_migrate_docker_args() {
  local network=$1 tools_image=$2 secret_bind_source=$3
  printf '%s\n' \
    --pull never --rm --network "$network" --read-only \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --mount "type=bind,src=$secret_bind_source,dst=/run/secrets/database-url,readonly" \
    --env DATABASE_URL_FILE=/run/secrets/database-url \
    --entrypoint /usr/local/bin/invoice-migrate \
    "$tools_image"
}

# shadow_eval_freeze_deltas <report.json>
# Prints one line per freeze reason that appears in either snapshot, sorted:
# "REASON before -> after (+delta)" (or a "-" sign for a decrease, or no
# sign for zero). Purely informational -- never affects the verdict. Only
# whether a reason is genuinely NEW does that (shadow_eval_new_freeze_reasons);
# a pre-existing reason's count growing or shrinking is expected, ordinary
# operation (e.g. a batch of new SOURCE_GAP freezes opening from ordinary
# source-stream lag) and must never be mistaken for a regression -- this is
# what lets an operator see "SOURCE_GAP 79 -> 85 (+6)" in the summary without
# drawing that wrong conclusion themselves either.
shadow_eval_freeze_deltas() {
  local report=$1
  local before_file after_file reasons_file reason before_count after_count delta sign
  before_file=$(mktemp)
  after_file=$(mktemp)
  reasons_file=$(mktemp)
  _shadow_eval_freeze_block "$report" 1 >"$before_file"
  _shadow_eval_freeze_block "$report" 2 >"$after_file"
  { cut -f1 "$before_file"; cut -f1 "$after_file"; } | sort -u >"$reasons_file"
  while IFS= read -r reason; do
    [[ -n "$reason" ]] || continue
    before_count=$(awk -F'\t' -v r="$reason" '$1==r{print $2}' "$before_file")
    after_count=$(awk -F'\t' -v r="$reason" '$1==r{print $2}' "$after_file")
    before_count=${before_count:-0}
    after_count=${after_count:-0}
    delta=$((after_count - before_count))
    sign=""
    (( delta > 0 )) && sign="+"
    printf '%s %s -> %s (%s%s)\n' "$reason" "$before_count" "$after_count" "$sign" "$delta"
  done <"$reasons_file"
  rm -f "$before_file" "$after_file" "$reasons_file"
}

# shadow_eval_human_summary <report.json>
# Prints a short human-readable rendering of the report to stdout -- or, for
# anything shadow_eval_report_is_valid rejects (see its own doc comment), a
# short explanation that this was a tooling/execution failure with no
# verdict, instead of silently rendering blank or nonsensical freeze/error
# counts from a non-report file.
shadow_eval_human_summary() {
  local report=$1
  if ! shadow_eval_report_is_valid "$report"; then
    printf 'XM-INV-SHADOW-EVAL rehearsal report\n'
    printf '  EXECUTION FAILURE -- no report was produced, so there is no verdict.\n'
    printf '  reason:        %s\n' "$(_shadow_eval_scalar "$report" reason)"
    printf '  tool exit code: %s\n' "$(_shadow_eval_scalar "$report" tool_exit_code)"
    printf '  log file:      %s\n' "$(_shadow_eval_scalar "$report" log_file)"
    return 0
  fi
  local before_freezes after_freezes new_reasons freeze_deltas round_error_count failed_account_count migrations_applied
  before_freezes=$(_shadow_eval_freeze_block "$report" 1 | awk -F'\t' '{printf "%s%s=%s", (NR>1?", ":""), $1, $2}')
  after_freezes=$(_shadow_eval_freeze_block "$report" 2 | awk -F'\t' '{printf "%s%s=%s", (NR>1?", ":""), $1, $2}')
  new_reasons=$(shadow_eval_new_freeze_reasons "$report" | paste -sd ',' - | sed 's/,/, /g')
  freeze_deltas=$(shadow_eval_freeze_deltas "$report" | paste -sd ';' - | sed 's/;/; /g')
  # The opening-bracket markers below are anchored to end-of-line ("\[$")
  # for the identical reason _shadow_eval_migrations_applied's is: an
  # empty-but-non-nil "[]" (exactly what a real production run, RC78, hit
  # for failed_accounts before toReportFailedAccounts' own fix) must never
  # be misread as an unclosed array.
  round_error_count=$(awk '/"round_errors": \[$/{c=1;next} c&&/^ {2}\],?$/{exit} c&&/^ {4}"/{n++} END{print n+0}' "$report")
  failed_account_count=$(awk '/"failed_accounts": \[$/{c=1;next} c&&/^ {2}\],?$/{exit} c&&/^ {4}\{/{n++} END{print n+0}' "$report")
  migrations_applied=$(_shadow_eval_migrations_applied "$report" | paste -sd ',' - | sed 's/,/, /g')

  printf 'XM-INV-SHADOW-EVAL rehearsal report\n'
  printf '  backup:              %s\n' "$(_shadow_eval_scalar "$report" backup_label)"
  printf '  candidate image tag: %s\n' "$(_shadow_eval_scalar "$report" candidate_image_tag)"
  printf '  migrations applied:  %s\n' "${migrations_applied:-none}"
  printf '  generated at:        %s\n' "$(_shadow_eval_scalar "$report" generated_at)"
  printf '  rounds run:          %s / %s\n' "$(_shadow_eval_scalar "$report" rounds_run)" "$(_shadow_eval_scalar "$report" max_rounds)"
  printf '  queue drained:       %s\n' "$(_shadow_eval_scalar "$report" queue_drained)"
  printf '  tool verdict:        %s (%s)\n' "$(_shadow_eval_scalar "$report" verdict)" "$(_shadow_eval_scalar "$report" verdict_reason)"
  printf '\n'
  printf '  open freezes before: %s\n' "$before_freezes"
  printf '  open freezes after:  %s\n' "$after_freezes"
  printf '  freeze reason deltas (informational, does not affect the verdict): %s\n' "$freeze_deltas"
  printf '  new freeze reasons:  %s\n' "$new_reasons"
  printf '  round errors:        %s\n' "$round_error_count"
  printf '  failed accounts:     %s\n' "$failed_account_count"
}
