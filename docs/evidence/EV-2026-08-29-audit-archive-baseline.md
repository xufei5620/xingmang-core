# EV-2026-08-29: AUD1 audit archive baseline

## Scope and authority

- Task: `XM-C-AUD1-archive-format`
- Source: local staging compose project `xingmang-launch`; this is not production evidence.
- Capture mode: one PostgreSQL `REPEATABLE READ READ ONLY` transaction through the
  already-running local PostgreSQL container.
- Approval record: <https://github.com/xufei5620/xingmang-platform/pull/113#issuecomment-5455721173>
- Authorization boundary: AUD1 only. No migration, object store, provider SDK, HTTP contract,
  worker, deployment, production access, physical slimming, or merge is authorized.

No DSN, password, CredentialRef value, payload, principal ID, request ID, or key material was
read into or recorded in this evidence.

## Snapshot identity

| Fact | Value |
|---|---|
| captured at (UTC) | `2026-08-28T17:42:07.709946Z` |
| ActionRun evidence cutoff (UTC) | `2026-08-28T17:37:07.709946Z` |
| evidence-only settle window | `5 minutes` |
| PostgreSQL server version | `180006` |
| database/environment fingerprint | `sha256:740dcbc97eb4cc5fdebf8cb51250e3d75081eb3741ba07c819f23c9a59829d8c` |
| environment-set fingerprint | `sha256:ca6ea135a853543f7d101d277c025f9605ff42fe3c2944984ebc0b72ccdec0a5` |
| environment count | `3` |

The database fingerprint hashes the local PostgreSQL system identifier, database name, server
version and sorted environment IDs. The environment fingerprint hashes only the sorted IDs.
The preimage is intentionally not a connection string and contains no credential.

The five-minute settle window is used only to make this planning snapshot repeatable. AUD1 does
not freeze the future AUD3 operational settle-window policy and makes no completeness claim from
that value alone.

## Audit chain inventory

| Fact | Value |
|---|---:|
| minimum sequence | 1 |
| maximum sequence | 108 |
| event rows | 108 |
| sequence gaps | 0 |
| duplicate sequences | 0 |
| `canonical_version=1` rows | 103 |
| `canonical_version=2` rows | 5 |
| Chain Root rows | 0 |
| maximum Chain Root `to_sequence` | 0 |

The sequence/count/version/root facts exactly match the dated AUD0 planning snapshot
(`1..108`, `108`, `103/5`, `0`), so the AUD1 drift STOP did not trigger. These are still
time-scoped facts and must not be reused as current staging state by AUD2 or later slices.

## ActionRun capture reconciliation

| Fact | Value |
|---|---:|
| eligible terminal ActionRuns | 8 |
| exactly one matching audit event | 5 |
| no matching audit event | 3 |
| more than one matching audit event | 0 |

The three missing matches are all `registry.service.create@1`, status `succeeded`. This snapshot
therefore records `capture_completeness=gaps_found`; it does not reinterpret a continuous hash
chain as complete capture. AUD1 preserves these counts in manifest wire fields but does not repair
or hide the historical gap.

## Fixed read-only query shape

The capture used one read-only repeatable-read transaction and only these aggregate shapes:

```sql
SELECT min(sequence), max(sequence), count(*),
       max(sequence) - min(sequence) + 1 - count(*),
       count(*) - count(DISTINCT sequence)
FROM audit.audit_event;

SELECT canonical_version, count(*)
FROM audit.audit_event
GROUP BY canonical_version
ORDER BY canonical_version;

SELECT count(*), coalesce(max(to_sequence), 0)
FROM audit.chain_root;

WITH eligible AS (
  SELECT id
  FROM action.action_run
  WHERE finished_at <= transaction_timestamp() - interval '5 minutes'
), joined AS (
  SELECT eligible.id, count(audit.audit_event.id) AS audit_count
  FROM eligible
  LEFT JOIN audit.audit_event ON audit.audit_event.action_run_id = eligible.id
  GROUP BY eligible.id
)
SELECT count(*),
       count(*) FILTER (WHERE audit_count = 1),
       count(*) FILTER (WHERE audit_count = 0),
       count(*) FILTER (WHERE audit_count > 1)
FROM joined;
```

## Limitations

- This is an inventory snapshot, not a signed archive, Chain Root, recovery proof, backup, RPO/RTO
  claim, or production activation record.
- No database row was written and no existing audit event or legacy Chain Root export marker was
  changed.
- Later slices must create a fresh snapshot from a base no older than that slice.
