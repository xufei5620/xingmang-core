# POP-07

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `9b2b0017e129ca9277ceb5629b827f7815b4f07c`.

select smoke checks from parsed auth mode and existing defaults

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-07`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:51:32.502183+00:00 | 2026-09-10T17:51:37.472115+00:00 | 0 |

| mutation-explicit | 2026-09-10T17:51:38.701652+00:00 | 2026-09-10T17:51:38.963492+00:00 | 1 |

| mutation-local | 2026-09-10T17:51:37.581642+00:00 | 2026-09-10T17:51:37.852186+00:00 | 1 |

| mutation-production-default | 2026-09-10T17:51:37.952678+00:00 | 2026-09-10T17:51:38.217744+00:00 | 1 |

| mutation-staging-default | 2026-09-10T17:51:38.330537+00:00 | 2026-09-10T17:51:38.598083+00:00 | 1 |

| red | 2026-09-10T17:50:56.916817+00:00 | 2026-09-10T17:51:01.833828+00:00 | 1 |

| restored-green | 2026-09-10T17:51:39.092931+00:00 | 2026-09-10T17:51:39.365796+00:00 | 0 |

Red/mutant required failure: `FAIL POP-07:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.