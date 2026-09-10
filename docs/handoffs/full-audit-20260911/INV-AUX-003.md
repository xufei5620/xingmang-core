# INV-AUX-003

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `307d8f8ee4445c30401d877eee24fc9b06bc33f3`

Separate secret reads from export and explicitly propagate file/read failures. Documented test-DB eval/export captures now check the child status and reject empty output before applying the environment; header examples match. Execute current source snippets and actual documented shell blocks with inert inputs and trapped provisioning.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:43:03.946301+00:00 | 2026-09-10T17:43:05.212056+00:00 | 1 |
| red-corrected-fixture | 2026-09-10T17:43:33.138175+00:00 | 2026-09-10T17:43:34.676415+00:00 | 1 |
| green | 2026-09-10T17:44:03.135115+00:00 | 2026-09-10T17:44:04.372540+00:00 | 0 |
| mutation-db-format-rejected | 2026-09-10T17:45:02.847260+00:00 | 2026-09-10T17:45:03.112433+00:00 | 1 |
| mutation-bootstrap-format-rejected | 2026-09-10T17:45:03.190614+00:00 | 2026-09-10T17:45:03.642176+00:00 | 1 |
| mutation-read-failure-rejected | 2026-09-10T17:45:03.740448+00:00 | 2026-09-10T17:45:04.004099+00:00 | 1 |
| mutation-db-export-omitted | 2026-09-10T17:45:04.107264+00:00 | 2026-09-10T17:45:04.443378+00:00 | 1 |
| mutation-bootstrap-export-omitted | 2026-09-10T17:45:04.515411+00:00 | 2026-09-10T17:45:04.874619+00:00 | 1 |
| mutation-runbook-eval-failed | 2026-09-10T17:45:04.961732+00:00 | 2026-09-10T17:45:05.132962+00:00 | 1 |
| mutation-runbook-eval-empty | 2026-09-10T17:45:05.203196+00:00 | 2026-09-10T17:45:05.368136+00:00 | 1 |
| mutation-runbook-eval-valid | 2026-09-10T17:45:05.436747+00:00 | 2026-09-10T17:45:05.546663+00:00 | 1 |
| mutation-runbook-eval-valid | 2026-09-10T17:45:26.756617+00:00 | 2026-09-10T17:45:26.899013+00:00 | 1 |
| mutation-runbook-export-failed | 2026-09-10T17:45:26.961680+00:00 | 2026-09-10T17:45:27.101918+00:00 | 1 |
| mutation-runbook-export-empty | 2026-09-10T17:45:27.160508+00:00 | 2026-09-10T17:45:27.287000+00:00 | 1 |
| mutation-runbook-export-valid | 2026-09-10T17:45:27.344017+00:00 | 2026-09-10T17:45:27.473748+00:00 | 1 |
| mutation-header-eval-failed | 2026-09-10T17:45:27.534769+00:00 | 2026-09-10T17:45:27.669834+00:00 | 1 |
| mutation-header-eval-empty | 2026-09-10T17:45:27.732828+00:00 | 2026-09-10T17:45:27.853743+00:00 | 1 |
| mutation-header-eval-valid | 2026-09-10T17:45:27.914483+00:00 | 2026-09-10T17:45:28.044330+00:00 | 1 |
| mutation-header-export-failed | 2026-09-10T17:45:28.114395+00:00 | 2026-09-10T17:45:28.241843+00:00 | 1 |
| mutation-header-export-empty | 2026-09-10T17:45:28.302050+00:00 | 2026-09-10T17:45:28.432769+00:00 | 1 |
| mutation-header-export-valid | 2026-09-10T17:45:28.498152+00:00 | 2026-09-10T17:45:28.620486+00:00 | 1 |
| restored-green | 2026-09-10T17:45:28.681617+00:00 | 2026-09-10T17:45:29.763266+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-AUX-003` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/deploy/keycloak/entrypoint.sh`
- `platform/docs/runbooks/GIT-WORKFLOW.md`
- `platform/scripts/dev/worktree-testdb.sh`
- `invoice/scripts/tests/test_capture_failures.py`

## Preservation and limitations

Secret format, bootstrap selection, startup ordering and database provisioning implementation remain unchanged. 16 cases cover invalid DB/bootstrap, read failure, exported legal values and failed/empty/valid eval/export examples in both sources. All 17 corresponding mutations reject, followed by restored green.

No Keycloak startup, real secret, database, service or server operation. Initial regression fixture incorrectly validated missing DB variables after downstream continuation; red-corrected-fixture supersedes it. First eval-valid mutant removed its extraction anchor; corrected behavioral mutation preserves the anchor and rejects for lost environment binding. These setup results are not counted as mutation success. Full suites remain parent-coordinated.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
