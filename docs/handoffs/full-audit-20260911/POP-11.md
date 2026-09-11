# POP-11

Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `386be9fa0677485689a7aca77274ca33d644f1e8`.

validate RFC3339 dates and bind psql input values

Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.

Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-11`.

| Phase | UTC start | UTC end | Exit |

|---|---|---|---|

| green | 2026-09-10T17:55:41.700323+00:00 | 2026-09-10T17:55:42.453112+00:00 | 1 |

| green-r2 | 2026-09-10T17:56:29.992876+00:00 | 2026-09-10T17:56:34.898612+00:00 | 0 |

| mutation-binding-r2 | 2026-09-10T17:57:22.261489+00:00 | 2026-09-10T17:57:22.567788+00:00 | 1 |

| mutation-calendar-r2 | 2026-09-10T17:57:21.762654+00:00 | 2026-09-10T17:57:22.177327+00:00 | 1 |

| mutation-format-r2 | 2026-09-10T17:57:16.110103+00:00 | 2026-09-10T17:57:21.671837+00:00 | 1 |

| mutation-legal-r2 | 2026-09-10T17:57:23.468659+00:00 | 2026-09-10T17:57:23.650628+00:00 | 1 |

| mutation-until-binding | 2026-09-10T17:57:22.678946+00:00 | 2026-09-10T17:57:22.987053+00:00 | 1 |

| mutation-until-value | 2026-09-10T17:57:23.074187+00:00 | 2026-09-10T17:57:23.381412+00:00 | 1 |

| red | 2026-09-10T17:54:47.529230+00:00 | 2026-09-10T17:54:48.198326+00:00 | 1 |

| restored-green | 2026-09-10T17:55:43.741603+00:00 | 2026-09-10T17:55:44.345577+00:00 | 1 |

| restored-green-r2 | 2026-09-10T17:57:23.739768+00:00 | 2026-09-10T17:57:24.022469+00:00 | 0 |

Red/mutant required failure: `FAIL POP-11:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.

Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.