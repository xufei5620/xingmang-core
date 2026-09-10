# AGT-TEST-002 — assert durable inventory and phase after partial scans

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

The existing two-page/restart fixture now snapshots the complete inventory before an incomplete scan and compares it after reopening, including every miss counter. It also requires durable scanning phase. Separate premature-finalization and idle-phase mutants fail the new assertions. Production reconciliation ordering and thresholds are unchanged.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-partial-miss-underlying | 2026-09-10T17:44:58.970908+00:00 | 2026-09-10T17:45:01.053321+00:00 | 0 |
| red-partial-miss | 2026-09-10T17:45:01.055073+00:00 | 2026-09-10T17:45:01.083444+00:00 | 1 |
| green | 2026-09-10T17:45:01.132530+00:00 | 2026-09-10T17:45:03.182650+00:00 | 0 |
| mutant-miss | 2026-09-10T17:45:03.191159+00:00 | 2026-09-10T17:45:05.412140+00:00 | 1 |
| mutant-phase | 2026-09-10T17:45:05.419235+00:00 | 2026-09-10T17:45:07.451423+00:00 | 1 |
| restored-green | 2026-09-10T17:45:07.459054+00:00 | 2026-09-10T17:45:08.872917+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/AGT-TEST-002/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
