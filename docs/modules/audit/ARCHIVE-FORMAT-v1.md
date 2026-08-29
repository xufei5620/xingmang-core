# Audit Archive Format v1

> Status: AUD1 local/offline format and verifier. This document does not authorize an object
> store, migration, RecoveryIndex publication, worker, HTTP contract, staging/production archive,
> restore, physical slimming, or deletion of hot audit history.

## Invariants

- `audit.audit_event` remains append-only and copy-only. Archive v1 never updates, deletes,
  truncates, drops, detaches, or rehashes an event.
- The authoritative payload follows the one global sequence chain. Environment projections are
  query aids and never become independent evidence chains.
- Every payload row carries its persisted `canonical_version`. Missing or zero is format-invalid;
  an unknown future value is the typed compatibility result `verifier_outdated`.
- UTF-8, no BOM, no insignificant whitespace, fixed field order, and exactly one trailing LF are
  part of the signed bytes.
- Runtime `audit.Event`, database rows, provider SDK types, and generic maps are not signed wire
  types.

## Event payload NDJSON

Media type: `application/x-ndjson; charset=utf-8`.

Each line contains these fields exactly once and in this order:

```text
id, sequence, occurred_at, recorded_at,
principal_id, principal_type,
action_id, action_version, action_run_id,
resource_type, resource_id, environment,
reason, approval_id, request_id, trace_id, source_ip,
before_summary, after_summary,
connector_request_summary, connector_response_summary,
result, compensation_result,
prev_hash, event_hash, canonical_version
```

`occurred_at` and `recorded_at` are UTC with exactly six fractional digits. Summary object keys
are byte-sorted. Unknown, duplicate, missing, null, misordered, wrong-typed and non-canonical JSON
is rejected before mapping to `audit.Event`. The verifier then recomputes `event_hash` with the
row's explicit frozen v1 or v2 canonical implementation.

Segments are closed global-sequence intervals. A segment cuts before exceeding 25,000 rows or
64 MiB of uncompressed payload, never inside a line. One row larger than 64 MiB is a one-row
segment with `oversized_record_count=1`. A missing final LF, an extra blank line, or EOF inside a
JSON token is `truncated_object` / format-invalid.

## Environment projection NDJSON

Each environment receives only rows belonging to it and only these current HTTP fields:

```text
sequence, occurred_at, principal_id, principal_type,
action_id, action_version, action_run_id,
resource_type, resource_id, environment, request_id,
result, error_code, before_summary, after_summary,
event_hash, prev_hash
```

The projection must not contain `reason`, `approval_id`, `trace_id`, `source_ip`, either connector
summary, `recorded_at`, `canonical_version`, object metadata, KMS metadata, or payload locators.
Its trust comes from the signed manifest hash and exact sequence mapping to the payload.

## Frozen artifact wire

All artifacts use canonical JSON plus one trailing LF. `ChainRootRefV1` is the sole envelope
exception: it directly carries the existing root signature fields in this order:

```text
id, computed_at, from_sequence, to_sequence, root_hash, signature, key_id
```

It never carries `public_key`, `signed_payload`, `exported_at`, or `export_target`.

`ObjectVersionV1` field order:

```text
bucket_id, key, version_id, sha256, size_bytes, content_type,
provider_checksum, etag, encryption_mode, kms_key_id,
object_lock_mode, retain_until, row_count
```

Production artifacts must later prove exact VersionID readback, `SSE-KMS`, the approved KMS key,
Object Lock mode, and retain-until. AUD1's `local-fixture` descriptor is explicitly
`LOCAL-ONLY`/`NONE`, is never a provider/WORM claim, and cannot be used by AUD2+, a Checkpoint, or
RecoveryIndex as committed evidence. Because the AUD1 local source interface does not perform the
future AUD3 ActionRun reconciliation, an all-zero local manifest count set is reported as
`capture_completeness=not_checked`, never as verified.

`PreviousManifestRefV1` and `PreviousCheckpointRefV1` field order:

```text
kind, bucket_id, key, version_id, sha256
```

The first manifest/checkpoint uses its exact published genesis hash and empty locator strings.
Later artifacts require the complete bucket/key/VersionID/SHA tuple; hash-only, latest, List, or a
caller-supplied predecessor is invalid.

`ManifestV1` unsigned field order:

```text
kind, format_version, exporter_version, exporter_commit,
from_sequence, to_sequence, row_count,
first_prev_hash, first_event_hash, last_event_hash,
previous_manifest, canonical_version_counts, oversized_record_count,
payload, projections, chain_root,
created_at, source_tip_observed_at, action_run_snapshot_at,
eligible_action_run_count, matched_action_run_count,
missing_audit_event_count, duplicate_audit_event_count
```

`CheckpointV1` unsigned field order:

```text
kind, format_version, generation, previous_checkpoint,
first_manifest, terminal_manifest,
from_sequence, to_sequence, manifest_count, catalog_rows_digest,
chain_root, canonical_version_counts,
chain_integrity, capture_completeness,
eligible_action_run_count, matched_action_run_count,
missing_audit_event_count, duplicate_audit_event_count,
source_tip_sequence, source_tip_observed_at,
approval_envelope_sha256, operation_intent_digest, created_at
```

`RecoveryIndexV1` unsigned field order:

```text
kind, format_version, generation, previous_generation, previous_index_sha256,
checkpoint, terminal_manifest, terminal_sequence, terminal_root_hash, updated_at
```

Manifest, Checkpoint, and RecoveryIndex all use exactly this outer envelope:

```text
unsigned, unsigned_sha256, signature_algorithm="Ed25519",
signature_key_id, signature
```

Checked-in literal event/root/manifest/checkpoint/index bytes and SHA-256 values are the
compatibility authority. Tests never derive expected golden bytes with the encoder under test.

## Signature trust

Signatures are verified only with an independently supplied trusted keyring record containing:

```text
key_id, algorithm, public_key, fingerprint,
purpose, protocol, valid_from, valid_until, revoked_at, revoke_reason
```

The verifier recomputes the public-key fingerprint, requires the signature instant in
`[valid_from, valid_until)`, applies revocation, and requires exact purpose/protocol. The same raw
key material cannot serve two purposes.

The existing Chain Root domain remains `audit-chain-root/v1`. Other signatures use:

```text
<domain>\nsha256=<unsigned_sha256>\n
```

Domains are `xm-audit-archive-manifest-v1`, `xm-audit-archive-checkpoint-v1`,
`xm-audit-recovery-index-v1`, and the separately reserved PLO/Kill-Switch/Scrub domains. A valid
signature in one domain must fail in every other domain.

## Verification results

Deterministic evidence failures use `VerificationReport` codes such as:

```text
ok, object_hash_mismatch, manifest_signature_invalid, manifest_gap,
sequence_gap, broken_link, event_hash_mismatch,
root_signature_invalid, archive_format_invalid
```

Unknown archive/canonical versions never become integrity codes; they return typed
`CompatibilityError{Code: verifier_outdated}`. I/O, context, keyring/discovery and other runtime
failures return typed `OperationalError`. Only a future Query adapter may convert a failed report
into `IntegrityError`.

The local CLI uses stable exit codes: `0` verified/success, `1` integrity, `2` configuration,
`3` verifier outdated, `4` operational, `5` conflict. Output never includes payload, private key,
seed, DSN, object credential, or wrapped secret-bearing cause text.

## Canonical v1 caveat

Canonical v1 is historically frozen but non-injective. A valid v1 event hash/root proves that the
archive preserved the database's historical chain representation; it does not upgrade v1 to v2
semantic collision resistance. Any segment containing v1 must retain the machine-readable caveat
`legacy_canonical_v1_non_injective`.
