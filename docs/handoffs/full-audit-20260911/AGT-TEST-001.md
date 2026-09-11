# AGT-TEST-001 — seed and preserve full published sequence history

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

The schedule persistence fixture now has a legal successful publish CAS and compares full reloaded cursor and sequence state. Published revision/sequence/hash and a persisted cursor timestamp corruption are independently detected. Initial setup using sequence 7 violated the +1 CAS invariant. An attempted Cursor.Revision mutation was not behavioral because Load reconstructs that field, so its survival is excluded; the persisted timestamp mutation fails at the cursor assertion. Production state implementation remains unchanged.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-history-underlying | 2026-09-10T17:43:36.923308+00:00 | 2026-09-10T17:43:40.642916+00:00 | 0 |
| red-history | 2026-09-10T17:43:40.644996+00:00 | 2026-09-10T17:43:40.683765+00:00 | 1 |
| green | 2026-09-10T17:43:40.757030+00:00 | 2026-09-10T17:43:42.873015+00:00 | 1 |
| green-valid-cas | 2026-09-10T17:44:08.363408+00:00 | 2026-09-10T17:44:10.305093+00:00 | 0 |
| mutant-revision | 2026-09-10T17:44:10.308202+00:00 | 2026-09-10T17:44:12.272444+00:00 | 1 |
| mutant-sequence | 2026-09-10T17:44:12.280018+00:00 | 2026-09-10T17:44:14.283063+00:00 | 1 |
| mutant-hash | 2026-09-10T17:44:14.291496+00:00 | 2026-09-10T17:44:16.284089+00:00 | 1 |
| mutant-cursor | 2026-09-10T17:44:16.291105+00:00 | 2026-09-10T17:44:18.213460+00:00 | 0 |
| mutant-cursor-persisted-time | 2026-09-10T17:45:30.293184+00:00 | 2026-09-10T17:45:32.040590+00:00 | 1 |
| restored-green | 2026-09-10T17:45:32.046958+00:00 | 2026-09-10T17:45:33.295675+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/AGT-TEST-001/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
