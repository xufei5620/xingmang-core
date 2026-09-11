# TRIVY-03

share a daemon and volume mutex across worktrees and release output directories

No real Docker, network, task service, secrets or production operations. Pins/dependencies unchanged. Fixtures and prior cache evidence retained.

| Stage | UTC start | UTC end | Exit |
|---|---|---|---:|
| batch-green-image-parameters | 2026-09-10T17:54:19.781940+00:00 | 2026-09-10T17:54:20.542059+00:00 | 0 |
| batch-green-lock-errors | 2026-09-10T17:54:20.549773+00:00 | 2026-09-10T17:54:21.344144+00:00 | 0 |
| batch-green-resume-identity | 2026-09-10T17:54:21.351009+00:00 | 2026-09-10T17:54:22.308451+00:00 | 0 |
| batch-green-unchanged-cache | 2026-09-10T17:54:22.315628+00:00 | 2026-09-10T17:54:23.174667+00:00 | 0 |
| green | 2026-09-10T17:53:05.707741+00:00 | 2026-09-10T17:53:08.000965+00:00 | 0 |
| mutant-daemon-failure | 2026-09-10T17:54:11.734204+00:00 | 2026-09-10T17:54:13.691876+00:00 | 1 |
| mutant-daemon-key | 2026-09-10T17:54:09.657097+00:00 | 2026-09-10T17:54:11.724651+00:00 | 1 |
| mutant-no-exclusion | 2026-09-10T17:54:04.056035+00:00 | 2026-09-10T17:54:05.544157+00:00 | 1 |
| mutant-no-release | 2026-09-10T17:54:05.552055+00:00 | 2026-09-10T17:54:07.617434+00:00 | 1 |
| mutant-refresh-volume | 2026-09-10T17:54:15.894504+00:00 | 2026-09-10T17:54:17.752458+00:00 | 1 |
| mutant-release-output-lock | 2026-09-10T17:54:13.730282+00:00 | 2026-09-10T17:54:15.874678+00:00 | 1 |
| mutant-volume-key | 2026-09-10T17:54:07.627037+00:00 | 2026-09-10T17:54:09.648239+00:00 | 1 |
| mutant-worktree-key | 2026-09-10T17:54:03.216024+00:00 | 2026-09-10T17:54:04.047941+00:00 | 1 |
| red | 2026-09-10T17:53:03.557799+00:00 | 2026-09-10T17:53:04.483242+00:00 | 1 |
| restored-green | 2026-09-10T17:54:17.759329+00:00 | 2026-09-10T17:54:19.775523+00:00 | 0 |

Evidence and exact source SHA256: `G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-03/`. Tests run from current-source copies with explicit external boundaries; no full suite run.
