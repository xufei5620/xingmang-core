sprint-section: 7

# XM-AUD2-archive-store-catalog · Exact-version archive store / catalog

## status

BLOCKED · WAITING APPROVED AUD2 GENERATED (sqlc output prepared)

This branch starts from the exact accepted release tip and records the approved AUD2
input digest below. The corrected input is approved; generated-artifact review remains the
next gate. Shared-stack execution and production access remain prohibited throughout this
slice.

## branch / base

- branch: `ai/codex/XM-AUD2-archive-store-catalog`
- worktree: `K:/星芒统一控制平台/wt-xmAUD2-impl`
- fresh release tip at implementation start: `db9d4cabe4092defc3f07b11d511a825491f56ae`
- current rebased release/base: `9dd9e8bcd7c34a11b1c1f1ed998ad8e7721feefb`
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

The provider approval did not itself approve migration bytes; the acceptance log contains
`2026-08-30T08:04Z APPROVED AUD2 INPUT 000018` for the corrected five-file candidate
(`digest=caaa33624b93c5d5fc2ee02688bf0d1207d8799f`). The branch has since been rebased to
the newer release `9dd9e8b` (migration max remains 17); the approved five input bytes are
unchanged. Migration application is limited to disposable PG18 probes. `go tool sqlc
generate` has run with the pinned tool; generated artifacts require a separate
approval. Shared `xingmang-launch`, production endpoints, servers, real
credentials and shared MinIO remain prohibited until that generated-artifact signal.

## migration input pin (pre-generation)

The manifest covers the five plan-mandated pre-generation inputs:

| path | SHA-256 |
|---|---|
| `db/migrations/000018_audit_archive_catalog.up.sql` | `559dbf9346df300cb65482e0c2af0bdc060c629d` |
| `db/migrations/000018_audit_archive_catalog.down.sql` | `0cf6a623f4ed3765496e173248319ec7dfdeff14` |
| `db/queries/audit.sql` | `db8f953e1a174c506731460e6d4b0922ef26e7fa` |
| `internal/platform/audit/archive/catalog_integration_test.go` | `1145d666422da7677eb500eee6c644fd56ca1631` |
| `internal/platform/audit/archive/receipt_journal_integration_test.go` | `24ae0703fce3c4399dc906961c954e5432e71d24` |

`migration_input_digest` is computed as the SHA-256 of the sorted `hash-object + two
spaces + path` lines plus one final LF, exactly as the implementation plan specifies.
The `2026-08-30T08:04Z` approval covers the corrected receipt-test bytes (savepoint
handling for expected duplicate-key errors) and the five-file digest
`caaa33624b93c5d5fc2ee02688bf0d1207d8799f`. The earlier e3ba approval is superseded.
The earlier PowerShell pipeline digest `6113a7453907148508697458b8be2ce2ffedeaba` was
an encoding artifact and is superseded. Any input byte edit or release-tip migration
movement invalidates approval and requires a fresh recomputation.

## generated artifact pin (sqlc)

`go tool sqlc version` returned `v1.31.1`; `go tool sqlc generate` completed successfully
after the exact input approval. `git diff --exit-code -- sqlc.yaml` is clean. Because the
repository's sqlc configs share one PostgreSQL schema, the generator refreshed three
`internal/platform/audit/gen` files plus six collateral package model files; all nine are
listed and frozen here (no hand edits):

| generated path | SHA-1 (`git hash-object`) |
|---|---|
| `internal/platform/action/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/alerts/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/audit/gen/audit.sql.go` | `ac259d14e3470fd1abb4e864dbf50ab283d7454c` |
| `internal/platform/audit/gen/db.go` | `3456a021e22e5ccca67590f8c3f47d4855852189` |
| `internal/platform/audit/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/finance/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/ops/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/registry/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |
| `internal/platform/savedviews/gen/models.go` | `8577186b474ad1f4646b0e678ea70d1390516767` |

The all-nine sorted-manifest digest (UTF-8 bytes of `hash  path` lines plus one LF) is
`52ee14cccebb36e866eb0648a459b1867e18897d`. For the plan's audit-only path set
(`internal/platform/audit/gen/*`), the corresponding digest is
`2b3f7899acbff5111ea4d658e20c51e562afd53d`. These are review artifacts, not deployment
evidence; await an exact `APPROVED AUD2 GENERATED` acceptance-log signal before adding
the provider SDK or runtime wiring.

## TDD stage

1. RED first: contract tests assert catalog ranges, checkpoint/generation
   coverage, append-only journal semantics, duplicate/conflicting bytes, and role-bound
   fixed-column access. They are opt-in to a disposable database and must not target the
   shared stack.
2. GREEN now: provider-independent validation, fixed locator/recovery-index models and
   filesystem fixture may be implemented and tested without the migration.
3. WAITING gate: PostgreSQL catalog/journal runtime wiring and shared/disposable MinIO
   qualification remain paused until the separate generated-artifact approval. A disposable
   PG18 migration/probe and sqlc generation are allowed now; neither is staging deployment.

### RED evidence

- A disposable PostgreSQL 18 container with a random name and loopback port was started
  solely for the schema probe and removed immediately. With
  `XM_AUD2_EXPECT_SCHEMA=1`,
  `go test ./internal/platform/audit/archive -run '^TestAUD2ArchiveSchemaMissingIsAVisibleRedFailure$' -count=1 -v`
  failed as intended: `AUD2 migration RED: audit.archive_segment is absent` (exit 1).
- The default archive package run compiles the new tests and skips database cases unless
  the explicit `XM_AUD2_CONTRACT_DATABASE_URL` is set; it does not target the shared stack.
- A fresh disposable PostgreSQL 18 probe (`xm-aud2-pg-51a19baf423c`, random loopback
  port; removed immediately) applied all 18 top-level migrations and ran the complete
  archive package with `XM_AUD2_CONTRACT_DATABASE_URL`:
  `go test ./internal/platform/audit/archive -count=1` — **PASS**. It then verified all
  four archive tables existed, confirmed the three AUD2 capability roles were absent as
  expected (DBR2 provisioning remains separate), applied the disposable down file, and
  observed all four archive relations as `NULL`. This is disposable evidence only; no
  shared stack, server, MinIO endpoint or production database was touched.

## files / non-goals for this stage

- Allowed preparation: this Handoff, the exact draft migration/query/test inputs, pure
  archive models/validation, filesystem fixture, and protocol-only MinIO qualification
  harness.
- Not done and not claimed: shared/staging application of `000018`, provisioning roles,
  starting MinIO, connecting any external endpoint, changing the worker/API, or deploying
  to `xingmang-launch`. The disposable-only `000018` apply/probe and sqlc generation are
  recorded above; generated bytes are not yet approved or deployed.
- No `audit.audit_event` UPDATE/DELETE/TRUNCATE/DROP path is introduced.

### files_changed

- `db/migrations/000018_audit_archive_catalog.up.sql` (approved input; disposable apply only)
- `db/migrations/000018_audit_archive_catalog.down.sql` (disposable teardown only)
- `db/queries/audit.sql` (approved fixed-column query input; generated output is pinned below)
- generated `internal/platform/audit/gen/{audit.sql.go,db.go,models.go}` plus six collateral
  `*/gen/models.go` files listed in the generated-artifact manifest
- `internal/platform/audit/archive/{objectstore,filesystem_store,catalog,receipt_journal,recovery_index,checkpoint,provider_qualification}.go`
- corresponding pure/integration contract tests under `internal/platform/audit/archive/*_test.go`
- this Handoff

### tests_not_run

- No generated artifact approval, MinIO process/provider SDK call, shared Docker stack,
  worker/API restart, staging, production or external credential qualification has been
  run yet. The disposable PostgreSQL migration/apply, sqlc generation and full archive
  package probe are recorded above.
- Live MinIO qualification remains pending generated-artifact approval and the one-time
  disposable credential; no secret value is recorded here.

### risks / follow_ups

- DB owner/ACL provisioning for `xm_audit_archive_catalog_writer`,
  `xm_audit_archive_receipt_reader` and `xm_audit_archive_receipt_writer` is intentionally
  deferred to the separately approved DBR2 policy; this migration does not create roles.
- PostgreSQL `archive_segment` cross-row contiguity and RecoveryIndex coverage remain
  application/transaction invariants until the exact generated SQL and catalog writer are
  approved and implemented.
- Before any runtime claim, re-fetch the release tip, recompute max/000018 and all five
  input hashes/digest, verify the `2026-08-30T08:04Z APPROVED AUD2 INPUT` line, and
  re-check the nine generated hashes/digest against the exact `APPROVED AUD2 GENERATED`
  line. Any changed input or generated byte invalidates the corresponding approval.

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

- `go test ./internal/platform/audit/archive -count=1` — PASS (pure package run; opt-in
  PostgreSQL tests skipped in the no-DSN run). The fresh disposable DSN run also passed
  the same full command with all archive integration probes enabled.
- `go test -p 1 ./... -count=1` — PASS after rebase to `9dd9e8b`; all backend/connector,
  generated and archive packages completed without failures.
- `go tool sqlc version` — `v1.31.1`; `go tool sqlc generate` — PASS; `git diff --exit-code
  -- sqlc.yaml` — PASS. Generated review/approval is still pending.
- `go test ./internal/platform/audit/archive -run 'TestAUD2' -count=1` — PASS (pure
  MemoryCatalog/ReceiptJournal/RecoveryIndex/Filesystem and provider checks).
- `go vet ./internal/platform/audit/archive` — PASS; `gofmt -l internal/platform/audit/archive`
  — no output; `git diff --check` — PASS.
- GOPROXY query recorded by the implementation line: `go list -m -json
  github.com/minio/minio-go/v7@latest` resolved current stable `v7.3.0` (2026-08-15).
  The dependency remains unadded until sqlc/generated-artifact review gates the provider
  adapter; no `latest` string is written to `VERSIONS.lock`.
- Provider-independent migration review used disposable PG18 probes and reported
  `up=PASS migrations=18`, full archive tests `PASS`, roles absent as expected, and
  `down=PASS tables_after=NULL|NULL|NULL|NULL`; no shared stack or production database
  was touched by this branch.

## references

- `docs/superpowers/specs/2026-08-28-audit-archive-design.md` §§6.1–6.3, 12–13.
- `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md` Task 2, Steps 1–9.
- `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.1–§7.2.
- `docs/handoffs/ACCEPTANCE-LOG.md` `2026-08-30T06:57Z APPROVED AUD2`.
