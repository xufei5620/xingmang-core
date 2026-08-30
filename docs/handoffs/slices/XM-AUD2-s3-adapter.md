sprint-section: 7

# XM-AUD2-s3-adapter · MinIO/S3 object capability adapter

## status

**READY (protocol + disposable qualification; no production/runtime claim).** The
adapter protocol, exact-key recovery, and one-time disposable MinIO qualification are
complete. This slice still does not claim production activation or full AUD2 completion;
it only proves the approved local provider/image contract and leaves runtime wiring to
the parent AUD2 implementation.

## branch / base

- branch: `ai/codex/XM-AUD2-s3-adapter`
- worktree: `K:/星芒统一控制平台/wt-xmAUD2-s3`
- initial development base: `e579b9e` (AUD2 input + generated approvals already present)
- final release base: `db0e499` (`origin/release/v0.1-launch`, including the merged
  AUD2 catalog follow-up, DBR1 approvals, and qualification/recovery approval)
- implementation commits: `d83b2b7` (adapter baseline), followed by the exact-key
  recovery, policy guards, qualification harness, and lock updates; final delivery SHA
  is reported after the release rebase.

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
- `RecoverPutResult` implements the newly approved recovery primitive:
  `ListObjectVersions` with `Prefix` equal to the exact content-addressed key, followed
  by an exact VersionID HEAD. Zero, multiple, delete-marker, wrong-key, missing-version,
  or metadata-mismatch results return `ErrProviderQualificationFailed`; the adapter never
  falls back to a latest HEAD, arbitrary List, or a second PUT.
- The adapter constructor enforces the approved `region=local`, SSE-S3,
  Object-Lock COMPLIANCE, and 3650-day policy defaults/values. HTTP is permitted only for
  loopback disposable fixtures; non-loopback endpoints require HTTPS.
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
- `internal/platform/audit/archive/s3_qualification_test.go` (opt-in disposable harness)
- `go.mod` (`github.com/minio/minio-go/v7 v7.3.0`)
- `go.sum` (v7.3.0 module and zip hashes)
- `VERSIONS.lock` (`minio-go = v7.3.0` plus pinned MinIO image tag/index digest)
- this Handoff

No AUD2 input files, generated artifacts, migration/query/catalog files, compose files,
or runtime wiring were changed.

## protocol evidence

- `go test -mod=readonly ./internal/platform/audit/archive -run '^TestS3|^TestReadMinIOImagePin|^TestQualificationCompose' -count=1 -v` — PASS for
  all exact v7.3.0 protocol/fixture tests (live test is opt-in).
  Coverage includes conditional/SSE-S3/known-size writes, digest/size preflight,
  empty VersionID, exact Head/Get/readback, unbound bucket, protection metadata,
  endpoint/credential/redirect/policy failures, duplicate credential JSON, exact-key
  version-list recovery, and response-loss refusal cases.
- `go test -p 1 -mod=readonly ./... -count=1` — PASS (full repository suite).
- `go build -mod=readonly ./...` and `go vet -mod=readonly ./...` — PASS.
- `go mod tidy` with the available module proxy — PASS; a second tidy produced no
  additional changes.
- `XM_REQUIRE_AUDIT_ARCHIVE_PROVIDER_QUALIFICATION=1 go test -mod=readonly ./internal/platform/audit/archive -run '^TestS3LiveQualificationDisposableMinIO$' -count=1 -v` — PASS (5.74s).
  The harness generated a random Compose project/volume and root credentials, mapped
  `secret://archive/minio-qualification` through `ConventionEnvProvider`, used
  `docker.io/minio/minio:RELEASE.2025-04-22T22-12-26Z@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e`, verified Linux image
  RepoDigest/labels and Docker-assigned loopback port, enabled versioning + Object Lock
  COMPLIANCE/3650 days, and passed SSE-S3/conditional-write plus response-drop → exact
  ListObjectVersions recovery → exact Get/readback. Teardown left no labeled resources or
  listening port; evidence logs contain status/counts only, never credentials or payload.
- `git diff --check` — PASS.
- `scripts/check-governance.sh` — PASS.
- gitleaks source scans for both changed Go files — PASS; no credential values or
  key-shaped fixtures are committed.
- Dependency lock facts: module `h1:HM4pFCSQq/TK+j0/zmorSh5ddh81iDgRgU0BG0Vz/YU=`;
  go.mod `h1:KUPWdecEO1LWyUz+sTGXAuf2jZHrPh5fCsRH86QbPfk=`.

## tests_not_run / qualification boundary

- The live qualification test is intentionally skipped by default with the explicit
  `ProviderQualificationMissing` reason; set
  `XM_REQUIRE_AUDIT_ARCHIVE_PROVIDER_QUALIFICATION=1` to make inability to qualify fail.
- No shared `xingmang-launch` stack, staging/production bucket, external credential, or
  AWS KMS service was contacted. The generated Compose project is disposable and is
  always torn down by label/volume/port checks.

## risks / follow-ups

1. Keep the qualification image digest and retention policy in sync with any separately
   approved provider change; a tag-only or unqualified image is rejected.
2. Runtime AUD2 wiring must persist the exact VersionID/readback metadata and use the
   approved append-only journal; this adapter alone does not activate archive jobs.
3. Keep `KMSKeyID` semantics aligned with the approved MinIO SSE-S3 logical protection
   label; introducing AWS KMS/SSE-KMS headers requires a new approval and is outside
   this slice.

## references

- `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7
- `docs/handoffs/ACCEPTANCE-LOG.md` `APPROVED AUD2` / `APPROVED AUD2 GENERATED`
- `docs/superpowers/specs/2026-08-28-audit-archive-design.md` §§6.1–6.3
- `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md` Task 2 Steps 5–7
