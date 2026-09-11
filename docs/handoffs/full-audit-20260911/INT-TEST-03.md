# INT-TEST-03 — exercise traversal against a real sandbox sibling

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

Created a bounded readable sibling and an allowed issued-file control. The old test survives direct-open mutation; strengthened traversal and positive-control assertions separately kill boundary removal and reject-all mutants. Only the test changed.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-traversal-underlying | 2026-09-10T17:42:45.097537+00:00 | 2026-09-10T17:42:58.442935+00:00 | 0 |
| red-traversal | 2026-09-10T17:42:58.444854+00:00 | 2026-09-10T17:42:58.472742+00:00 | 1 |
| green | 2026-09-10T17:42:58.519381+00:00 | 2026-09-10T17:42:59.741481+00:00 | 0 |
| mutant-boundary | 2026-09-10T17:42:59.748221+00:00 | 2026-09-10T17:43:00.889284+00:00 | 1 |
| mutant-allowed-control | 2026-09-10T17:43:00.896114+00:00 | 2026-09-10T17:43:02.020593+00:00 | 1 |
| restored-green | 2026-09-10T17:43:02.026960+00:00 | 2026-09-10T17:43:02.941707+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/INT-TEST-03/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
