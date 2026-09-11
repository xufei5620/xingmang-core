# INT-TEST-02 — isolate strict JSON keyfile rejection fixtures

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

Unknown-field and trailing-JSON fixtures now differ from a valid deterministic keyring only at the predicate under test. Existing valid-keyring control is retained. Both old tests survive their guard-removal mutants (meta RED), and both strengthened cases reject the corresponding mutants. Production loading/encryption code is unchanged.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-unknown-underlying | 2026-09-10T16:41:10.008282+00:00 | 2026-09-10T16:41:36.257070+00:00 | 0 |
| red-unknown | 2026-09-10T16:41:36.259027+00:00 | 2026-09-10T16:41:36.291041+00:00 | 1 |
| red-trailing-underlying | 2026-09-10T16:41:36.298359+00:00 | 2026-09-10T16:41:37.269193+00:00 | 0 |
| red-trailing | 2026-09-10T16:41:37.276000+00:00 | 2026-09-10T16:41:37.309463+00:00 | 1 |
| green | 2026-09-10T16:41:37.445490+00:00 | 2026-09-10T16:41:38.364053+00:00 | 0 |
| mutant-unknown | 2026-09-10T16:41:38.370712+00:00 | 2026-09-10T16:41:39.280478+00:00 | 1 |
| mutant-trailing | 2026-09-10T16:41:39.288662+00:00 | 2026-09-10T16:41:40.191392+00:00 | 1 |
| restored-green | 2026-09-10T16:41:40.197724+00:00 | 2026-09-10T16:41:40.946227+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/INT-TEST-02/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
