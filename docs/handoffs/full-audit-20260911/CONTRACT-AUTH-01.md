# CONTRACT-AUTH-01

status: fixed; local targeted validation only
branch: ai/codex/XM-FULL-AUDIT-contracts-20260911
base: 97af6593cfa0eab89ee5658c700063be5f49a92c

## Change

Use an independent test-only claims/header shape, strict JSON decoding and literal frozen audience/ACR/lifetime/nonce expectations. Production signing and verification are unchanged.

## Verification

| Stage | Start UTC | End UTC | exit / expected |
| --- | --- | --- | --- |
| red-old-oracle | 2026-09-10T16:40:27.283647+00:00 | 2026-09-10T16:40:45.974118+00:00 | 0 / 1 |
| green-meta-oracle | 2026-09-10T16:41:41.927042+00:00 | 2026-09-10T16:41:43.680551+00:00 | 1 / 1 |
| green-fixed | 2026-09-10T17:43:06.419434+00:00 | 2026-09-10T17:43:08.148887+00:00 | 0 / 0 |
| mutate-field-alg | 2026-09-10T17:43:08.153281+00:00 | 2026-09-10T17:43:09.780346+00:00 | 1 / 1 |
| mutate-field-kid | 2026-09-10T17:43:09.783940+00:00 | 2026-09-10T17:43:11.477964+00:00 | 1 / 1 |
| mutate-field-typ | 2026-09-10T17:43:11.482068+00:00 | 2026-09-10T17:43:13.092235+00:00 | 1 / 1 |
| mutate-field-iss | 2026-09-10T17:43:13.095433+00:00 | 2026-09-10T17:43:14.712625+00:00 | 1 / 1 |
| mutate-field-aud | 2026-09-10T17:43:14.716791+00:00 | 2026-09-10T17:43:16.327597+00:00 | 1 / 1 |
| mutate-field-sub | 2026-09-10T17:43:16.331563+00:00 | 2026-09-10T17:43:17.883291+00:00 | 1 / 1 |
| mutate-field-username | 2026-09-10T17:43:17.887134+00:00 | 2026-09-10T17:43:19.487059+00:00 | 1 / 1 |
| mutate-field-roles | 2026-09-10T17:43:19.490697+00:00 | 2026-09-10T17:43:21.070650+00:00 | 1 / 1 |
| mutate-field-amr | 2026-09-10T17:43:21.074677+00:00 | 2026-09-10T17:43:22.846128+00:00 | 1 / 1 |
| mutate-field-scope | 2026-09-10T17:43:22.849732+00:00 | 2026-09-10T17:43:24.809952+00:00 | 1 / 1 |
| mutate-field-nonce | 2026-09-10T17:43:24.814793+00:00 | 2026-09-10T17:43:26.874556+00:00 | 1 / 1 |
| mutate-field-iat | 2026-09-10T17:43:26.878881+00:00 | 2026-09-10T17:43:28.804000+00:00 | 1 / 1 |
| mutate-field-nbf | 2026-09-10T17:43:28.808023+00:00 | 2026-09-10T17:43:30.633406+00:00 | 1 / 1 |
| mutate-field-exp | 2026-09-10T17:43:30.637125+00:00 | 2026-09-10T17:43:32.471973+00:00 | 1 / 1 |
| mutate-acr | 2026-09-10T17:43:32.476203+00:00 | 2026-09-10T17:43:34.496917+00:00 | 1 / 1 |
| mutate-audience | 2026-09-10T17:43:34.501510+00:00 | 2026-09-10T17:43:36.351523+00:00 | 1 / 1 |
| mutate-lifetime | 2026-09-10T17:43:36.355198+00:00 | 2026-09-10T17:43:38.191731+00:00 | 1 / 1 |
| mutate-nonce-length | 2026-09-10T17:43:38.196039+00:00 | 2026-09-10T17:43:40.103696+00:00 | 1 / 1 |
| mutate-kid-value | 2026-09-10T17:43:40.108422+00:00 | 2026-09-10T17:43:42.083786+00:00 | 1 / 1 |
| mutate-iat-value | 2026-09-10T17:43:42.087770+00:00 | 2026-09-10T17:43:43.920881+00:00 | 1 / 1 |
| mutate-exp-value | 2026-09-10T17:43:43.924800+00:00 | 2026-09-10T17:43:45.732352+00:00 | 1 / 1 |
| restored-green | 2026-09-10T17:43:45.734162+00:00 | 2026-09-10T17:43:47.164786+00:00 | 0 / 0 |

The pre-fix meta regression expected the existing signer test to reject the acr_drift producer overlay. The child survived with exit 0 and the meta command exited 1. After the test-only fix the same child exits 1 for unknown field, so the meta command exits 0. Twenty-one additional field/value overlays fail at the intended checks; current bytes remain unchanged and restored targeted signer suite exits 0.

Every newly added substantive check has a corresponding retained mutation; exact commands, source SHA256, expected failure diagnostics, and restoration checks are in `G:\xingmang\logs\full-audit-20260910\phase2\contracts\CONTRACT-AUTH-01`.

## Preserved behavior and limits

No production source, schema, auth policy, key material or dependency changed. Only ephemeral in-memory keys are used by existing signer tests. Actual real-manifest test is excluded. The fixed test still verifies the real signature but does not pretend to run online invoice authentication.

No real keyring, environment, database, external provider, server, CPA operation, or full suite was executed.
