# IDEP-007 — propagate proxy restart failures; test recovery exits

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

The normal restart helper now propagates Docker failure. The EXIT recovery still attempts the proxy restart, removes its own trap, and preserves the original nonzero status. Four exit scenarios and eight corresponding behavioral mutations were checked without changing lifecycle order.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-007`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:43:46.2437724Z | 2026-09-10T17:43:46.4351393Z | 1 |
| green | 2026-09-10T17:44:24.796915+00:00 | 2026-09-10T17:44:24.935477+00:00 | 0 |
| mutation-1 | 2026-09-10T17:44:24.938506+00:00 | 2026-09-10T17:44:25.016624+00:00 | 1 |
| mutation-2 | 2026-09-10T17:44:25.018913+00:00 | 2026-09-10T17:44:25.088565+00:00 | 1 |
| mutation-3 | 2026-09-10T17:44:25.090816+00:00 | 2026-09-10T17:44:25.173035+00:00 | 1 |
| mutation-4 | 2026-09-10T17:44:25.175206+00:00 | 2026-09-10T17:44:25.247691+00:00 | 1 |
| mutation-5 | 2026-09-10T17:44:25.249945+00:00 | 2026-09-10T17:44:25.333809+00:00 | 1 |
| mutation-6 | 2026-09-10T17:44:25.336073+00:00 | 2026-09-10T17:44:25.457005+00:00 | 1 |
| mutation-7 | 2026-09-10T17:44:25.459335+00:00 | 2026-09-10T17:44:25.543567+00:00 | 1 |
| mutation-8 | 2026-09-10T17:44:25.545794+00:00 | 2026-09-10T17:44:25.632404+00:00 | 1 |
| restored-green | 2026-09-10T17:44:25.634651+00:00 | 2026-09-10T17:44:25.767326+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
