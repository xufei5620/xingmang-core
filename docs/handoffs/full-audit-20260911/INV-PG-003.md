# INV-PG-003

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `b70d11f0b2e2b7a926194a9fd6b1a70824babf63`

The source-readiness static operator gate now executes the existing assert_plan predicate on text fixtures before checking static contracts. A meta-regression first proved the old gate accepted an early-return bypass; the strengthened gate rejects that same bypass. The production plan predicate and SQL are unchanged.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| meta-red | 2026-09-10T17:59:54.137905+00:00 | 2026-09-10T17:59:55.146070+00:00 | 1 |
| green | 2026-09-10T18:00:30.493654+00:00 | 2026-09-10T18:00:31.685500+00:00 | 0 |
| meta-green | 2026-09-10T18:00:31.742718+00:00 | 2026-09-10T18:00:32.628269+00:00 | 0 |
| mutation-allowed-index | 2026-09-10T18:00:33.162341+00:00 | 2026-09-10T18:00:33.273117+00:00 | 1 |
| mutation-missing-index | 2026-09-10T18:00:33.346295+00:00 | 2026-09-10T18:00:33.518900+00:00 | 1 |
| mutation-sequential-scan | 2026-09-10T18:00:33.587491+00:00 | 2026-09-10T18:00:33.745434+00:00 | 1 |
| mutation-parallel-sequential-scan | 2026-09-10T18:00:33.824928+00:00 | 2026-09-10T18:00:33.967771+00:00 | 1 |
| restored-green | 2026-09-10T18:00:34.048986+00:00 | 2026-09-10T18:00:35.404948+00:00 | 0 |
| wsl-path-green | 2026-09-10T18:01:06.763761+00:00 | 2026-09-10T18:01:13.002868+00:00 | 0 |
| mutation-allowed-index | 2026-09-10T18:01:43.880745+00:00 | 2026-09-10T18:01:48.546890+00:00 | 1 |
| mutation-missing-index | 2026-09-10T18:01:48.636653+00:00 | 2026-09-10T18:01:48.826147+00:00 | 1 |
| mutation-sequential-scan | 2026-09-10T18:01:48.899592+00:00 | 2026-09-10T18:01:49.107807+00:00 | 1 |
| mutation-parallel-sequential-scan | 2026-09-10T18:01:49.185224+00:00 | 2026-09-10T18:01:49.363844+00:00 | 1 |
| restored-green-final | 2026-09-10T18:01:49.438315+00:00 | 2026-09-10T18:01:51.114041+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-PG-003` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/scripts/verify-source-readiness-index-operator.ps1`
- `invoice/scripts/tests/test_readiness_plan.py`

## Preservation and limitations

Allowed index plans pass; missing-index, ordinary source_ingest_events Seq Scan and Parallel Seq Scan plans reject. Four case mutations reject plus an end-to-end copied-gate early-return meta-mutation. The final scoped gate passes with both Git Bash and the Windows WSL bash launcher using relative fixture paths.

Only the extracted shell predicate and existing offline/static gate run, on synthetic plan text. No PostgreSQL EXPLAIN, SQL execution, maintenance, Docker or production operation occurs. Local WSL may print its existing localhost proxy warning; the measured gate exits zero. Full final suites remain parent-coordinated.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
