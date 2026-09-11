# POP-01
Branch: ai/codex/XM-FULL-AUDIT-platform-ops-20260911; base: `f58c7ed9f28eef099ffab137513a821b35119fa6`.
separate Git roots from platform asset roots in tools and runbooks
Only owned worktree changed; no production, remote push, database, real notification, secret material or CPA operation. Tests run on new synthetic fixtures or extracted side-effect-free boundaries. Runtime-blocked phase1 artifact-controls was not retried.
Evidence: `G:\xingmang\logs\full-audit-20260910\phase2\platform-ops\POP-01`.
| Phase | UTC start | UTC end | Exit |
|---|---|---|---|
| batch-existing-monorepo | 2026-09-10T18:20:45.501490+00:00 | 2026-09-10T18:20:58.013259+00:00 | 0 |
| green | 2026-09-10T18:03:46.409798+00:00 | 2026-09-10T18:03:59.810306+00:00 | 0 |
| green-complete-layouts | 2026-09-10T18:14:37.396298+00:00 | 2026-09-10T18:14:57.115109+00:00 | 0 |
| green-doc-assets | 2026-09-10T18:04:41.248503+00:00 | 2026-09-10T18:04:51.254460+00:00 | 1 |
| green-production-paths | 2026-09-10T18:16:15.737529+00:00 | 2026-09-10T18:16:35.740624+00:00 | 0 |
| mutation-ci-container-cwd | 2026-09-10T18:18:32.144936+00:00 | 2026-09-10T18:18:48.153228+00:00 | 1 |
| mutation-ci-container-env | 2026-09-10T18:18:30.930457+00:00 | 2026-09-10T18:18:47.085513+00:00 | 1 |
| mutation-ci-default-root | 2026-09-10T18:18:33.798236+00:00 | 2026-09-10T18:18:49.791737+00:00 | 1 |
| mutation-ci-project-cwd | 2026-09-10T18:18:47.225492+00:00 | 2026-09-10T18:19:02.904135+00:00 | 1 |
| mutation-compose-context | 2026-09-10T18:17:56.248875+00:00 | 2026-09-10T18:18:17.366034+00:00 | 1 |
| mutation-deploy-project | 2026-09-10T18:17:56.248867+00:00 | 2026-09-10T18:18:14.525546+00:00 | 1 |
| mutation-git-workflow | 2026-09-10T18:20:03.698628+00:00 | 2026-09-10T18:20:17.159575+00:00 | 1 |
| mutation-golive-assets | 2026-09-10T18:19:48.607235+00:00 | 2026-09-10T18:20:02.697286+00:00 | 1 |
| mutation-golive-checkout | 2026-09-10T18:19:49.582851+00:00 | 2026-09-10T18:20:03.581232+00:00 | 1 |
| mutation-local-monorepo | 2026-09-10T18:18:16.369379+00:00 | 2026-09-10T18:18:32.001562+00:00 | 1 |
| mutation-local-standalone | 2026-09-10T18:18:14.676268+00:00 | 2026-09-10T18:18:30.781833+00:00 | 1 |
| mutation-migration-cwd | 2026-09-10T18:19:05.982129+00:00 | 2026-09-10T18:19:21.905737+00:00 | 1 |
| mutation-migration-project | 2026-09-10T18:19:04.082937+00:00 | 2026-09-10T18:19:19.892740+00:00 | 1 |
| mutation-prod-compose-whitelist | 2026-09-10T18:19:19.040970+00:00 | 2026-09-10T18:19:34.110526+00:00 | 1 |
| mutation-prod-env-whitelist | 2026-09-10T18:19:22.042139+00:00 | 2026-09-10T18:19:36.827688+00:00 | 1 |
| mutation-prod-override-whitelist | 2026-09-10T18:19:20.028733+00:00 | 2026-09-10T18:19:35.138103+00:00 | 1 |
| mutation-prod-port | 2026-09-10T18:19:34.236297+00:00 | 2026-09-10T18:19:48.464039+00:00 | 1 |
| mutation-promote-hook | 2026-09-10T18:18:17.501206+00:00 | 2026-09-10T18:18:33.661256+00:00 | 1 |
| mutation-quickstart-assets | 2026-09-10T18:19:35.285098+00:00 | 2026-09-10T18:19:49.450117+00:00 | 1 |
| mutation-quickstart-checkout | 2026-09-10T18:19:36.950647+00:00 | 2026-09-10T18:19:51.237519+00:00 | 1 |
| mutation-reqlog-assets | 2026-09-10T18:19:51.365858+00:00 | 2026-09-10T18:20:05.347471+00:00 | 1 |
| mutation-reqlog-checkout | 2026-09-10T18:20:02.825665+00:00 | 2026-09-10T18:20:16.313873+00:00 | 1 |
| mutation-tree-prefix | 2026-09-10T18:17:56.248551+00:00 | 2026-09-10T18:18:16.224844+00:00 | 1 |
| mutation-verify-compose-cwd | 2026-09-10T18:19:03.042942+00:00 | 2026-09-10T18:19:18.902337+00:00 | 1 |
| mutation-verify-default-assets | 2026-09-10T18:18:49.928280+00:00 | 2026-09-10T18:19:05.845054+00:00 | 1 |
| mutation-verify-default-root | 2026-09-10T18:18:48.291534+00:00 | 2026-09-10T18:19:03.951413+00:00 | 1 |
| red | 2026-09-10T18:02:18.376281+00:00 | 2026-09-10T18:02:25.867830+00:00 | 1 |
| red-checkout-root | 2026-09-10T18:13:23.987635+00:00 | 2026-09-10T18:13:44.025679+00:00 | 1 |
| restored-green | 2026-09-10T18:20:28.258583+00:00 | 2026-09-10T18:20:41.441829+00:00 | 0 |
Red/mutant required failure: `FAIL POP-01:`. Fixed source bytes are recorded in each result.json; mutations were confined to separate copies, then the fixed source was rechecked.
Limit: scoped synthetic validation does not prove deployed hooks, production configuration, container health or real network behavior. Parent coordinates full gates.

Preserved boundaries: existing controlled checkout `/srv/deploy/xingmang-platform`, origin allowlist, checkout-name/Git-root/branch checks, environment names, 18088/18089 and local 8088 ports, lifecycle ordering, and test DB naming/explicit DSNs. Only project-root resolution and Compose context were adapted. No F1 root strictness or PT-02 DB naming change is included.

Normal regression: `bash tests/deploy/full-audit-pop01.test.sh` from `platform/` on Linux. Windows uses WSL with the script `/mnt/g/xingmang/01-core/platform/tests/deploy/full-audit-pop01.test.sh`. Requires bash, Python3, Git and coreutils; set a task-owned TMPDIR, GIT_ALLOW_PROTOCOL=file, GIT_TERMINAL_PROMPT=0, GIT_CONFIG_GLOBAL=/dev/null and LC_ALL=C.UTF-8. Do not set AUDIT_SOURCE for normal execution. No real Docker/Go/psql/curl process is used.

Coverage: monorepo, real sparse-checkout and standalone synthetic Git fixtures; deploy/promote; deploy-local dry-run; CI project cwd from default and explicit Git root; migration cwd; verify-real-mode default root/Compose wrapper. The production selection and three file whitelists are extracted and executed without invoking any deployment or external operation.

26 copied-source behavioral mutants all exit 1 at the stated assertions; `mutations.json` maps each mutation to its exact run, and `mutation-restoration-hashes.json` proves the working source bytes were unchanged. Final restored regression exit 0. `final-scoped-batch.json` contains all 11 owned regression runs plus the existing deploy-local-monorepo test, all exit 0.

Earlier green/green-doc-assets attempts failed on incomplete documentation changes; these logs are retained, not counted as passing checks. The supplemental red-checkout-root run also exposed test-fixture .env initialization before sparse selection; the synthetic fixture now initializes after selecting platform. No real environment file was opened. Current completed passing evidence starts at green-complete-layouts/green-production-paths and ends at restored-green plus batch-existing-monorepo.
