# INV-PG-001

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `6e2514199b440575b7bc61bda45c3ff5fb5ec4e9`

The concurrent-index EXIT finalizer now preserves incoming failure and rejects failed source hashes, result/manifest writes, permission tightening or sync. It marks retained evidence failed and regenerates its checksum manifest on a best-effort basis without ever restoring a successful exit.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:51:01.344494+00:00 | 2026-09-10T17:51:05.346278+00:00 | 1 |
| green | 2026-09-10T17:51:34.812456+00:00 | 2026-09-10T17:51:40.747890+00:00 | 0 |
| mutation-healthy | 2026-09-10T17:52:12.897106+00:00 | 2026-09-10T17:52:13.567459+00:00 | 1 |
| mutation-compose-hash | 2026-09-10T17:52:13.638704+00:00 | 2026-09-10T17:52:14.126092+00:00 | 1 |
| mutation-env-hash | 2026-09-10T17:52:14.213244+00:00 | 2026-09-10T17:52:14.794397+00:00 | 1 |
| mutation-migration-hash | 2026-09-10T17:52:14.875212+00:00 | 2026-09-10T17:52:15.370125+00:00 | 1 |
| mutation-result-write | 2026-09-10T17:52:15.458429+00:00 | 2026-09-10T17:52:15.953853+00:00 | 1 |
| mutation-manifest-write | 2026-09-10T17:52:16.038001+00:00 | 2026-09-10T17:52:16.482096+00:00 | 1 |
| mutation-permission | 2026-09-10T17:52:16.561822+00:00 | 2026-09-10T17:52:17.037807+00:00 | 1 |
| mutation-sync | 2026-09-10T17:52:17.118404+00:00 | 2026-09-10T17:52:17.645401+00:00 | 1 |
| mutation-incoming-failure | 2026-09-10T17:52:17.730025+00:00 | 2026-09-10T17:52:18.537355+00:00 | 1 |
| mutation-retained-failure-status | 2026-09-10T17:52:18.619147+00:00 | 2026-09-10T17:52:19.322688+00:00 | 1 |
| restored-green | 2026-09-10T17:52:49.829641+00:00 | 2026-09-10T17:52:55.743203+00:00 | 0 |
| scoped-static-gate | 2026-09-10T17:52:55.827546+00:00 | 2026-09-10T17:52:57.009387+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-PG-001` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/deploy/postgres/apply-source-readiness-index-concurrently.sh`
- `invoice/scripts/tests/test_index_finalizer.py`

## Preservation and limitations

Only finalizer error/evidence handling changed. SQL, schema, index policy, producer versions and database lifecycle order remain unchanged. Healthy, seven evidence failure modes and incoming exit37 are verified; ten mutations separately prove rejection and retained failure status, followed by restored green and the scoped static operator gate.

Only the extracted EXIT function runs on newly created synthetic files. chmod/sync are fakes, no Docker/PSQL/SSH is reachable, and no real env/credential content is read. Permanent filesystem failure can prevent retaining complete evidence, but now exits nonzero; the test covers an initial result/manifest write failure that permits a failure marker retry. No production persistence claim.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
