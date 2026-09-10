# POP-12

status: fixed; targeted local validation only

Local and server deploy tests compare trace positions for config/build/up. Installer test executes only its reviewed Git-config block behind an in-memory command recorder, so commented commands cannot satisfy checks. Test fixture umask077 supplies the owner-only marker required by existing hooks. The original order assertions and compatible-permission old installer assertions survive their mutants (meta RED); strengthened suites kill reordered calls and each commented installer config. All three suites restore GREEN. CPA code and dedicated CPA tests unchanged; no actual installation or deployment.

| Check | UTC start | UTC end | Exit |
|---|---|---|---:|
| meta-red-server-order | 2026-09-10T18:00:35.086741+00:00 | 2026-09-10T18:00:40.164209+00:00 | 1 |
| old-server-order | 2026-09-10T18:00:38.326405+00:00 | 2026-09-10T18:00:39.853089+00:00 | 0 |
| meta-red-local-order | 2026-09-10T18:00:40.269106+00:00 | 2026-09-10T18:00:43.607373+00:00 | 1 |
| old-local-order | 2026-09-10T18:00:40.472182+00:00 | 2026-09-10T18:00:43.353016+00:00 | 0 |
| meta-red-installer | 2026-09-10T18:00:43.689066+00:00 | 2026-09-10T18:00:46.311084+00:00 | 0 |
| old-installer-config | 2026-09-10T18:00:43.860606+00:00 | 2026-09-10T18:00:46.131058+00:00 | 1 |
| meta-red-installer-valid | 2026-09-10T18:02:01.117166+00:00 | 2026-09-10T18:02:03.657999+00:00 | 1 |
| old-installer-permission-control | 2026-09-10T18:02:01.407149+00:00 | 2026-09-10T18:02:03.574161+00:00 | 0 |
| green-a | 2026-09-10T18:02:03.891621+00:00 | 2026-09-10T18:02:06.085816+00:00 | 0 |
| green-b | 2026-09-10T18:02:06.347530+00:00 | 2026-09-10T18:02:07.618518+00:00 | 0 |
| green-local | 2026-09-10T18:02:07.868607+00:00 | 2026-09-10T18:02:10.475648+00:00 | 0 |
| local-order-mutant | 2026-09-10T18:02:26.697630+00:00 | 2026-09-10T18:02:29.295881+00:00 | 1 |
| server-order-mutant | 2026-09-10T18:02:29.623495+00:00 | 2026-09-10T18:02:30.905684+00:00 | 1 |
| installer-delete-mutant | 2026-09-10T18:02:31.220285+00:00 | 2026-09-10T18:02:33.434719+00:00 | 1 |
| installer-script-mutant | 2026-09-10T18:02:33.766157+00:00 | 2026-09-10T18:02:36.101959+00:00 | 1 |
| restored-a | 2026-09-10T18:02:36.402146+00:00 | 2026-09-10T18:02:38.683013+00:00 | 0 |
| restored-b | 2026-09-10T18:02:38.965346+00:00 | 2026-09-10T18:02:40.355928+00:00 | 0 |
| restored-local | 2026-09-10T18:02:40.631829+00:00 | 2026-09-10T18:02:43.283681+00:00 | 0 |

Exact commands, source SHA256, mutation replacements and logs: `G:\xingmang\logs\full-audit-20260910\phase2\platform-tests\POP-12`. No production, server, real DB/container, credential or dependency changes. Full gates are deferred to parent integration.
