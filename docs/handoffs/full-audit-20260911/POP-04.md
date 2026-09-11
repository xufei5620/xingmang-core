# POP-04

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `16732154aaec81ca0c9949eaf5622637bb204b91`.

lock shared checkout before fetch across candidate SHAs

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-04`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:45:34.211125+00:00 | 2026-09-10T17:45:39.497288+00:00 | 0 |

| mutation-cleanup | 2026-09-10T17:45:40.108506+00:00 | 2026-09-10T17:45:40.430285+00:00 | 1 |

| mutation-identity | 2026-09-10T17:45:40.526375+00:00 | 2026-09-10T17:45:40.865510+00:00 | 1 |

| mutation-per-sha | 2026-09-10T17:45:39.602125+00:00 | 2026-09-10T17:45:40.014360+00:00 | 1 |

| red | 2026-09-10T17:44:52.577490+00:00 | 2026-09-10T17:44:58.068216+00:00 | 1 |

| restored-green | 2026-09-10T17:45:40.958314+00:00 | 2026-09-10T17:45:41.265112+00:00 | 0 |

Red/mutant required failure: `FAIL POP-04:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.