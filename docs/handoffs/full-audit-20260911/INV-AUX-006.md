# INV-AUX-006

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `b3a4dbea8bde9b1933227e9caa7c26ce7d12f24b`

The invitation consumer uses jq -eRn so false ACK bindings fail. Its actual jq content predicate now runs against valid, wrong-record, wrong-manifest, wrong-line-count and malformed-time fixtures; the existing ACK test invokes these consumer checks by default and supports a consumer-only safe run.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:48:43.952547+00:00 | 2026-09-10T17:48:49.140570+00:00 | 1 |
| green | 2026-09-10T17:49:20.705145+00:00 | 2026-09-10T17:49:21.955317+00:00 | 0 |
| mutation-valid | 2026-09-10T17:49:22.614681+00:00 | 2026-09-10T17:49:22.838494+00:00 | 1 |
| mutation-wrong-record | 2026-09-10T17:49:22.930178+00:00 | 2026-09-10T17:49:23.169260+00:00 | 1 |
| mutation-wrong-hash | 2026-09-10T17:49:23.247208+00:00 | 2026-09-10T17:49:23.475945+00:00 | 1 |
| mutation-wrong-lines | 2026-09-10T17:49:23.560236+00:00 | 2026-09-10T17:49:23.774375+00:00 | 1 |
| mutation-wrong-time | 2026-09-10T17:49:23.852381+00:00 | 2026-09-10T17:49:24.097789+00:00 | 1 |
| mutation-exit-status-removed | 2026-09-10T17:49:24.184680+00:00 | 2026-09-10T17:49:24.389520+00:00 | 1 |
| restored-green | 2026-09-10T17:49:24.478977+00:00 | 2026-09-10T17:49:25.639773+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-AUX-006` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/deploy/keycloak/invite-permanent-master-admin.sh`
- `invoice/scripts/test-keycloak-offsite-ack.ps1`
- `invoice/scripts/tests/test_offsite_ack_content.py`

## Preservation and limitations

Independent signature verification, signer namespace/identity, approval policy, secret handling and maintenance lifecycle order are untouched. Five substantive assertions reject corresponding mutants; an additional removal of jq -e also rejects, followed by restored green.

Only the extracted jq predicate executes using inert ACK records; Linux jq runs via local WSL when Windows jq is unavailable. No invitation, backup, signing key or signature operation is run. Existing producer signature tests are unchanged and final full gates remain parent-coordinated.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
