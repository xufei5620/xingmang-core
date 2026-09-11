# POP-05

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `5b94b9e0f3c7db5141e3456d3395e09bbca14b7d`.

reject aliased and root-parent installation targets before writes

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-05`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:46:57.266182+00:00 | 2026-09-10T17:47:02.169930+00:00 | 0 |

| mutation-alias | 2026-09-10T17:47:02.257919+00:00 | 2026-09-10T17:47:02.402065+00:00 | 1 |

| mutation-legal | 2026-09-10T17:47:02.788205+00:00 | 2026-09-10T17:47:02.914757+00:00 | 1 |

| mutation-parent | 2026-09-10T17:47:02.487819+00:00 | 2026-09-10T17:47:02.688316+00:00 | 1 |

| red | 2026-09-10T17:46:23.375406+00:00 | 2026-09-10T17:46:28.325955+00:00 | 1 |

| restored-green | 2026-09-10T17:47:02.995832+00:00 | 2026-09-10T17:47:03.135550+00:00 | 0 |

Red/mutant required failure: `FAIL POP-05:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.