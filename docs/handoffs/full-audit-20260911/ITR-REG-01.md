# ITR-REG-01

fail closed on task lookup errors and create only after established absence

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| batch-green-meta | 2026-09-10T17:59:03.042226+00:00 | 2026-09-10T17:59:06.117780+00:00 | 0 |
| batch-green-registration | 2026-09-10T17:59:02.095586+00:00 | 2026-09-10T17:59:03.035478+00:00 | 0 |
| green | 2026-09-10T17:58:32.227885+00:00 | 2026-09-10T17:58:33.071963+00:00 | 0 |
| mutant-any-notfound | 2026-09-10T17:58:57.935500+00:00 | 2026-09-10T17:58:58.683303+00:00 | 1 |
| mutant-force-create | 2026-09-10T17:58:59.467940+00:00 | 2026-09-10T17:59:00.209359+00:00 | 1 |
| mutant-reject-absence | 2026-09-10T17:58:58.690508+00:00 | 2026-09-10T17:58:59.459971+00:00 | 1 |
| mutant-silenced-query | 2026-09-10T17:58:57.204017+00:00 | 2026-09-10T17:58:57.928331+00:00 | 1 |
| mutant-swallow-write-errors | 2026-09-10T17:59:00.217828+00:00 | 2026-09-10T17:59:01.351991+00:00 | 1 |
| red | 2026-09-10T17:58:30.813058+00:00 | 2026-09-10T17:58:31.605445+00:00 | 1 |
| restored-green | 2026-09-10T17:59:01.358678+00:00 | 2026-09-10T17:59:02.089465+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/ITR-REG-01/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
