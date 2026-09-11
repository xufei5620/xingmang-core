# IT-CLI-02 — propagate collected repair item failures after full reports

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

All four batch repair wrappers now return an aggregate error after printing their complete result when per-unit Errors is nonempty. A small private store interface permits in-memory result tests. Tests cover clean/all-failed/partial-failed results, error rows and successful rows; policy Blocked/NoOp stays successful without Errors. Each wrapper return and each report row class has an independent behavioral mutant. Exact-main process fixtures demonstrate nonzero exit and catch an exit-zero mutation, with all run I/O replaced by an inert result provider. No store transaction, evaluator, encryption, schema or dependency code changed. Queue-narrow result tests use dry-run to avoid field-key generation; apply reaches the identical result-error return.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-result-errors | 2026-09-10T17:51:49.860158+00:00 | 2026-09-10T17:51:50.993735+00:00 | 1 |
| green-result-errors | 2026-09-10T17:51:51.023889+00:00 | 2026-09-10T17:51:51.961597+00:00 | 0 |
| mutant-queue-error-return | 2026-09-10T17:52:52.988181+00:00 | 2026-09-10T17:52:54.091108+00:00 | 1 |
| mutant-queue-error-report | 2026-09-10T17:52:54.099969+00:00 | 2026-09-10T17:52:55.337646+00:00 | 1 |
| mutant-queue-success-report | 2026-09-10T17:52:55.347729+00:00 | 2026-09-10T17:52:56.593566+00:00 | 1 |
| mutant-projection-error-return | 2026-09-10T17:52:56.603873+00:00 | 2026-09-10T17:52:57.750306+00:00 | 1 |
| mutant-projection-error-report | 2026-09-10T17:52:57.759048+00:00 | 2026-09-10T17:52:58.839068+00:00 | 1 |
| mutant-projection-success-report | 2026-09-10T17:52:58.846848+00:00 | 2026-09-10T17:53:00.000641+00:00 | 1 |
| mutant-ingest-error-return | 2026-09-10T17:53:00.011074+00:00 | 2026-09-10T17:53:01.029069+00:00 | 1 |
| mutant-ingest-error-report | 2026-09-10T17:53:01.037976+00:00 | 2026-09-10T17:53:02.039767+00:00 | 1 |
| mutant-ingest-success-report | 2026-09-10T17:53:02.047831+00:00 | 2026-09-10T17:53:03.081972+00:00 | 1 |
| mutant-policy-error-return | 2026-09-10T17:53:03.089851+00:00 | 2026-09-10T17:53:04.195863+00:00 | 1 |
| mutant-policy-error-report | 2026-09-10T17:53:04.207279+00:00 | 2026-09-10T17:53:05.349882+00:00 | 1 |
| mutant-policy-success-report | 2026-09-10T17:53:05.359018+00:00 | 2026-09-10T17:53:06.439413+00:00 | 1 |
| mutant-clean-result-rejection | 2026-09-10T17:53:06.448713+00:00 | 2026-09-10T17:53:07.547626+00:00 | 1 |
| restored-green | 2026-09-10T17:53:07.555334+00:00 | 2026-09-10T17:53:08.350626+00:00 | 0 |
| fixed-main-build | 2026-09-10T17:53:08.359386+00:00 | 2026-09-10T17:53:09.025349+00:00 | 0 |
| fixed-main-process | 2026-09-10T17:53:09.031366+00:00 | 2026-09-10T17:53:09.238912+00:00 | 1 |
| mutant-main-exit-build | 2026-09-10T17:53:09.248555+00:00 | 2026-09-10T17:53:09.941604+00:00 | 0 |
| mutant-main-exit-process | 2026-09-10T17:53:09.948649+00:00 | 2026-09-10T17:53:10.105469+00:00 | 0 |
| mutant-main-exit-assertion | 2026-09-10T17:53:10.113612+00:00 | 2026-09-10T17:53:10.149049+00:00 | 1 |
| restored-main-build | 2026-09-10T17:53:10.158382+00:00 | 2026-09-10T17:53:10.812406+00:00 | 0 |
| restored-main-process | 2026-09-10T17:53:10.819204+00:00 | 2026-09-10T17:53:10.953225+00:00 | 1 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/IT-CLI-02/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
