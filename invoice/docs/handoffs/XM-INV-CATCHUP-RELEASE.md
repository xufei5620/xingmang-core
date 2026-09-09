# XM-INV-CATCHUP-RELEASE: release an account from its catch-up whatever status it reached

- **status:** implemented on the release branch, gated locally (backend
  build/vet, full `go test -p 1 -count=1 ./...` against real PostgreSQL). Not
  yet released.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).
- **found in production**, 2026-09-04, from a question the product owner asked
  about one account whose invoiceable amount looked far too small.

## Symptom

Account `12` (Sub2API) showed 起点后消耗 ¥5.46 in the operator ledger while its
real post-policy-start usage was ¥55.78, and its upstream balance had fallen
from ¥60 of recharges to ¥4.22. Its status read 对账中暂不可开票 and never
changed. Every other account in production had a `finalized_through` a few
minutes old; this one was frozen at 2026-09-01 12:14:31 and its last
projection had run on 2026-09-03 11:58:38.

## Root cause

`completeEligibilityCatchupTx` released only the accounts still marked
`syncing`:

```sql
WHERE source_instance_id=$1 AND catchup_key_hmac=$2 AND eligibility_status='syncing'
```

An account can leave `syncing` **during its own catch-up**. The POLICY_ANCHOR
bootstrap parks one in `not_invoiceable_pending_reconciliation` the moment its
triggering balance checkpoint reports a negative balance, which is exactly what
happened here (four of this account's 726 checkpoints are flagged negative).
Such an account no longer matched the filter, so its `catchup_key_hmac` was
never cleared.

That key is what `finalizeSourceAccountsTx` excludes an account on
(`WHERE ... eas.catchup_key_hmac IS NULL`). Keeping it means the account is
excluded from finalization **permanently**:

- no `eligibility_projection_jobs` row is ever enqueued for it;
- its balance checkpoints stay `pending_finalization` and are never evaluated;
- `finalized_through` stops advancing;
- the self-clearing pending-reconciliation state can never reach its two
  consecutive matches, because there are no evaluated checkpoints to match;
- consumption stops being allocated, so the invoiceable amount an operator sees
  freezes at whatever the last projection wrote.

None of that surfaces as an error. The account simply stops moving, which is
why it took a human noticing an implausible number to find it.

## Fix

The completion now selects every account still carrying the catch-up key,
regardless of status, and drops the key from all of them.

The status itself is only decided for accounts that are still `syncing`
(active, or frozen when an open freeze exists). Any other status was set by a
rule that owns it -- a freeze, or the pending-reconciliation state -- and
completing a catch-up is not evidence about that rule, so it is left exactly as
it stands. The audit row records whether the status was decided here.

No migration. No change to the finalization query itself: the exclusion is
correct, it was the release that was too narrow.

## Test

`TestCatchupCompletionReleasesAnAccountThatLeftSyncing`
(`postgresstore`, integration, real PostgreSQL): an account enters a catch-up
`syncing`, moves to `not_invoiceable_pending_reconciliation` mid-flight with
the paired columns migration 0020 requires, and the catch-up then completes.
The test requires the exclusion key to be gone **and** the status to be
untouched. It fails on the old code at the first assertion.

`TestParkedIdentityFactCanPublishAndCatchUpAfterBinding` still passes: an
account that stays `syncing` is still promoted to active on completion.

## Production follow-up

The one affected account stays stuck until this ships: the fix repairs the
release path, it does not retroactively clear a key that is already set. After
deploying, clear it once for that account so the normal machinery resumes --
`catchup_key_hmac` is set to NULL by the same UPDATE the completion runs, and
the account then finalizes, its checkpoints evaluate, and either the pending
state self-clears or it surfaces a real reconciliation difference to act on.
