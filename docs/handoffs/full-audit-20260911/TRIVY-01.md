# TRIVY-01

bind resumable parts to blob identity and retain rejected attempts for safe retries

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| green | 2026-09-10T17:48:09.569468+00:00 | 2026-09-10T17:48:10.452928+00:00 | 0 |
| green-complete | 2026-09-10T17:48:56.856028+00:00 | 2026-09-10T17:48:57.780472+00:00 | 0 |
| mutant-cli-identity | 2026-09-10T17:49:01.355280+00:00 | 2026-09-10T17:49:02.277542+00:00 | 1 |
| mutant-digest-check | 2026-09-10T17:48:59.518500+00:00 | 2026-09-10T17:49:00.422644+00:00 | 1 |
| mutant-legacy-parts | 2026-09-10T17:48:57.803059+00:00 | 2026-09-10T17:48:58.604537+00:00 | 1 |
| mutant-no-resume | 2026-09-10T17:48:58.625634+00:00 | 2026-09-10T17:48:59.495635+00:00 | 1 |
| mutant-rejected-retry | 2026-09-10T17:49:00.445646+00:00 | 2026-09-10T17:49:01.338006+00:00 | 1 |
| red | 2026-09-10T17:47:25.782310+00:00 | 2026-09-10T17:47:26.432046+00:00 | 1 |
| red-behavior | 2026-09-10T17:48:07.886509+00:00 | 2026-09-10T17:48:08.704186+00:00 | 1 |
| restored-green | 2026-09-10T17:49:02.283823+00:00 | 2026-09-10T17:49:03.156757+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-01/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
