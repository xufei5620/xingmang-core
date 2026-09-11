# POP-06

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `264f2466cc63193e687924571ef9db79dd114230`.

stage decoded YAML separately and restore output on publication failure

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-06`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:49:16.907297+00:00 | 2026-09-10T17:49:22.095162+00:00 | 0 |

| green-complete | 2026-09-10T17:50:13.685368+00:00 | 2026-09-10T17:50:19.566960+00:00 | 0 |

| mutation-decrypt | 2026-09-10T17:49:22.699377+00:00 | 2026-09-10T17:49:23.322938+00:00 | 1 |

| mutation-input | 2026-09-10T17:49:22.195384+00:00 | 2026-09-10T17:49:22.605828+00:00 | 1 |

| mutation-invalid | 2026-09-10T17:49:23.414816+00:00 | 2026-09-10T17:49:24.175601+00:00 | 1 |

| mutation-invalid-preserved | 2026-09-10T17:50:19.664135+00:00 | 2026-09-10T17:50:20.564707+00:00 | 1 |

| mutation-preservation | 2026-09-10T17:49:24.264165+00:00 | 2026-09-10T17:49:24.825929+00:00 | 1 |

| mutation-publish-failure | 2026-09-10T17:50:21.867892+00:00 | 2026-09-10T17:50:23.025109+00:00 | 1 |

| mutation-rollback | 2026-09-10T17:50:20.659795+00:00 | 2026-09-10T17:50:21.742388+00:00 | 1 |

| parser-suite | 2026-09-10T17:50:24.385943+00:00 | 2026-09-10T17:50:25.417902+00:00 | 0 |

| red | 2026-09-10T17:48:02.889011+00:00 | 2026-09-10T17:48:07.636816+00:00 | 1 |

| restored-green | 2026-09-10T17:49:24.934539+00:00 | 2026-09-10T17:49:25.686128+00:00 | 0 |

| restored-green-final | 2026-09-10T17:50:23.126621+00:00 | 2026-09-10T17:50:24.291816+00:00 | 0 |

Red/mutant required failure: `FAIL POP-06:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.