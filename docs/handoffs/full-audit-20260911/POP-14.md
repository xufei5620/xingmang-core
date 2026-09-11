# POP-14

status: fixed; targeted local validation only

The notification isolation assertion now observes the captured environment file and requires it to exist/nonempty, retaining the separate payload assertion. Old suite survives removal of env -i (meta red), fixed suite rejects that leak and missing capture independently, then restored suite passes. Runs are fresh Linux filesystem fixtures with fake Docker/curl and file-only Git; no real notifier/server/containers. An initial missing-file mutation search failed before execution and is not used as proof.

| Check | UTC start | UTC end | Exit |
|---|---|---|---:|
| baseline | 2026-09-10T17:58:34.546348+00:00 | 2026-09-10T17:58:35.918715+00:00 | 0 |
| meta-red | 2026-09-10T17:59:04.970328+00:00 | 2026-09-10T17:59:11.491351+00:00 | 1 |
| existing-notify-mutant | 2026-09-10T17:59:05.122447+00:00 | 2026-09-10T17:59:11.432697+00:00 | 0 |
| existing-notify-native | 2026-09-10T17:59:09.865193+00:00 | 2026-09-10T17:59:11.300424+00:00 | 0 |
| green | 2026-09-10T17:59:12.185453+00:00 | 2026-09-10T17:59:13.538873+00:00 | 0 |
| notify-mutant | 2026-09-10T17:59:13.786811+00:00 | 2026-09-10T17:59:15.434334+00:00 | 1 |
| notify-native | 2026-09-10T17:59:13.896917+00:00 | 2026-09-10T17:59:15.306466+00:00 | 1 |
| restored-green | 2026-09-10T17:59:15.688095+00:00 | 2026-09-10T17:59:17.195289+00:00 | 0 |
| absent-file-mutant-valid | 2026-09-10T17:59:30.668897+00:00 | 2026-09-10T17:59:32.199722+00:00 | 1 |
| absent-file-native-valid | 2026-09-10T17:59:30.813715+00:00 | 2026-09-10T17:59:32.106984+00:00 | 1 |
| restored-final | 2026-09-10T17:59:32.392586+00:00 | 2026-09-10T17:59:33.749380+00:00 | 0 |

Exact commands, source SHA256, mutation replacements and logs: `G:\xingmang\logs\full-audit-20260910\phase2\platform-tests\POP-14`. No production, server, real DB/container, credential or dependency changes. Full gates are deferred to parent integration.
