# POP-13

status: fixed; targeted local validation only

Each negative fixture now satisfies earlier predicates: trusted-hash receives a writable fake Docker trace and asserts hash rejection before Docker; ready failure lets health pass; production confirmation executes only the extracted predicate with missing/wrong/valid tokens. The three old suites survive their defective implementations (meta RED); restored suites pass with matching current SHA256. Five mutations reject bypassed trusted hash, removed ready probe, bypassed confirmation, broken health control, and broken valid confirmation. Only test files changed; no production invocation.

| Check | UTC start | UTC end | Exit |
|---|---|---|---:|
| meta-red-hash | 2026-09-10T18:03:19.663267+00:00 | 2026-09-10T18:03:26.948198+00:00 | 1 |
| old-hash | 2026-09-10T18:03:24.574791+00:00 | 2026-09-10T18:03:26.852864+00:00 | 0 |
| meta-red-ready | 2026-09-10T18:03:27.009646+00:00 | 2026-09-10T18:03:28.636231+00:00 | 1 |
| old-ready | 2026-09-10T18:03:27.275146+00:00 | 2026-09-10T18:03:28.524729+00:00 | 0 |
| meta-red-confirm | 2026-09-10T18:03:28.715364+00:00 | 2026-09-10T18:03:30.349732+00:00 | 1 |
| old-confirm | 2026-09-10T18:03:28.986441+00:00 | 2026-09-10T18:03:30.234788+00:00 | 0 |
| green-a | 2026-09-10T18:04:22.540531+00:00 | 2026-09-10T18:04:24.754152+00:00 | 0 |
| green-b | 2026-09-10T18:04:25.002624+00:00 | 2026-09-10T18:04:26.340523+00:00 | 0 |
| hash-mutant | 2026-09-10T18:04:46.044839+00:00 | 2026-09-10T18:04:48.314672+00:00 | 1 |
| ready-mutant | 2026-09-10T18:04:48.593669+00:00 | 2026-09-10T18:04:49.945181+00:00 | 1 |
| confirm-mutant | 2026-09-10T18:04:50.232530+00:00 | 2026-09-10T18:04:51.666386+00:00 | 1 |
| health-control-mutant | 2026-09-10T18:04:51.884223+00:00 | 2026-09-10T18:04:53.565646+00:00 | 1 |
| health-native | 2026-09-10T18:04:52.041505+00:00 | 2026-09-10T18:04:53.460816+00:00 | 1 |
| valid-confirm-mutant | 2026-09-10T18:04:53.710476+00:00 | 2026-09-10T18:04:55.478203+00:00 | 1 |
| valid-confirm-native | 2026-09-10T18:04:53.868216+00:00 | 2026-09-10T18:04:55.377172+00:00 | 1 |
| restored-a | 2026-09-10T18:04:55.685164+00:00 | 2026-09-10T18:04:57.893570+00:00 | 0 |
| restored-b | 2026-09-10T18:04:58.149961+00:00 | 2026-09-10T18:04:59.491688+00:00 | 0 |

Exact commands, source SHA256, mutation replacements and logs: `G:\xingmang\logs\full-audit-20260910\phase2\platform-tests\POP-13`. No production, server, real DB/container, credential or dependency changes. Full gates are deferred to parent integration.
