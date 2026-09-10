# IDEP-006 — verify resource absence after inspect errors; test denied queries

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

Cleanup now requires a successful resource inventory excluding the exact container/network name after an inspect error. Reachable Docker alone no longer proves absence. Existing three-state controls remain; four new denied-inspect/listing cases and four targeted mutations passed. The runbook uses the same absence contract.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-006`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:46:57.5961360Z | 2026-09-10T17:47:04.3236623Z | 1 |
| green | 2026-09-10T17:47:41.478572+00:00 | 2026-09-10T17:47:48.879644+00:00 | 0 |
| mutation-container-present | 2026-09-10T17:47:48.882669+00:00 | 2026-09-10T17:47:55.598089+00:00 | 1 |
| mutation-network-present | 2026-09-10T17:47:55.614199+00:00 | 2026-09-10T17:48:02.494015+00:00 | 1 |
| mutation-container-query-error | 2026-09-10T17:48:02.497198+00:00 | 2026-09-10T17:48:09.739822+00:00 | 1 |
| mutation-network-query-error | 2026-09-10T17:48:09.743494+00:00 | 2026-09-10T17:48:17.071824+00:00 | 1 |
| restored-green | 2026-09-10T17:48:17.073784+00:00 | 2026-09-10T17:48:24.228717+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
