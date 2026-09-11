# INV-WIRE-02

status: fixed; local targeted validation only
branch: ai/codex/XM-FULL-AUDIT-contracts-20260911
base: 97af6593cfa0eab89ee5658c700063be5f49a92c

## Change

Reject a non-array records value and require an actual boolean for V3 scan_complete before the decoded zero values can be treated as a legitimate batch. Keep the V2 shape and all valid empty/completion/first-hash cases. Add pure decoder tests and direct unsigned published-example controls.

## Verification

| Stage | Start UTC | End UTC | exit / expected |
| --- | --- | --- | --- |
| red-null-types | 2026-09-10T17:54:54.188880+00:00 | 2026-09-10T17:55:07.041911+00:00 | 1 / 1 |
| green-fixed | 2026-09-10T17:55:49.436802+00:00 | 2026-09-10T17:55:50.909099+00:00 | 0 / 0 |
| mutate-allow-null-records | 2026-09-10T17:56:47.513899+00:00 | 2026-09-10T17:56:49.021076+00:00 | 1 / 1 |
| mutate-allow-null-completion | 2026-09-10T17:56:49.025348+00:00 | 2026-09-10T17:56:50.489059+00:00 | 1 / 1 |
| mutate-reject-empty-array | 2026-09-10T17:56:50.493382+00:00 | 2026-09-10T17:56:51.899366+00:00 | 1 / 1 |
| mutate-reject-false | 2026-09-10T17:56:51.903047+00:00 | 2026-09-10T17:56:53.177134+00:00 | 1 / 1 |
| mutate-reject-true | 2026-09-10T17:56:53.180919+00:00 | 2026-09-10T17:56:54.453608+00:00 | 1 / 1 |
| mutate-require-v3-field-in-v2 | 2026-09-10T17:56:54.457328+00:00 | 2026-09-10T17:56:55.767471+00:00 | 1 / 1 |
| mutate-reject-first-null-hash | 2026-09-10T17:56:55.771764+00:00 | 2026-09-10T17:56:57.039394+00:00 | 1 / 1 |
| mutate-normalize-records | 2026-09-10T17:56:57.043083+00:00 | 2026-09-10T17:56:58.243619+00:00 | 1 / 1 |
| mutate-normalize-scan_complete | 2026-09-10T17:56:58.253652+00:00 | 2026-09-10T17:56:59.463678+00:00 | 1 / 1 |
| mutate-output-records | 2026-09-10T17:56:59.474729+00:00 | 2026-09-10T17:57:00.771101+00:00 | 1 / 1 |
| mutate-output-completion | 2026-09-10T17:57:00.774843+00:00 | 2026-09-10T17:57:02.010689+00:00 | 1 / 1 |
| mutate-output-hash | 2026-09-10T17:57:02.014814+00:00 | 2026-09-10T17:57:03.257184+00:00 | 1 / 1 |
| restored-green | 2026-09-10T17:57:03.258946+00:00 | 2026-09-10T17:57:04.408057+00:00 | 0 / 0 |
| green-with-published-examples | 2026-09-10T17:58:04.059169+00:00 | 2026-09-10T17:58:05.355698+00:00 | 0 / 0 |
| mutate-reject-nonempty | 2026-09-10T17:58:05.359708+00:00 | 2026-09-10T17:58:06.670428+00:00 | 1 / 1 |
| restored-green-with-published-examples | 2026-09-10T17:58:06.676259+00:00 | 2026-09-10T17:58:07.836530+00:00 | 0 / 0 |

Before the change, the new tests fail specifically because records:null and V3 scan_complete:null are accepted; legal controls already pass. Both missing type-check overlays now fail the intended null assertions. Boundary-normalization mutants fail every missing/null/string/number/object case; reject-valid and changed-output mutants exercise empty arrays, false/true, V2 omission and first null hash preservation. Both unsigned published nonempty examples pass and a reject-nonempty mutant fails both. Final restored targeted suite exits 0.

Every newly added substantive check has a corresponding retained mutation; exact commands, source SHA256, expected failure diagnostics, and restoration checks are in `G:\xingmang\logs\full-audit-20260910\phase2\contracts\INV-WIRE-02`.

## Preserved behavior and limits

Only the decode boundary changed (five added/modified lines); producer, schema, source events, algorithms, lifecycle ordering, signature verification and databases are unchanged. The prior producer supplement proves current legitimate empty paths emit [] and boolean, while immutable RawBody is replayed; no producer change is needed. V2 scan_complete omission, records:[], V3 false/true and first previous_batch_hash:null remain legal. Historical deployed spool contents and third-party producers were not inspected.

No real keyring, environment, database, external provider, server, CPA operation, or full suite was executed.
