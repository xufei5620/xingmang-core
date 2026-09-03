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
# one element per line, never inlined. report_test.go's fixtures in this
# directory pin that shape; if a future change to report.go's field order or
# indentation ever breaks it, that test catches it long before a rehearsal
# run would.

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
# (the durable per-account trace). Report.go leaves both fields as their nil
# ([]T) zero value, which MarshalIndent renders as the literal `null`, until
# something is appended -- so "both still null" is the precise, unambiguous
# "no errors" case.
shadow_eval_has_errors() {
  local report=$1
  if grep -qE '^  "round_errors": null,?$' "$report" && grep -qE '^  "failed_accounts": null,?$' "$report"; then
    printf 'false\n'
  else
    printf 'true\n'
  fi
}

# shadow_eval_verdict_exit_code <report.json>
# Prints "ready" or "not_ready" on stdout and returns 0 for ready, 3 for
# not_ready -- the same release-blocking contract as
# backend/cmd/eligibility-shadow's own process exit code (see ExitCode in
# report.go).
shadow_eval_verdict_exit_code() {
  local report=$1
  local new_reasons has_errors
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

# shadow_eval_human_summary <report.json>
# Prints a short human-readable rendering of the report to stdout.
shadow_eval_human_summary() {
  local report=$1
  local before_freezes after_freezes new_reasons round_error_count failed_account_count
  before_freezes=$(_shadow_eval_freeze_block "$report" 1 | awk -F'\t' '{printf "%s%s=%s", (NR>1?", ":""), $1, $2}')
  after_freezes=$(_shadow_eval_freeze_block "$report" 2 | awk -F'\t' '{printf "%s%s=%s", (NR>1?", ":""), $1, $2}')
  new_reasons=$(shadow_eval_new_freeze_reasons "$report" | paste -sd ',' - | sed 's/,/, /g')
  round_error_count=$(awk '/"round_errors": \[/{c=1;next} c&&/^ {2}\],?$/{exit} c&&/^ {4}"/{n++} END{print n+0}' "$report")
  failed_account_count=$(awk '/"failed_accounts": \[/{c=1;next} c&&/^ {2}\],?$/{exit} c&&/^ {4}\{/{n++} END{print n+0}' "$report")

  printf 'XM-INV-SHADOW-EVAL rehearsal report\n'
  printf '  backup:              %s\n' "$(_shadow_eval_scalar "$report" backup_label)"
  printf '  candidate image tag: %s\n' "$(_shadow_eval_scalar "$report" candidate_image_tag)"
  printf '  generated at:        %s\n' "$(_shadow_eval_scalar "$report" generated_at)"
  printf '  rounds run:          %s / %s\n' "$(_shadow_eval_scalar "$report" rounds_run)" "$(_shadow_eval_scalar "$report" max_rounds)"
  printf '  queue drained:       %s\n' "$(_shadow_eval_scalar "$report" queue_drained)"
  printf '  tool verdict:        %s (%s)\n' "$(_shadow_eval_scalar "$report" verdict)" "$(_shadow_eval_scalar "$report" verdict_reason)"
  printf '\n'
  printf '  open freezes before: %s\n' "$before_freezes"
  printf '  open freezes after:  %s\n' "$after_freezes"
  printf '  new freeze reasons:  %s\n' "$new_reasons"
  printf '  round errors:        %s\n' "$round_error_count"
  printf '  failed accounts:     %s\n' "$failed_account_count"
}
