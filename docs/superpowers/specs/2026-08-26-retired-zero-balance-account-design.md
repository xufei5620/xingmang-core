# Retired zero-balance account state design

## Goal

Allow a balance source account to disappear from an atomic full projection only
when its last acknowledged service-unit balance is canonical `"0"` and
`balance_negative=false`, without weakening the long-lived identity and funding
boundaries of the invoice ledger.

This change is confined to the independent source agent. It does not modify
Sub2API or New API source code, their application tables, the invoice receiver,
or any PostgreSQL projection contract.

## Security invariant

An external user ID represents one lifetime financial identity. Once a missing
zero-balance account is accepted, that ID is retired permanently for that source
balance stream. Any later appearance of the same ID fails closed before a new
snapshot, batch, watermark, or receiver event can be published.

The following removals remain errors:

- the last acknowledged service-unit balance is greater than zero;
- `balance_negative=true`;
- the previous row or state is malformed;
- a retired ID appears in a captured full projection;
- the retirement set is invalid, oversized, non-monotonic, or intersects the
  captured account rows.

## Encrypted state v2

Only `balanceReconciliationState` advances from version 1 to version 2. The
cutover manifest and balance snapshot schema versions do not change.

Version 2 adds:

```json
{
  "schema_version": 2,
  "state_kind": "balance_delta_v2",
  "base_snapshot_id": "<sha256>",
  "captured_snapshot": {},
  "emission_snapshot": {},
  "retired_zero_account_ids": ["35"]
}
```

`retired_zero_account_ids` is a strictly numeric-sorted, unique, monotonic set
of valid external IDs. It is disjoint from `captured_snapshot.rows`. Its count
is bounded independently so serialized state remains safely below the existing
8 MiB encrypted-state plaintext limit. Reaching the bound fails before the
current state file is replaced.

## State transition

For each complete atomic capture:

1. Load the acknowledged comparison snapshot and retirement set. Version 1 and
   legacy full-snapshot state migrate in memory with an empty retirement set.
2. Reject the capture if any retired ID is present, regardless of balance,
   negative flag, or baseline membership.
3. Compare the prior acknowledged rows with the capture in numeric ID order.
4. For each missing prior row, require exact zero and
   `balance_negative=false`, then add its ID to the candidate retirement set.
   Do not emit a synthetic balance checkpoint.
5. Compute ordinary new and changed balance rows.
6. Validate the complete version-2 candidate state, then atomically replace
   `balance-current.enc` with the captured snapshot, emission snapshot, and
   retirement set together.

The retirement set is never removed or reset. Prepared state whose
`base_snapshot_id` matches the committed cursor is reused byte-for-byte after a
crash or ACK loss; the source database is not recaptured. Only the exact ACK
makes the captured snapshot and retirement set the base of the next cycle.

## Migration and rollback

Empty version-1 migration is permitted only if no binary that accepted missing
zero accounts has ever prepared or acknowledged production state. Production
must prove it still runs the prior fail-closed image and that the failing cycle
did not create `pending.enc` or replace `balance-current.enc`.

Before rollout, stop the affected source agent and archive the complete state
unit: cursor, current encrypted balance state, pending spool if present,
associated key material inside the existing encrypted backup process, image ID,
and file hashes. The first successful version-2 state write is a one-way
state-format upgrade. The old image may be restored only before any version-2
state or receiver ACK is committed. After that point rollback means
forward-fixing with a compatible binary; it must not restore an older cursor or
state generation over accepted receiver sequences.

## Verification

Automated tests must cover:

- baseline and post-cutover zero/non-negative removals;
- positive and negative-marked removals remaining fail closed with state bytes
  unchanged;
- multiple head, middle, and tail removals interleaved with new and unchanged
  IDs, including numeric ordering around `9`, `10`, and `11`;
- retired-ID reappearance for zero, positive, negative, baseline and
  non-baseline forms;
- monotonic retirement across acknowledged cycles;
- version-1 and legacy state migration;
- prepared-but-unacknowledged replay and crash-before-spool replay;
- invalid, duplicate, unsorted, intersecting, and oversized retirement sets;
- encrypted-state size failure preserving the prior decryptable file;
- complete zero-record balance cycles and receiver carry-forward behavior.

Production postconditions are all ten source agents healthy on one exact signed
image, no queued/failed/dead ingest events, New API and Sub2API test identities
merged into one invoice user, both eligibility states active, no historical or
pre-policy amount claimable, and public `/readyz` returning HTTP 200.
