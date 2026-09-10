# ITR-REG-02

execute registration wiring behind fakes so branch WhatIf and refresh-invocation regressions fail

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| green-baseline-suite | 2026-09-10T17:56:57.727093+00:00 | 2026-09-10T17:56:58.532517+00:00 | 0 |
| green-meta | 2026-09-10T17:56:54.486209+00:00 | 2026-09-10T17:56:57.720553+00:00 | 0 |
| mutant-branch | 2026-09-10T17:56:58.539928+00:00 | 2026-09-10T17:56:59.250645+00:00 | 1 |
| mutant-refresh-invocation | 2026-09-10T17:57:00.817816+00:00 | 2026-09-10T17:57:01.520793+00:00 | 1 |
| mutant-register-whatif | 2026-09-10T17:56:59.258020+00:00 | 2026-09-10T17:57:00.057420+00:00 | 1 |
| mutant-update-whatif | 2026-09-10T17:57:00.065793+00:00 | 2026-09-10T17:57:00.809538+00:00 | 1 |
| red | 2026-09-10T17:56:18.471500+00:00 | 2026-09-10T17:56:21.622327+00:00 | 1 |
| restored-green | 2026-09-10T17:57:01.527284+00:00 | 2026-09-10T17:57:04.626629+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/ITR-REG-02/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
