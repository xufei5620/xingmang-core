# TRIVY-04

accept explicit immutable tagged image defaults while rejecting mutable references

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| green | 2026-09-10T17:45:00.582348+00:00 | 2026-09-10T17:45:01.226957+00:00 | 0 |
| mutant-mutable-reference | 2026-09-10T17:45:02.595216+00:00 | 2026-09-10T17:45:03.253810+00:00 | 1 |
| mutant-registry-port | 2026-09-10T17:45:03.270948+00:00 | 2026-09-10T17:45:04.237467+00:00 | 1 |
| mutant-tag-separator | 2026-09-10T17:45:01.858459+00:00 | 2026-09-10T17:45:02.576499+00:00 | 1 |
| red | 2026-09-10T17:44:22.571755+00:00 | 2026-09-10T17:44:23.288951+00:00 | 1 |
| restored-green | 2026-09-10T17:45:04.244248+00:00 | 2026-09-10T17:45:04.905008+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-04/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
