sprint-section: 7

# XM-AUD2-archive-store-catalog · Exact-version archive store / catalog

## status

IN_PROGRESS · exact AUD2 INPUT approved; waiting generated-artifact approval

This branch starts from the exact accepted release tip and currently limits itself to
AUD2 contract preparation plus provider-independent code. The migration/input digest is
recorded below; until the acceptance log carries an exact matching approval, migration,
sqlc and shared-stack execution remain prohibited.

## branch / base

- branch: `ai/codex/XM-AUD2-archive-store-catalog`
- worktree: `K:/星芒统一控制平台/wt-xmAUD2-impl`
- fresh release tip at implementation start: `db9d4cabe4092defc3f07b11d511a825491f56ae`
- current rebased release/base: `fb4a774066bf88db53312425f96babf52c92fb88`
- top-level migration max at fresh tip: `17`
- allocated migration number: `000018` (`000018_audit_archive_catalog`)
- commit: see final delivery line / branch HEAD

## approval and exact boundary

The `2026-08-30T06:57Z APPROVED AUD2` record authorizes the provider direction:
self-hosted MinIO, production project `xingmang-archive`, `region=local`, own-server
residency, versioning + Object Lock COMPLIANCE with 3650-day retention, no lifecycle
deletion, TLS + MinIO SSE-S3 (`MINIO_KMS_SECRET_KEY` via
`secret://archive/minio-kms`), PII excluded, `github.com/minio/minio-go/v7` with the
current stable version pinned by Codex, and content-addressed conditional Put/HEAD
recovery. It also requires a disposable random MinIO qualification project using
`secret://archive/minio-qualification`.

The provider approval did not itself approve migration bytes; the acceptance log now
contains `2026-08-30T07:41Z APPROVED AUD2 INPUT 000018` for the exact five-file candidate
based on `db9d4ca`. The branch has been rebased to the newer release `fb4a774` without
changing those five files; the migration max remains 17 and the approved digest remains
valid. Migration application is limited to a disposable PG18 probe. `go tool sqlc generate`,
generated artifacts, shared `xingmang-launch`, production endpoints, servers, real
credentials and shared MinIO remain prohibited until the separate generated-artifact
approval signal appears.

## migration input pin (pre-generation)

The manifest covers the five plan-mandated pre-generation inputs:

| path | SHA-256 |
|---|---|
| `db/migrations/000018_audit_archive_catalog.up.sql` | `559dbf9346df300cb65482e0c2af0bdc060c629d` |
| `db/migrations/000018_audit_archive_catalog.down.sql` | `0cf6a623f4ed3765496e173248319ec7dfdeff14` |
| `db/queries/audit.sql` | `db8f953e1a174c506731460e6d4b0922ef26e7fa` |
| `internal/platform/audit/archive/catalog_integration_test.go` | `1145d666422da7677eb500eee6c644fd56ca1631` |
| `internal/platform/audit/archive/receipt_journal_integration_test.go` | `16c39b4ab597de9858606f6050c770f8e46be43c` |

`migration_input_digest` is computed as the SHA-256 of the sorted `hash-object + two
spaces + path` lines plus one final LF, exactly as the implementation plan specifies.
Approved input digest: `e3ba2f84812a56a9a454aa73204298b9b8214f0e` (UTF-8 bytes of the
sorted `hash  path` lines plus one LF, matching the `2026-08-30T07:41Z` acceptance record).
The earlier PowerShell pipeline digest `6113a7453907148508697458b8be2ce2ffedeaba` was
an encoding artifact and is superseded. Any input byte edit or release-tip migration
movement invalidates approval and requires a fresh recomputation.

## TDD stage

1. RED first: contract tests assert catalog ranges, checkpoint/generation
   coverage, append-only journal semantics, duplicate/conflicting bytes, and role-bound
   fixed-column access. They are opt-in to a disposable database and must not target the
   shared stack.
2. GREEN now: provider-independent validation, fixed locator/recovery-index models and
   filesystem fixture may be implemented and tested without the migration.
3. WAITING gate: sqlc generation, generated artifacts, PostgreSQL catalog/journal runtime
   wiring, and shared/disposable MinIO qualification remain paused until the separate
   generated-artifact approval. A disposable PG18 migration/probe is allowed now; it must
   not be mistaken for staging deployment.

### RED evidence

- A disposable PostgreSQL 18 container with a random name and loopback port was started
  solely for the schema probe and removed immediately. With
  `XM_AUD2_EXPECT_SCHEMA=1`,
  `go test ./internal/platform/audit/archive -run '^TestAUD2ArchiveSchemaMissingIsAVisibleRedFailure$' -count=1 -v`
  failed as intended: `AUD2 migration RED: audit.archive_segment is absent` (exit 1).
- The default archive package run compiles the new tests and skips database cases unless
  the explicit `XM_AUD2_CONTRACT_DATABASE_URL` is set; it does not target the shared stack.
- A separate disposable PostgreSQL review probe (random container, removed after the run)
  applied the draft only for syntax/valid-row checking and reported
  `migration=PASS insert_exit=0 output=INSERT 0 1 1`; this is review evidence, not shared
  stack or production evidence. The current branch itself has not applied `000018`.

## files / non-goals for this stage

- Allowed preparation: this Handoff, the exact draft migration/query/test inputs, pure
  archive models/validation, filesystem fixture, and protocol-only MinIO qualification
  harness.
- Not done and not claimed: applying `000018`, running `go tool sqlc generate`, writing
  generated files, provisioning roles, starting MinIO, connecting any external endpoint,
  changing the worker/API, or deploying to `xingmang-launch`.
- No `audit.audit_event` UPDATE/DELETE/TRUNCATE/DROP path is introduced.

### files_changed

- `db/migrations/000018_audit_archive_catalog.up.sql` (draft only)
- `db/migrations/000018_audit_archive_catalog.down.sql` (disposable teardown only)
- `db/queries/audit.sql` (fixed-column query drafts; sqlc not run)
- `internal/platform/audit/archive/{objectstore,filesystem_store,catalog,receipt_journal,recovery_index,checkpoint,provider_qualification}.go`
- corresponding pure/integration contract tests under `internal/platform/audit/archive/*_test.go`
- this Handoff

### tests_not_run

- No PostgreSQL migration, sqlc generation, generated artifact review, MinIO process,
  provider SDK call, shared Docker stack, worker/API restart, staging, production or
  external credential qualification was run in this branch.
- The live MinIO qualification test remains pending the one-time disposable credential
  and exact-input approval evidence; no secret value is recorded here.

### risks / follow_ups

- DB owner/ACL provisioning for `xm_audit_archive_catalog_writer`,
  `xm_audit_archive_receipt_reader` and `xm_audit_archive_receipt_writer` is intentionally
  deferred to the separately approved DBR2 policy; this migration does not create roles.
- PostgreSQL `archive_segment` cross-row contiguity and RecoveryIndex coverage remain
  application/transaction invariants until the exact generated SQL and catalog writer are
  approved and implemented.
- Before any green migration/runtime claim, re-fetch the release tip, recompute max/000018
  and all five input hashes/digest, obtain the exact `APPROVED AUD2 INPUT` log line, then
  run sqlc and its separate generated-artifact review. Any changed byte invalidates this
  Handoff's digest.

### Pure contract implementation delivered in this stage

- `objectstore.go`: exact capability interfaces, intent/version validation, canonical
  digest helpers and bounded object-size guard. The approved MinIO SSE-S3 exception is
  accepted through a local validation shim alongside the historical SSE-KMS wire fixture;
  both remain explicitly bound to an opaque key identifier and Object Lock metadata.
- `filesystem_store.go`: disposable local fixture implementing only PutIfAbsent,
  RecoverPutResult, HeadVersion and GetVersion. Writes are content-addressed, atomic,
  loopback/local-root constrained, symlink checked, and readback verifies exact bytes,
  size and metadata. It is not production evidence and has no deletion/list API.
- `catalog.go`, `receipt_journal.go`, `recovery_index.go`, `checkpoint.go`: in-memory
  protocol doubles for contiguous catalog commit, append-only intent/put/terminal
  receipts, fixed-locator generation CAS and exact signed-wire digests. They are test
  doubles only; no PostgreSQL calls or runtime wiring are present.
- `provider_qualification.go`: approved MinIO qualification config validator and
  sanitized result shape; no endpoint/credential is opened.

### Verification evidence

- `go test ./internal/platform/audit/archive -count=1` — PASS (package tests; opt-in
  PostgreSQL tests skipped because no `XM_AUD2_CONTRACT_DATABASE_URL` was set).
- `go test ./internal/platform/audit/archive -run 'TestAUD2' -count=1` — PASS (pure
  MemoryCatalog/ReceiptJournal/RecoveryIndex/Filesystem and provider checks).
- `go vet ./internal/platform/audit/archive` — PASS; `gofmt -l internal/platform/audit/archive`
  — no output; `git diff --check` — PASS.
- GOPROXY query recorded by the implementation line: `go list -m -json
  github.com/minio/minio-go/v7@latest` resolved current stable `v7.3.0` (2026-08-15);
  the dependency is intentionally not yet added to `go.mod`/`VERSIONS.lock` in this
  prep commit until the exact input review permits the provider adapter.
- Provider-independent migration review used a separate disposable PG18 probe and
  reported `migration=PASS insert_exit=0 output=INSERT 0 1 1`; no shared stack or
  production database was touched by this branch.

## references

- `docs/superpowers/specs/2026-08-28-audit-archive-design.md` §§6.1–6.3, 12–13.
- `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md` Task 2, Steps 1–9.
- `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.1–§7.2.
- `docs/handoffs/ACCEPTANCE-LOG.md` `2026-08-30T06:57Z APPROVED AUD2`.
