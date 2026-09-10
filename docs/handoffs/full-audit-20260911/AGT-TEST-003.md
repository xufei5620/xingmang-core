# AGT-TEST-003 — separate invalid key ID from aliased output fixtures

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

Invalid signing ID now uses distinct absent paths and must report the ID error without creating either output. Alias rejection stays separate. Guard-bypass mutant is trapped before cryptographic generation; output-side-effect mutants write only inert local markers. Initial assertion capitalization mismatch was corrected to the actual consumer error, with its log retained. No key pair was generated or printed during these tests.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-id-underlying | 2026-09-10T17:46:37.899568+00:00 | 2026-09-10T17:46:39.501418+00:00 | 0 |
| red-id | 2026-09-10T17:46:39.503365+00:00 | 2026-09-10T17:46:39.541677+00:00 | 1 |
| green | 2026-09-10T17:46:39.575152+00:00 | 2026-09-10T17:46:40.669430+00:00 | 1 |
| green-actual-error-text | 2026-09-10T17:47:12.680195+00:00 | 2026-09-10T17:47:13.737040+00:00 | 0 |
| mutant-id-trapped | 2026-09-10T17:47:13.744647+00:00 | 2026-09-10T17:47:14.849934+00:00 | 1 |
| mutant-private-marker | 2026-09-10T17:47:14.857347+00:00 | 2026-09-10T17:47:16.016875+00:00 | 1 |
| mutant-public-marker | 2026-09-10T17:47:16.024521+00:00 | 2026-09-10T17:47:17.146368+00:00 | 1 |
| restored-green | 2026-09-10T17:47:17.152486+00:00 | 2026-09-10T17:47:18.103427+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/AGT-TEST-003/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
