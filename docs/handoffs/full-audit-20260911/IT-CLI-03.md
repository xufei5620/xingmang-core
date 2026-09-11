# IT-CLI-03 — isolate acknowledge account flag rejection from event guard

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

The account-negative case now supplies a valid event, checks its own account error and no summary. This argument-only test uses nonexistent owned paths, removing its unnecessary database fixture. The meaningful mutant removes both account rejection paths; the old assertions survive with setup/I/O trapped and the strengthened case fails. Production repair behavior is unchanged.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red-masked-account-underlying | 2026-09-10T17:49:53.198328+00:00 | 2026-09-10T17:49:54.125442+00:00 | 0 |
| red-masked-account | 2026-09-10T17:49:54.126995+00:00 | 2026-09-10T17:49:54.156767+00:00 | 1 |
| green | 2026-09-10T17:49:54.193855+00:00 | 2026-09-10T17:49:55.147702+00:00 | 0 |
| mutant-account-guards | 2026-09-10T17:49:55.154805+00:00 | 2026-09-10T17:49:56.064227+00:00 | 1 |
| mutant-output | 2026-09-10T17:49:56.071627+00:00 | 2026-09-10T17:49:56.993241+00:00 | 1 |
| restored-green | 2026-09-10T17:49:56.999528+00:00 | 2026-09-10T17:49:57.614638+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/IT-CLI-03/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
