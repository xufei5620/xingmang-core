# INV-AUX-004

status: FIXED / TARGETED_VERIFIED / NOT_DEPLOYED

branch: ai/codex/XM-FULL-AUDIT-invoice-aux-20260911
base: `080f18e9091c121258d4f0526ac187e73ef11b77`

Capture find/sort and Docker member inspection status in the current shell before interpreting their collections. Only a successful empty scan reports empty spools; query/scan failures now reject. Preserve allowed members and pending-spool refusal.

## Verification

| Command label | UTC start | UTC end | Exit |
| --- | --- | --- | --- |
| red | 2026-09-10T17:46:49.943653+00:00 | 2026-09-10T17:46:50.516804+00:00 | 1 |
| green | 2026-09-10T17:47:27.536975+00:00 | 2026-09-10T17:47:28.062825+00:00 | 0 |
| mutation-spool-empty | 2026-09-10T17:47:28.605387+00:00 | 2026-09-10T17:47:28.797447+00:00 | 1 |
| mutation-spool-present | 2026-09-10T17:47:28.857686+00:00 | 2026-09-10T17:47:29.009425+00:00 | 1 |
| mutation-spool-find-error | 2026-09-10T17:47:29.070541+00:00 | 2026-09-10T17:47:29.242662+00:00 | 1 |
| mutation-members-allowed | 2026-09-10T17:47:29.300497+00:00 | 2026-09-10T17:47:29.420094+00:00 | 1 |
| mutation-members-unexpected | 2026-09-10T17:47:29.482036+00:00 | 2026-09-10T17:47:29.611810+00:00 | 1 |
| mutation-members-query-error | 2026-09-10T17:47:29.672786+00:00 | 2026-09-10T17:47:29.818344+00:00 | 1 |
| restored-green | 2026-09-10T17:47:29.885204+00:00 | 2026-09-10T17:47:30.383659+00:00 | 0 |

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-aux/INV-AUX-004` (`runs.jsonl`, stdout/stderr logs, copied mutants).

Changed files:

- `invoice/deploy/check-pending-spools.sh`
- `invoice/deploy/provision-projection-networks.sh`
- `invoice/scripts/tests/test_collection_producers.py`

## Preservation and limitations

No spool modification, deletion, membership policy or lifecycle order change. Six cases cover empty/present/error spool scans and allowed/unexpected/error members; six per-case copied mutants reject and fixed bytes pass again.

All find/Docker calls are bounded fakes; spool root is a fresh fixture. No actual Docker, server, database, upstream or production paths are queried. Parent owns final full gate.

Exact commit and source byte hashes are recorded in `phase2/invoice-aux/results.json`; no production or remote verification is claimed.
