# POP-03

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `d4a0751bbb7706b10f605e75e836f3f70ef22248`.

release only invocation-owned promote lock files

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-03`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:44:00.055062+00:00 | 2026-09-10T17:44:05.510510+00:00 | 0 |

| mutation-foreign | 2026-09-10T17:44:05.862312+00:00 | 2026-09-10T17:44:06.102211+00:00 | 1 |

| mutation-owned | 2026-09-10T17:44:05.613055+00:00 | 2026-09-10T17:44:05.780407+00:00 | 1 |

| red | 2026-09-10T17:43:20.832196+00:00 | 2026-09-10T17:43:28.389417+00:00 | 1 |

| restored-green | 2026-09-10T17:44:06.183583+00:00 | 2026-09-10T17:44:06.425551+00:00 | 0 |

Red/mutant required failure: `FAIL POP-03:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.