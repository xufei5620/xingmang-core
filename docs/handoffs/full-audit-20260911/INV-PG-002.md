# INV-PG-002

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `67254cc68d192efbf374269ddd74203a3272b9c1`

Parse each intended shell file with its own bash -n invocation and check each native result. Apply this to the top invoice source gate, readiness-index and balance-cleanup operator gates, plus the related permanent-Keycloak-administrator gate supplied by the parent.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:54:25.343151+00:00 | 2026-09-10T17:54:46.184967+00:00 | 1 |
| green | 2026-09-10T17:55:41.004885+00:00 | 2026-09-10T17:56:27.891919+00:00 | 0 |
| mutation-index-valid | 2026-09-10T17:56:43.113801+00:00 | 2026-09-10T17:56:44.427287+00:00 | 1 |
| mutation-index-invalid-0 | 2026-09-10T17:56:44.511891+00:00 | 2026-09-10T17:56:45.691275+00:00 | 1 |
| mutation-index-invalid-1 | 2026-09-10T17:56:45.779153+00:00 | 2026-09-10T17:56:46.937809+00:00 | 1 |
| mutation-cleanup-valid | 2026-09-10T17:56:47.019895+00:00 | 2026-09-10T17:56:48.377554+00:00 | 1 |
| mutation-cleanup-invalid-0 | 2026-09-10T17:56:48.470666+00:00 | 2026-09-10T17:56:49.759893+00:00 | 1 |
| mutation-cleanup-invalid-1 | 2026-09-10T17:56:49.847336+00:00 | 2026-09-10T17:56:51.158612+00:00 | 1 |
| mutation-cleanup-invalid-2 | 2026-09-10T17:56:51.254336+00:00 | 2026-09-10T17:56:52.523654+00:00 | 1 |
| mutation-cleanup-invalid-3 | 2026-09-10T17:56:52.609515+00:00 | 2026-09-10T17:56:53.766615+00:00 | 1 |
| mutation-keycloak-valid | 2026-09-10T17:56:53.846298+00:00 | 2026-09-10T17:56:55.072268+00:00 | 1 |
| mutation-keycloak-invalid-0 | 2026-09-10T17:56:55.153718+00:00 | 2026-09-10T17:56:56.253324+00:00 | 1 |
| mutation-keycloak-invalid-1 | 2026-09-10T17:56:56.325624+00:00 | 2026-09-10T17:56:57.434467+00:00 | 1 |
| mutation-top-valid | 2026-09-10T17:56:57.511504+00:00 | 2026-09-10T17:56:58.680575+00:00 | 1 |
| mutation-top-invalid-0 | 2026-09-10T17:56:58.763844+00:00 | 2026-09-10T17:57:01.051912+00:00 | 1 |
| mutation-top-invalid-1 | 2026-09-10T17:57:01.134718+00:00 | 2026-09-10T17:57:03.516508+00:00 | 1 |
| mutation-top-invalid-2 | 2026-09-10T17:57:03.598478+00:00 | 2026-09-10T17:57:05.933491+00:00 | 1 |
| mutation-top-invalid-3 | 2026-09-10T17:57:06.012977+00:00 | 2026-09-10T17:57:08.234162+00:00 | 1 |
| mutation-top-invalid-4 | 2026-09-10T17:57:08.309277+00:00 | 2026-09-10T17:57:10.536025+00:00 | 1 |
| mutation-top-invalid-5 | 2026-09-10T17:57:10.617896+00:00 | 2026-09-10T17:57:12.825352+00:00 | 1 |
| mutation-top-invalid-6 | 2026-09-10T17:57:12.898862+00:00 | 2026-09-10T17:57:15.151346+00:00 | 1 |
| mutation-top-invalid-7 | 2026-09-10T17:57:15.223469+00:00 | 2026-09-10T17:57:17.487056+00:00 | 1 |
| mutation-top-invalid-8 | 2026-09-10T17:57:17.572504+00:00 | 2026-09-10T17:57:19.723445+00:00 | 1 |
| mutation-top-invalid-9 | 2026-09-10T17:57:19.799738+00:00 | 2026-09-10T17:57:22.153529+00:00 | 1 |
| mutation-top-invalid-10 | 2026-09-10T17:57:22.230562+00:00 | 2026-09-10T17:57:24.688707+00:00 | 1 |
| mutation-top-invalid-11 | 2026-09-10T17:57:24.773302+00:00 | 2026-09-10T17:57:27.438692+00:00 | 1 |
| mutation-top-invalid-12 | 2026-09-10T17:57:27.523749+00:00 | 2026-09-10T17:57:30.018832+00:00 | 1 |
| mutation-top-invalid-13 | 2026-09-10T17:57:30.110300+00:00 | 2026-09-10T17:57:32.470956+00:00 | 1 |
| mutation-top-invalid-14 | 2026-09-10T17:57:32.554561+00:00 | 2026-09-10T17:57:34.866860+00:00 | 1 |
| mutation-top-invalid-15 | 2026-09-10T17:57:34.944578+00:00 | 2026-09-10T17:57:37.276292+00:00 | 1 |
| mutation-top-invalid-16 | 2026-09-10T17:57:37.351663+00:00 | 2026-09-10T17:57:39.622895+00:00 | 1 |
| mutation-top-invalid-17 | 2026-09-10T17:57:39.713146+00:00 | 2026-09-10T17:57:41.931914+00:00 | 1 |
| mutation-top-invalid-18 | 2026-09-10T17:57:42.003561+00:00 | 2026-09-10T17:57:44.352315+00:00 | 1 |
| mutation-top-invalid-19 | 2026-09-10T17:57:44.434962+00:00 | 2026-09-10T17:57:46.674671+00:00 | 1 |
| mutation-top-invalid-20 | 2026-09-10T17:57:46.753685+00:00 | 2026-09-10T17:57:49.279089+00:00 | 1 |
| mutation-top-invalid-21 | 2026-09-10T17:57:49.352955+00:00 | 2026-09-10T17:57:51.618219+00:00 | 1 |
| mutation-top-invalid-22 | 2026-09-10T17:57:51.697259+00:00 | 2026-09-10T17:57:54.091485+00:00 | 1 |
| mutation-top-invalid-23 | 2026-09-10T17:57:54.178850+00:00 | 2026-09-10T17:57:56.579468+00:00 | 1 |
| mutation-top-invalid-24 | 2026-09-10T17:57:56.662169+00:00 | 2026-09-10T17:57:59.008558+00:00 | 1 |
| mutation-top-invalid-25 | 2026-09-10T17:57:59.088288+00:00 | 2026-09-10T17:58:01.409967+00:00 | 1 |
| mutation-top-invalid-26 | 2026-09-10T17:58:01.490959+00:00 | 2026-09-10T17:58:03.824250+00:00 | 1 |
| mutation-top-invalid-27 | 2026-09-10T17:58:03.905267+00:00 | 2026-09-10T17:58:06.290085+00:00 | 1 |
| mutation-top-invalid-28 | 2026-09-10T17:58:06.372797+00:00 | 2026-09-10T17:58:08.732432+00:00 | 1 |
| mutation-top-invalid-29 | 2026-09-10T17:58:08.805288+00:00 | 2026-09-10T17:58:11.068286+00:00 | 1 |
| mutation-top-invalid-30 | 2026-09-10T17:58:11.147866+00:00 | 2026-09-10T17:58:13.310947+00:00 | 1 |
| mutation-top-invalid-31 | 2026-09-10T17:58:13.380807+00:00 | 2026-09-10T17:58:15.513799+00:00 | 1 |
| mutation-top-invalid-32 | 2026-09-10T17:58:15.587205+00:00 | 2026-09-10T17:58:17.766331+00:00 | 1 |
| mutation-top-invalid-33 | 2026-09-10T17:58:17.839455+00:00 | 2026-09-10T17:58:20.243304+00:00 | 1 |
| restored-green | 2026-09-10T17:58:48.853047+00:00 | 2026-09-10T17:59:34.617297+00:00 | 0 |
| index-scoped-gate | 2026-09-10T17:59:52.030836+00:00 | 2026-09-10T17:59:52.820545+00:00 | 0 |
| cleanup-scoped-gate | 2026-09-10T17:59:52.873304+00:00 | 2026-09-10T17:59:53.545578+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-PG-002` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/scripts/verify.ps1`
- `invoice/scripts/verify-source-readiness-index-operator.ps1`
- `invoice/scripts/verify-balance-history-cleanup-operator.ps1`
- `invoice/scripts/verify-keycloak-permanent-master-admin.ps1`
- `invoice/scripts/tests/test_shell_syntax_coverage.py`

## Preservation and limitations

Production shell behavior and source-gate lifecycle order are unchanged. The fixture extracts the current PowerShell invocation/exit guard through AST and executes only bash -n over harmless files. Four legal controls and 42 individual invalid-file cases cover all 34 top-level enumerated shell scripts plus the narrower gates; every case rejects a corresponding skip-file or misreject mutation before restored green.

No full invoice gate, maintenance, backup, database, production wrapper, secret or server operation executes. Scoped tests invoke only syntax checks and the existing read-only/static operator gates. New files added later to deploy/scripts enter the top-level fixture enumeration automatically.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
