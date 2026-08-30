sprint-section: 7

# XM-AUD2-s3-adapter · MinIO/S3 object capability adapter

## status

**BLOCKED (protocol-ready; live provider qualification missing).** The protocol
implementation and `httptest` contract tests are complete, but this slice does not
claim AUD2 or production readiness. The approved one-time
`secret://archive/minio-qualification` credential and a disposable WORM-capable
MinIO qualification run are not available in this environment.

## branch / base

- branch: `ai/codex/XM-AUD2-s3-adapter`
- worktree: `K:/星芒统一控制平台/wt-xmAUD2-s3`
- initial development base: `e579b9e` (AUD2 input + generated approvals already present)
- final release base: `6242ffd` (`origin/release/v0.1-launch`, including the merged
  AUD2 catalog and the approved independent security-sink decision)
- implementation commit after rebase: `d83b2b7` (the final Handoff update is committed
  separately and its SHA is reported in the delivery line)

## approved boundary and decisions

- The acceptance log's AUD2 approval fixes self-hosted MinIO, `region=local`,
  versioning, Object Lock `COMPLIANCE`, and a 3650-day retention policy. This slice
  only supplies the adapter; it does not add compose services, migrations, catalog or
  runtime wiring.
- Encryption is MinIO/S3 SSE-S3 (`AES256`) via `encrypt.NewSSE()`. The frozen
  `ObjectVersionV1.KMSKeyID` field is retained as an opaque approved protection label
  and is sent only as immutable `x-amz-meta-*` metadata for readback comparison; it is
  never passed to `NewSSEKMS`, never emitted as an AWS KMS header, and never used to
  call an AWS KMS API.
- `PutIfAbsent` accepts only a validated content-addressed intent, buffers at the
  bounded known size, verifies SHA-256/size before network I/O, sets
  `If-None-Match: *`, SSE-S3 AES256, Object Lock COMPLIANCE/retain-until, and disables
  multipart and streaming chunk framing. The returned provider VersionID is mandatory;
  an empty, `null`, or `latest` value fails closed.
- `HeadVersion` and `GetVersion` require the store-bound bucket and an exact non-empty
  VersionID. GET first validates exact HEAD metadata, then validates the lazy SDK GET
  response's version/protection metadata and complete body SHA-256/size before any
  bytes are returned to the caller.
- `RecoverPutResult` intentionally performs **no** plain HEAD, latest lookup, List, or
  second PUT. MinIO/S3 v7.3.0 exposes no approved provider-native intent-token result
  query, so ambiguous PUT recovery returns `ErrProviderQualificationMissing` wrapped
  with `ErrS3RecoveryUnsupported`. This is the required fail-closed NO-GO until a
  separately approved primitive and qualification evidence exist.
- Endpoint hosts are exact allowlist entries (host or host:port); wildcard/path
  entries, redirects, endpoint scheme changes, and non-allowlisted requests fail
  closed. Credential material is resolved only through the injected
  `secrets.SecretProvider` and strict canonical JSON (`access_key`, `secret_key`,
  optional `session_token`) with duplicate/case-fold duplicate/unknown/trailing fields
  rejected.
- The `S3Store` method set satisfies only `ObjectWriter` and `ExactObjectReader`; no
  Delete, List, latest-object, presigned URL, or general Overwrite capability exists.

## files_changed

- `internal/platform/audit/archive/s3_store.go`
- `internal/platform/audit/archive/s3_store_test.go`
- `go.mod` (`github.com/minio/minio-go/v7 v7.3.0`)
- `go.sum` (v7.3.0 module and zip hashes)
- `VERSIONS.lock` (`minio-go = v7.3.0`)
- this Handoff

No AUD2 input files, generated artifacts, migration/query/catalog files, compose files,
or runtime wiring were changed.

## protocol evidence

- `go test ./internal/platform/audit/archive -run '^TestS3' -count=1 -v` — PASS for
  all protocol tests when compiled against the locally cached API-compatible MinIO
  module during development (the temporary `replace` was removed before delivery).
  Coverage includes conditional/SSE-S3/known-size writes, digest/size preflight,
  empty VersionID, exact Head/Get/readback, unbound bucket, protection metadata,
  endpoint/credential/redirect failures, duplicate credential JSON, and fail-closed
  ambiguous recovery.
- `git diff --check` — PASS.
- `scripts/check-governance.sh` — PASS.
- gitleaks source scans for both changed Go files — PASS; no credential values or
  key-shaped fixtures are committed.
- Dependency lock facts: module `h1:HM4pFCSQq/TK+j0/zmorSh5ddh81iDgRgU0BG0Vz/YU=`;
  go.mod `h1:KUPWdecEO1LWyUz+sTGXAuf2jZHrPh5fCsRH86QbPfk=`.

## tests_not_run / qualification gate

- Exact v7.3.0 package compilation and full Go suite could not run locally because the
  v7.3.0 zip fetch is blocked by the environment's unavailable GOPROXY DNS. No
  alternate-version `replace` is present in the branch.
- `TestS3LiveQualificationProviderQualificationMissing` is intentionally `SKIP` by
  default with the explicit `ProviderQualificationMissing` reason. With
  `XM_REQUIRE_AUDIT_ARCHIVE_PROVIDER_QUALIFICATION=1`, it fails instead of skipping;
  this is the required gate until the approved disposable MinIO credential is injected.
- No real MinIO endpoint, credential, shared Docker stack, staging, production bucket,
  Object Lock configuration, or AWS KMS service was contacted.

## risks / follow-ups

1. Obtain the approved one-time CredentialRef and run the independent random-project
   MinIO qualification, including response-loss/ambiguous-Put recovery. If the provider
   cannot return an exact VersionID through a provider-native intent-token primitive,
   record `ProviderQualificationFailed` and keep this adapter NO-GO; do not substitute
   latest HEAD/List/re-Put.
2. Re-run the exact v7.3.0 build, full package/Go gates, and the qualification evidence
   on the same commit before any AUD2 catalog/runtime slice consumes this adapter.
3. Keep `KMSKeyID` semantics aligned with the approved MinIO SSE-S3 logical protection
   label; introducing AWS KMS/SSE-KMS headers requires a new approval and is outside
   this slice.

## references

- `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7
- `docs/handoffs/ACCEPTANCE-LOG.md` `APPROVED AUD2` / `APPROVED AUD2 GENERATED`
- `docs/superpowers/specs/2026-08-28-audit-archive-design.md` §§6.1–6.3
- `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md` Task 2 Steps 5–7
