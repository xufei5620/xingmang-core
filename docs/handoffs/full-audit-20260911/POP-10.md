# POP-10

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `f99d90d1f64b86b263fc0235ccd77e0d9d2df9fc`.

validate each normalized test-mode target before filesystem access

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-10`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:54:01.298208+00:00 | 2026-09-10T17:54:06.041139+00:00 | 0 |

| mutation-alias | 2026-09-10T17:54:06.469552+00:00 | 2026-09-10T17:54:06.665608+00:00 | 1 |

| mutation-boundary | 2026-09-10T17:54:06.795441+00:00 | 2026-09-10T17:54:07.092237+00:00 | 1 |

| mutation-legal | 2026-09-10T17:54:07.194029+00:00 | 2026-09-10T17:54:07.304420+00:00 | 1 |

| mutation-targets | 2026-09-10T17:54:06.148907+00:00 | 2026-09-10T17:54:06.377343+00:00 | 1 |

| red | 2026-09-10T17:53:24.016356+00:00 | 2026-09-10T17:53:28.965875+00:00 | 1 |

| restored-green | 2026-09-10T17:54:07.398912+00:00 | 2026-09-10T17:54:07.663121+00:00 | 0 |

Red/mutant required failure: `FAIL POP-10:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.