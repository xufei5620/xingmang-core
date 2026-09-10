# F-PT-WEB-02

status: fixed; targeted local validation only

Only runtime config tests changed. A populated synthetic stale build origin now competes with absent or explicit runtime origin. Old23 tests let a Vite fallback survive (meta red); fixed23 pass, fallback and reversed-precedence mutants fail the corresponding absence/runtime-origin assertions, restored23 pass. Good runtime implementation is unchanged.

| Check | UTC start | UTC end | Exit |
|---|---|---|---:|
| meta-red | 2026-09-10T17:56:40.660480+00:00 | 2026-09-10T17:56:42.250238+00:00 | 1 |
| existing-fallback-mutant | 2026-09-10T17:56:40.929752+00:00 | 2026-09-10T17:56:42.193372+00:00 | 0 |
| green | 2026-09-10T17:56:43.189957+00:00 | 2026-09-10T17:56:44.449105+00:00 | 0 |
| fallback-mutant | 2026-09-10T17:56:44.590442+00:00 | 2026-09-10T17:56:45.781422+00:00 | 1 |
| precedence-mutant | 2026-09-10T17:56:45.950940+00:00 | 2026-09-10T17:56:47.175634+00:00 | 1 |
| restored-green | 2026-09-10T17:56:47.258849+00:00 | 2026-09-10T17:56:48.630207+00:00 | 0 |

Exact commands, source SHA256, mutation replacements and logs: `G:\xingmang\logs\full-audit-20260910\phase2\platform-tests\F-PT-WEB-02`. No production, server, real DB/container, credential or dependency changes. Full gates are deferred to parent integration.
