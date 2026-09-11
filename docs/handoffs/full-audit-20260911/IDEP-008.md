# IDEP-008 — consume complete Docker logs before evaluating startup markers

- status: fixed / targeted verification passed / not deployed
- base: `97af6593cfa0eab89ee5658c700063be5f49a92c`
- scope: roll-forward API, restore/shadow PostgreSQL and basic-scope PostgreSQL startup predicates.

`grep -q` exited after the matching marker, closing a pipe while Docker still wrote later logs. With pipefail this falsely rejected a healthy startup. Each affected predicate now consumes the full stream and redirects matched output to `/dev/null`. Genuine producer failure and absent markers still reject; lifecycle order, timeouts and recovery behavior are unchanged.

`invoice/scripts/test-deploy-log-markers.sh` extracts only the real predicates and supplies an in-process fake Docker. It is wired into `verify.ps1`; no deployment wrapper or daemon is executed.

Evidence: `G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/IDEP-008/`.

- Red: five present-marker cases failed with exit 141; `red.json` records native exit 1 and UTC.
- Green: 2026-09-10 16:40:37.140926Z–16:40:40.625990Z, exit 0, 15 cases.
- Mutations: all 15 case-specific changes rejected at their intended assertion (early-exit grep, ignored absent marker, ignored producer failure).
- Restored green: exit 0; exact UTC times and source SHA256 per run are in `proof.json`.

After the account-switch continuation, all six fixed source/test hashes were checked against saved proof and matched. Full suites, production, keys, env files and servers were not accessed.
