# IDEP-004 — bind Compose tag to approved env; reject inherited conflicts

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

The tag preflight rejects a conflicting inherited INVOICE_IMAGE_TAG before any image/production action, then exports the approved parsed tag to all Compose subprocesses. Unset and matching values pass; conflicting and explicitly empty values reject. Four corresponding mutations failed the intended cases.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-004`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:48:32.6014301Z | 2026-09-10T17:48:32.9695732Z | 1 |
| green | 2026-09-10T17:49:09.259834+00:00 | 2026-09-10T17:49:09.582551+00:00 | 0 |
| mutation-unset | 2026-09-10T17:49:09.585036+00:00 | 2026-09-10T17:49:09.738647+00:00 | 1 |
| mutation-matching | 2026-09-10T17:49:09.741011+00:00 | 2026-09-10T17:49:09.906912+00:00 | 1 |
| mutation-conflicting | 2026-09-10T17:49:09.909382+00:00 | 2026-09-10T17:49:10.059332+00:00 | 1 |
| mutation-empty | 2026-09-10T17:49:10.061713+00:00 | 2026-09-10T17:49:10.251287+00:00 | 1 |
| restored-green | 2026-09-10T17:49:10.252854+00:00 | 2026-09-10T17:49:10.580195+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
