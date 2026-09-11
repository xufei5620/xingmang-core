# IDEP-003 — forward sealed cutover runtime; verify both source fallbacks

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

Restore check-state now forwards the existing per-source immutable cutover runtime separately from the approved runtime. Empty/unset cutover values retain the prior approved-runtime fallback. Both sources and both fallback modes were exercised; eight argument mutations were rejected. The restore command example forwards the same existing variables; pins, manifest format and lifecycle are unchanged.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-003`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:45:36.3174859Z | 2026-09-10T17:45:36.6685039Z | 1 |
| green | 2026-09-10T17:46:16.581262+00:00 | 2026-09-10T17:46:16.942209+00:00 | 0 |
| mutation-1 | 2026-09-10T17:46:16.945044+00:00 | 2026-09-10T17:46:17.075408+00:00 | 1 |
| mutation-2 | 2026-09-10T17:46:17.077705+00:00 | 2026-09-10T17:46:17.200143+00:00 | 1 |
| mutation-3 | 2026-09-10T17:46:17.202988+00:00 | 2026-09-10T17:46:17.330117+00:00 | 1 |
| mutation-4 | 2026-09-10T17:46:17.333136+00:00 | 2026-09-10T17:46:17.464856+00:00 | 1 |
| mutation-5 | 2026-09-10T17:46:17.467492+00:00 | 2026-09-10T17:46:17.592389+00:00 | 1 |
| mutation-6 | 2026-09-10T17:46:17.594801+00:00 | 2026-09-10T17:46:17.713013+00:00 | 1 |
| mutation-7 | 2026-09-10T17:46:17.715675+00:00 | 2026-09-10T17:46:17.848823+00:00 | 1 |
| mutation-8 | 2026-09-10T17:46:17.851370+00:00 | 2026-09-10T17:46:17.976211+00:00 | 1 |
| restored-green | 2026-09-10T17:46:17.978026+00:00 | 2026-09-10T17:46:18.282140+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
