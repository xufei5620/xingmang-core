# IDEP-005 — reject shadow native-status mismatch; test orchestrator verdict exits

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

The exact final orchestrator boundary now rejects a native tool status inconsistent with the independently recomputed verdict. Ready remains 0 and not-ready remains 3; execution mismatch is 1. First the existing test accepted a forced-success mutant and the meta-regression was red. Added actual-tail tests then exposed five native-status mismatches. The strengthened suite passed and rejected all seven specific mutants, including a final exit0 bypass. New test wiring in verify.ps1 is deferred to the parent-coordinated full gate.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-005`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:50:26.701324+00:00 | 2026-09-10T17:50:41.439916+00:00 | 1 |
| green | 2026-09-10T17:52:18.669885+00:00 | 2026-09-10T17:52:46.986313+00:00 | 0 |
| mutation-forced-success | 2026-09-10T17:52:46.991833+00:00 | 2026-09-10T17:53:17.359388+00:00 | 1 |
| mutation-ready-corrupted | 2026-09-10T17:53:17.364258+00:00 | 2026-09-10T17:53:45.433854+00:00 | 1 |
| mutation-native-error-1 | 2026-09-10T17:53:45.438831+00:00 | 2026-09-10T17:54:13.131187+00:00 | 1 |
| mutation-native-error-125 | 2026-09-10T17:54:13.137589+00:00 | 2026-09-10T17:54:44.269582+00:00 | 1 |
| mutation-native-error-137 | 2026-09-10T17:54:44.275152+00:00 | 2026-09-10T17:55:12.098363+00:00 | 1 |
| mutation-ready-status-mismatch | 2026-09-10T17:55:12.116196+00:00 | 2026-09-10T17:55:37.000584+00:00 | 1 |
| mutation-notready-status-mismatch | 2026-09-10T17:55:37.006952+00:00 | 2026-09-10T17:56:06.389681+00:00 | 1 |
| restored-green | 2026-09-10T17:56:06.391596+00:00 | 2026-09-10T17:56:34.547812+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
