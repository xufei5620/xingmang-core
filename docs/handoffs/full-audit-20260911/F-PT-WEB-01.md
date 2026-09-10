# F-PT-WEB-01

status: fixed; targeted local validation only

Only component tests changed. The success case observes the real child iframe postMessage payload and target origin; a valid iframe refresh message must trigger a second signing call. The old14 tests survive disconnected assertion wiring (meta-red-valid). New15 tests pass; assertion and reissue disconnections each fail their own assertion, then restored15 pass. Initial meta-red was an invalid mutation search with no test execution; it is retained but excluded as evidence. Dependencies installed offline from unchanged lock, zero downloads.

| Check | UTC start | UTC end | Exit |
|---|---|---|---:|
| meta-red | 2026-09-10T17:54:08.875202+00:00 | 2026-09-10T17:54:09.014941+00:00 | 0 |
| meta-red-valid | 2026-09-10T17:54:44.135791+00:00 | 2026-09-10T17:55:04.627326+00:00 | 1 |
| original-assertion-disconnected | 2026-09-10T17:54:44.309116+00:00 | 2026-09-10T17:55:04.576787+00:00 | 0 |
| green | 2026-09-10T17:55:38.297943+00:00 | 2026-09-10T17:55:40.630786+00:00 | 0 |
| assertion-disconnected | 2026-09-10T17:55:40.779939+00:00 | 2026-09-10T17:55:43.334768+00:00 | 1 |
| refresh-disconnected | 2026-09-10T17:55:43.480094+00:00 | 2026-09-10T17:55:46.841732+00:00 | 1 |
| restored-green | 2026-09-10T17:55:46.925487+00:00 | 2026-09-10T17:55:49.158535+00:00 | 0 |

Exact commands, source SHA256, mutation replacements and logs: `G:\xingmang\logs\full-audit-20260910\phase2\platform-tests\F-PT-WEB-01`. No production, server, real DB/container, credential or dependency changes. Full gates are deferred to parent integration.
