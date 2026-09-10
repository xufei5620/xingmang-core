# IDEP-002 — bind backup resources to installed Compose mounts; reject stale generations with mutation proof

- status: fixed / targeted validation passed / not deployed
- audit base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

Before freeze, the same Compose/env scope must identify exactly one installed container per source service and API. Service/config-file labels, ten physical state paths, eight economic cutover paths, and the API document volume must match the reviewed resources. Existing stopped services remain supported without starting them. The runbook now requires the reviewed generation explicitly. Final 37-case regression passes; 37 behavior mutations fail; restored regression and both syntax checks pass. lifecycle-preservation.json proves the freeze-through-publication/resume suffix plus running-state capture and resume functions remain byte-identical after LF normalization. proof-supplement.json distinguishes valid baseline RED evidence from retained intermediate fixture/setup failures. No real Docker, secret, env, database, backup or restore operation executed.

Evidence directory: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-002`. Commands, duration, expected failure patterns and exact source SHA256 are in `proof.json` and per-command JSON/logs.

| Phase | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red-state-generation-corrected | 2026-09-10T18:16:28.302589+00:00 | 2026-09-10T18:16:29.161976+00:00 | 1 |
| red-doc-current-generation-final | 2026-09-10T18:24:14.244400+00:00 | 2026-09-10T18:24:14.489861+00:00 | 1 |
| green-complete-final | 2026-09-10T18:24:14.491718+00:00 | 2026-09-10T18:24:53.504581+00:00 | 0 |
| mutation-1-valid | 2026-09-10T18:24:53.510862+00:00 | 2026-09-10T18:24:54.754590+00:00 | 1 |
| mutation-2-stopped | 2026-09-10T18:24:54.819982+00:00 | 2026-09-10T18:24:55.416331+00:00 | 1 |
| mutation-3-canonical-input | 2026-09-10T18:24:55.490065+00:00 | 2026-09-10T18:24:56.109423+00:00 | 1 |
| mutation-4-canonical-mount | 2026-09-10T18:24:56.177190+00:00 | 2026-09-10T18:24:56.929694+00:00 | 1 |
| mutation-5-document-mismatch | 2026-09-10T18:24:56.999870+00:00 | 2026-09-10T18:24:59.126356+00:00 | 1 |
| mutation-6-mount-missing | 2026-09-10T18:24:59.196490+00:00 | 2026-09-10T18:25:00.849903+00:00 | 1 |
| mutation-7-mount-duplicate | 2026-09-10T18:25:00.920555+00:00 | 2026-09-10T18:25:03.063606+00:00 | 1 |
| mutation-8-mount-type | 2026-09-10T18:25:03.129344+00:00 | 2026-09-10T18:25:05.330646+00:00 | 1 |
| mutation-9-mount-path-missing | 2026-09-10T18:25:05.396413+00:00 | 2026-09-10T18:25:06.977934+00:00 | 1 |
| mutation-10-inspect-failure | 2026-09-10T18:25:07.040872+00:00 | 2026-09-10T18:25:08.608669+00:00 | 1 |
| mutation-11-container-missing | 2026-09-10T18:25:08.676966+00:00 | 2026-09-10T18:25:09.498604+00:00 | 1 |
| mutation-12-container-duplicate | 2026-09-10T18:25:09.567301+00:00 | 2026-09-10T18:25:11.951392+00:00 | 1 |
| mutation-13-container-query-failure | 2026-09-10T18:25:12.020795+00:00 | 2026-09-10T18:25:12.909675+00:00 | 1 |
| mutation-14-service-label | 2026-09-10T18:25:12.976770+00:00 | 2026-09-10T18:25:15.051044+00:00 | 1 |
| mutation-15-config-label | 2026-09-10T18:25:15.116347+00:00 | 2026-09-10T18:25:17.747203+00:00 | 1 |
| mutation-16-label-query-failure | 2026-09-10T18:25:17.809946+00:00 | 2026-09-10T18:25:18.778428+00:00 | 1 |
| mutation-17-state-sub2api-payments | 2026-09-10T18:25:18.842326+00:00 | 2026-09-10T18:25:20.929240+00:00 | 1 |
| mutation-18-cutover-sub2api-payments | 2026-09-10T18:25:20.990899+00:00 | 2026-09-10T18:25:23.238026+00:00 | 1 |
| mutation-19-state-sub2api-identities | 2026-09-10T18:25:23.300991+00:00 | 2026-09-10T18:25:25.614855+00:00 | 1 |
| mutation-20-state-sub2api-usage | 2026-09-10T18:25:25.684117+00:00 | 2026-09-10T18:25:28.078461+00:00 | 1 |
| mutation-21-cutover-sub2api-usage | 2026-09-10T18:25:28.141471+00:00 | 2026-09-10T18:25:30.446022+00:00 | 1 |
| mutation-22-state-sub2api-credits | 2026-09-10T18:25:30.524053+00:00 | 2026-09-10T18:25:32.633344+00:00 | 1 |
| mutation-23-cutover-sub2api-credits | 2026-09-10T18:25:32.692139+00:00 | 2026-09-10T18:25:34.907878+00:00 | 1 |
| mutation-24-state-sub2api-balances | 2026-09-10T18:25:34.976781+00:00 | 2026-09-10T18:25:37.397561+00:00 | 1 |
| mutation-25-cutover-sub2api-balances | 2026-09-10T18:25:37.460005+00:00 | 2026-09-10T18:25:39.674638+00:00 | 1 |
| mutation-26-state-newapi-payments | 2026-09-10T18:25:39.739589+00:00 | 2026-09-10T18:25:41.774866+00:00 | 1 |
| mutation-27-cutover-newapi-payments | 2026-09-10T18:25:41.834320+00:00 | 2026-09-10T18:25:43.795427+00:00 | 1 |
| mutation-28-state-newapi-identities | 2026-09-10T18:25:43.855877+00:00 | 2026-09-10T18:25:45.740171+00:00 | 1 |
| mutation-29-state-newapi-usage | 2026-09-10T18:25:45.798593+00:00 | 2026-09-10T18:25:47.924763+00:00 | 1 |
| mutation-30-cutover-newapi-usage | 2026-09-10T18:25:47.983748+00:00 | 2026-09-10T18:25:50.163870+00:00 | 1 |
| mutation-31-state-newapi-credits | 2026-09-10T18:25:50.227399+00:00 | 2026-09-10T18:25:52.416020+00:00 | 1 |
| mutation-32-cutover-newapi-credits | 2026-09-10T18:25:52.475952+00:00 | 2026-09-10T18:25:54.568482+00:00 | 1 |
| mutation-33-state-newapi-balances | 2026-09-10T18:25:54.628490+00:00 | 2026-09-10T18:25:56.851727+00:00 | 1 |
| mutation-34-cutover-newapi-balances | 2026-09-10T18:25:56.914077+00:00 | 2026-09-10T18:25:59.036966+00:00 | 1 |
| mutation-35-docs-valid | 2026-09-10T18:25:59.099719+00:00 | 2026-09-10T18:25:59.327790+00:00 | 1 |
| mutation-36-docs-missing-state | 2026-09-10T18:25:59.336819+00:00 | 2026-09-10T18:25:59.582200+00:00 | 1 |
| mutation-37-docs-missing-cutover | 2026-09-10T18:25:59.590011+00:00 | 2026-09-10T18:25:59.846961+00:00 | 1 |
| restored-green-complete | 2026-09-10T18:25:59.853614+00:00 | 2026-09-10T18:26:39.366501+00:00 | 0 |
| syntax-backup | 2026-09-10T18:26:39.368795+00:00 | 2026-09-10T18:26:39.400441+00:00 | 0 |
| syntax-test | 2026-09-10T18:26:39.402445+00:00 | 2026-09-10T18:26:39.433254+00:00 | 0 |

Validation uses extracted code boundaries and synthetic fixtures only; no full suite, production wrapper, daemon, server, real key/env, upstream, CPA, schema or dependency operation. Existing failure evidence is retained.
