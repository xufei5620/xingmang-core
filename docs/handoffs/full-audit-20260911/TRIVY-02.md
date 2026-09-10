# TRIVY-02

validate unchanged cache files and metadata and retain staging self-checks

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| green | 2026-09-10T17:50:25.855519+00:00 | 2026-09-10T17:50:26.740942+00:00 | 0 |
| mutant-downloaded | 2026-09-10T17:50:50.508055+00:00 | 2026-09-10T17:50:51.284703+00:00 | 1 |
| mutant-expired | 2026-09-10T17:50:49.717264+00:00 | 2026-09-10T17:50:50.488025+00:00 | 1 |
| mutant-future | 2026-09-10T17:50:51.306167+00:00 | 2026-09-10T17:50:52.107597+00:00 | 1 |
| mutant-missing-db | 2026-09-10T17:50:48.891678+00:00 | 2026-09-10T17:50:49.694999+00:00 | 1 |
| mutant-reader-presence | 2026-09-10T17:50:53.797654+00:00 | 2026-09-10T17:50:54.662659+00:00 | 1 |
| mutant-reject-valid | 2026-09-10T17:50:52.128467+00:00 | 2026-09-10T17:50:52.941739+00:00 | 1 |
| mutant-staging-bypass | 2026-09-10T17:50:52.958089+00:00 | 2026-09-10T17:50:53.775158+00:00 | 1 |
| red | 2026-09-10T17:50:23.904122+00:00 | 2026-09-10T17:50:24.897216+00:00 | 1 |
| restored-green | 2026-09-10T17:50:54.669202+00:00 | 2026-09-10T17:50:55.414603+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-02/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
