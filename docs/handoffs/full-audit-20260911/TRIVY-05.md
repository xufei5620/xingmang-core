# TRIVY-05

preserve lock IO failures and reserve exit 75 for actual contention

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| green | 2026-09-10T17:46:05.598746+00:00 | 2026-09-10T17:46:06.263269+00:00 | 0 |
| mutant-all-io-contention | 2026-09-10T17:46:21.947415+00:00 | 2026-09-10T17:46:22.588126+00:00 | 1 |
| mutant-message-classifier | 2026-09-10T17:46:23.237587+00:00 | 2026-09-10T17:46:23.908014+00:00 | 1 |
| mutant-missing-marker | 2026-09-10T17:46:22.608923+00:00 | 2026-09-10T17:46:23.222614+00:00 | 1 |
| red | 2026-09-10T17:46:04.250018+00:00 | 2026-09-10T17:46:04.900908+00:00 | 1 |
| restored-green | 2026-09-10T17:46:23.913091+00:00 | 2026-09-10T17:46:24.555200+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-05/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
