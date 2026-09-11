# INV-WIRE-01

status: fixed; local targeted validation only
branch: ai/codex/XM-FULL-AUDIT-contracts-20260911
base: 97af6593cfa0eab89ee5658c700063be5f49a92c

## Change

Add real Draft 2020-12 validation of all three published source-agent examples to verify.ps1 using existing PowerShell Test-Json. In-memory bundling rebases only reviewed local references and refuses external references before validation, without changing published schemas. Add a focused schema regression script invoked by the existing gate.

## Verification

| Stage | Start UTC | End UTC | exit / expected |
| --- | --- | --- | --- |
| red-old-oracle | 2026-09-10T17:49:22.952755+00:00 | 2026-09-10T17:49:23.545992+00:00 | 0 / 1 |
| green-fixed | 2026-09-10T17:51:19.370509+00:00 | 2026-09-10T17:51:20.191687+00:00 | 0 / 0 |
| mutate-original-event-schema | 2026-09-10T17:51:20.194005+00:00 | 2026-09-10T17:51:21.105051+00:00 | 1 / 1 |
| green-schema-regressions | 2026-09-10T17:51:21.107595+00:00 | 2026-09-10T17:51:24.009112+00:00 | 1 / 0 |
| green-schema-regressions-r2 | 2026-09-10T17:51:58.632364+00:00 | 2026-09-10T17:52:01.895872+00:00 | 1 / 0 |
| green-schema-regressions-r3 | 2026-09-10T17:52:39.059430+00:00 | 2026-09-10T17:52:44.351570+00:00 | 0 / 0 |
| mutate-helper-skip-v1 | 2026-09-10T17:52:44.356036+00:00 | 2026-09-10T17:52:46.689704+00:00 | 1 / 1 |
| mutate-helper-skip-v2 | 2026-09-10T17:52:46.693154+00:00 | 2026-09-10T17:52:49.345088+00:00 | 1 / 1 |
| mutate-helper-skip-v3 | 2026-09-10T17:52:49.348684+00:00 | 2026-09-10T17:52:53.148401+00:00 | 1 / 1 |
| mutate-helper-reject-valid | 2026-09-10T17:52:53.152493+00:00 | 2026-09-10T17:52:54.557363+00:00 | 1 / 1 |
| mutate-helper-allow-external | 2026-09-10T17:52:54.562024+00:00 | 2026-09-10T17:53:00.370934+00:00 | 1 / 1 |
| restored-green | 2026-09-10T17:53:00.373785+00:00 | 2026-09-10T17:53:01.245144+00:00 | 0 / 0 |
| mutate-helper-ignore-missing-ref | 2026-09-10T17:53:37.855469+00:00 | 2026-09-10T17:53:42.616466+00:00 | 1 / 1 |
| restored-schema-regressions | 2026-09-10T17:53:42.618896+00:00 | 2026-09-10T17:53:47.880681+00:00 | 0 / 0 |

The pre-fix extracted V3 gate survives the wrong usage_event schema (child exit 0; meta regression exit 1). New schema validation rejects that exact retained fixture. Valid v1/v2/v3 examples pass; isolated type/required/event/missing-ref/external-ref corruptions are rejected. Copied validator mutants skipping each version, rejecting valid data, or masking bad references make the regression fail. Two early selftest runs failed due incorrect fixture allOf paths; corrected runs pass and those setup failures are retained, not counted as mutation evidence.

Every newly added substantive check has a corresponding retained mutation; exact commands, source SHA256, expected failure diagnostics, and restoration checks are in `G:\xingmang\logs\full-audit-20260910\phase2\contracts\INV-WIRE-01`.

## Preserved behavior and limits

No schema, business algorithm, producer, decoder, dependency or signature artifact changed. Cross-document V2 references are converted to equivalent local pointers in memory so no schema download is required. Existing Go validator and structural gates remain. Only the new pure schema scripts and extracted old block were run, not full verify.ps1.

No real keyring, environment, database, external provider, server, CPA operation, or full suite was executed.
