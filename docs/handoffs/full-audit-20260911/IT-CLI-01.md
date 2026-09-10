# IT-CLI-01 — reject whitespace repair filters before credential access

status: fixed / targeted verified
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
branch: ai/codex/XM-FULL-AUDIT-invoice-tests-20260911

Nonempty whitespace-only account/event filters now fail before I/O. Documented empty/omitted bulk filters and valid UUID filters still reach the inert credential boundary. Tests run only against nonexistent paths in owned temporary directories. SQL, per-unit transactions and business behavior remain unchanged.

## Verification

| Case | UTC start | UTC end | Exit |
|---|---|---|---:|
| red | 2026-09-10T17:48:23.822130+00:00 | 2026-09-10T17:48:31.672896+00:00 | 1 |
| green | 2026-09-10T17:48:31.704095+00:00 | 2026-09-10T17:48:32.530906+00:00 | 0 |
| mutant-accountID | 2026-09-10T17:48:32.538465+00:00 | 2026-09-10T17:48:33.345956+00:00 | 1 |
| mutant-eventID | 2026-09-10T17:48:33.352391+00:00 | 2026-09-10T17:48:34.206049+00:00 | 1 |
| mutant-summary | 2026-09-10T17:48:34.212453+00:00 | 2026-09-10T17:48:35.025176+00:00 | 1 |
| mutant-legal-mode | 2026-09-10T17:48:35.031672+00:00 | 2026-09-10T17:48:35.893533+00:00 | 1 |
| restored-green | 2026-09-10T17:48:35.899991+00:00 | 2026-09-10T17:48:36.491056+00:00 | 0 |

Exact commands, failure patterns, overlay bytes and tested source SHA-256: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-tests/IT-CLI-01/steps.json`.

Preservation/limits: Only bounded local tests/overlays; no real database, CLI repair, key material, server or full gate execution.
