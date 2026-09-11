# INV-AUX-002

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `97af6593cfa0eab89ee5658c700063be5f49a92c`

Read existing network aliases from the container's NetworkSettings.Networks endpoint, where Docker actually exposes them. Capture inspection separately so its failure propagates before alias matching. The typed Go formatter fixture executes the actual production ensure_attachment function and rejects unexpected Docker calls.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T16:39:23.957531+00:00 | 2026-09-10T16:39:25.783302+00:00 | 1 |
| green | 2026-09-10T16:39:45.904470+00:00 | 2026-09-10T16:39:47.379463+00:00 | 0 |
| mutation-valid-alias | 2026-09-10T16:40:27.067090+00:00 | 2026-09-10T16:40:28.282709+00:00 | 1 |
| mutation-missing-alias | 2026-09-10T16:40:28.351869+00:00 | 2026-09-10T16:40:29.531620+00:00 | 1 |
| mutation-query-error | 2026-09-10T16:40:29.605446+00:00 | 2026-09-10T16:40:30.910337+00:00 | 1 |
| restored-green | 2026-09-10T16:40:30.989603+00:00 | 2026-09-10T16:40:32.481722+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-AUX-002` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/deploy/provision-projection-networks.sh`
- `invoice/scripts/tests/projection-inspect-fixture.go`
- `invoice/scripts/tests/test_projection_attachment.py`

## Preservation and limitations

Allowed network/container/alias values and network connect order remain unchanged. Three substantive behavior assertions each reject a copied production mutant, then the unchanged fixed source passes again.

No Docker daemon, network change, SSH, real environment file or production operation was used. Full source gates are delegated to the parent after integration. This verifies Docker API data shape and shell error propagation, not a live Docker host.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
