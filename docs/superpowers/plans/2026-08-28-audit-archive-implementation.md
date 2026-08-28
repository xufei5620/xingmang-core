# Audit Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不删除或改写任何审计事件的前提下，交付可签名验证、可透明查询、可隔离恢复的审计冷归档能力。

**Architecture:** PostgreSQL 继续保存全量 hot 事件；版本化、受信 PLO 按全局 sequence 生成冻结 wire payload/projection 与 exact-version signed manifest，再发布独立签名 ArchiveCheckpoint，并以独立故障域 RecoveryIndex CAS 作为 terminal commit。PostgreSQL catalog 是可重建投影。Query 优先 hot；恢复从 trusted keyring + fixed RecoveryIndex 发现 terminal manifest，在空隔离库同事务导入并验链后才提交。

**Tech Stack:** Go 1.27、PostgreSQL 18、pgx/sqlc、Ed25519/SHA-256、S3-compatible WORM object storage（供应商与精确 SDK 版本须先审批）、React/TypeScript/Vitest、River（仅在 R2-10 与 DB 角色拆分后启用调度）。

**Spec:** `docs/superpowers/specs/2026-08-28-audit-archive-design.md`

## Global Constraints

- 本计划是未来实施顺序，不是 XM-C-AUD0 对实现的授权；必须先由人类审批 spec，再逐片批准 AUD1～AUD5。
- 每片使用独立 worktree、分支、commit 和 PR；base 为实施时最新的 `origin/release/v0.1-launch`，Codex 不自行合并。
- AUD1～AUD5 禁止对 `audit.audit_event` 执行 `DELETE`、`UPDATE`、`TRUNCATE`、`DROP`，也禁止增加审计保留天数。
- ObjectWriter/ExactObjectReader 分离；禁止 Delete、latest Get、通用 Overwrite、任意 List 与浏览器预签名 URL；对象键只能由归档库按 spec 生成。
- 权威段按全局 sequence 连续分段；不得按 environment 拆证据链；v1/v2 `canonical_version` 必须逐行原样保存并分别验证。
- 归档、验证、恢复都是 Platform Lifecycle Operation；不增加普通 Action、不增加 UI 触发器。
- 生产对象存储必须是 PostgreSQL 之外的异故障域，并启用 versioning、WORM/Object Lock、SSE-KMS；开发同机 fixture 不算生产证据。
- 首期 human Query 继续使用 `audit.read`；full payload 只供 archive/verifier/restore 机器身份。
- Catalog 只有 committed 记录；对象全部 PutIfAbsent、Head、Get、hash/signature/root 验证成功后才允许 INSERT。
- Artifact 自带公钥永不建立信任；AUD1 必须退休现有 root JSON embedded-key 自认证，所有签名按 Purpose/Protocol/Validity/Fingerprint 从独立 trusted keyring 验证。
- 所有对象引用必须钉 `(bucket_id,key,VersionID,sha256)` 与实际 KMS/Object Lock；禁止 latest Get。RecoveryIndex CAS 是唯一 terminal marker，catalog 不得领先它。
- `chain_integrity` 与 ActionRun `capture_completeness` 分开报告；v1 验通仍带非单射 caveat。
- 自动调度必须同时等待 R2-10 集群级任务所有权与 DB 角色拆分；条件未满足时只允许人工批准的 CLI。
- copy-only 不释放 PostgreSQL 容量；物理瘦身/源副本回收不在本计划内，默认禁止。
- 目标值为 archive RPO ≤1 小时、restore RTO ≤4 小时；只有持续观测与隔离恢复演练通过后才能声称达标。
- 每片交付前运行仓库全量门禁：`pnpm -r run typecheck`、`pnpm -r run test`、`pnpm --filter ui-storybook run build`、`go fmt ./...`、`go vet ./...`、`go test -p 1 ./...`、`bash scripts/check-governance.sh`。没有前端改动的片仍运行前端门禁；环境阻塞必须如实记录，不能宣称全绿。

---

## 0. 审批与执行编排

### 0.1 XM-C-AUD0 后的停止门

XM-C-AUD0 只提交 spec 与本计划。提交后停止，不创建 AUD1 worktree，直到产品
负责人明确批准以下最小集合：

1. copy-only/no-delete 边界；
2. payload/projection/manifest 双层模型；
3. Chain Root/Manifest/Checkpoint/RecoveryIndex/PLO purpose 隔离的 trusted keyring；
4. fixed RecoveryIndex failure domain、terminal CAS 与 catalog 可重建模型；
5. AUD1 开工。

AUD2 另需供应商、region、WORM、KMS、PII/驻留与迁移审批；AUD4 另需 coverage
契约审批；AUD5 另需恢复身份、隔离环境和演练窗口审批。

### 0.2 每片固定开工检查

- [ ] **Step 1: 拉取并核验最新 base**

```powershell
git fetch origin
git rev-parse origin/release/v0.1-launch
git status --short --branch
```

Expected: fetch 成功；当前任务 worktree 干净；base commit 被记录进 Task/PR。

- [ ] **Step 2: 读取批准记录与依赖状态**

检查本 spec 审批评论、上一片 PR 评论、R2-10 与 DB 角色拆分是否已合入。任一
本片硬依赖缺失时，停止并在任务顶部写 `BLOCKED:`，不提交半套 runtime。

- [ ] **Step 3: 建立独立 worktree**

分支建议：

```text
ai/codex/XM-C-AUD1-archive-format
ai/codex/XM-C-AUD2-archive-store-catalog
ai/codex/XM-C-AUD3-archive-lifecycle
ai/codex/XM-C-AUD4-archive-query
ai/codex/XM-C-AUD5-archive-restore
```

每片只允许修改其 `Files` 清单；需要超出时先修订 Task Spec/审批，不顺手扩大。

---

## Task 1（AUD1）: 确定性格式、本地 Exporter 与离线 Verifier

**Approval gate:** XM-C-AUD0 spec 明确获批。AUD1 不引入迁移、对象 SDK、worker
调度、HTTP 契约或部署配置。

**Files:**

- Create: `internal/platform/audit/archive/model.go`
- Create: `internal/platform/audit/archive/format.go`
- Create: `internal/platform/audit/archive/format_test.go`
- Create: `internal/platform/audit/archive/decode.go`
- Create: `internal/platform/audit/archive/decode_test.go`
- Create: `internal/platform/audit/archive/artifact_wire.go`
- Create: `internal/platform/audit/archive/artifact_wire_test.go`
- Create: `internal/platform/audit/archive/export.go`
- Create: `internal/platform/audit/archive/export_test.go`
- Create: `internal/platform/audit/archive/verify.go`
- Create: `internal/platform/audit/archive/verify_test.go`
- Create: `internal/platform/audit/archive/keyring.go`
- Create: `internal/platform/audit/archive/keyring_test.go`
- Create: `internal/platform/audit/archive/testdata/mixed-v1-v2.ndjson`
- Create: `internal/platform/audit/archive/testdata/mixed-v1-v2.manifest.json`
- Create: `internal/platform/audit/archive/testdata/missing-canonical.ndjson`
- Create: `internal/platform/audit/archive/testdata/zero-canonical.ndjson`
- Create: `internal/platform/audit/archive/testdata/truncated-final-line.ndjson`
- Create: `internal/platform/audit/archive/testdata/chain-root-ref-v1.json`
- Create: `internal/platform/audit/archive/testdata/manifest-v1.json`
- Create: `internal/platform/audit/archive/testdata/checkpoint-v1.json`
- Create: `internal/platform/audit/archive/testdata/recovery-index-v1.json`
- Create: `internal/platform/audit/archive/testdata/artifact-wire-v1.sha256`
- Create: `internal/platform/audit/archive/testdata/root-keyring.json`
- Create: `internal/platform/audit/archive/testdata/manifest-keyring.json`
- Create: `cmd/audit-archive/main.go`
- Create: `cmd/audit-archive/main_test.go`
- Modify: `internal/platform/audit/anchor.go`
- Modify: `internal/platform/audit/anchor_test.go`
- Modify: `db/queries/audit.sql`
- Regenerate: `internal/platform/audit/gen/audit.sql.go`
- Create: `docs/modules/audit/ARCHIVE-FORMAT-v1.md`
- Modify: `docs/modules/audit/README.md`
- Modify: `docs/modules/audit/DATA-MODEL.md`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Create: `docs/evidence/EV-<date>-audit-archive-baseline.md`

**Interfaces:**

- Consumes: `audit.Event`, `audit.Store.List`, `audit.ChainRoot`,
  `audit.VerifyRoot`, per-row `Event.ComputeHash`.
- Produces:

```go
package archive

const FormatVersion = 1

type SegmentBoundary struct {
    FromSequence int64
    ToSequence   int64
    RowCount     int64
    FirstPrevHash string
    FirstEventHash string
    LastEventHash string
}
type EncodedObjectV1 struct {
    SHA256 string
    SizeBytes int64
    ContentType string
    RowCount int64
}

type WireTime string // ParseWireTime enforces UTC and exactly six fractional digits.

type ObjectVersionV1 struct {
    BucketID, Key, VersionID, SHA256 string
    SizeBytes int64
    ContentType, ProviderChecksum, ETag string
    EncryptionMode, KMSKeyID, ObjectLockMode string
    RetainUntil WireTime
    RowCount int64
}

type CanonicalCountV1 struct { Version int16; RowCount int64 }
type PreviousManifestRefV1 struct {
    Kind, BucketID, Key, VersionID, SHA256 string
}
type ArtifactRefV1 struct { BucketID, Key, VersionID, SHA256 string }
type PreviousCheckpointRefV1 struct {
    Kind, BucketID, Key, VersionID, SHA256 string
}
type ChainRootRefV1 struct {
    ID string
    ComputedAt WireTime
    FromSequence, ToSequence int64
    RootHash, Signature, KeyID string
}
type ProjectionRefV1 struct { Environment string; Object ObjectVersionV1 }

type ManifestV1 struct {
    Kind string
    FormatVersion int
    ExporterVersion, ExporterCommit string
    FromSequence, ToSequence, RowCount int64
    FirstPrevHash, FirstEventHash, LastEventHash string
    PreviousManifest PreviousManifestRefV1
    CanonicalVersionCounts [2]CanonicalCountV1
    OversizedRecordCount int64
    Payload ObjectVersionV1
    Projections []ProjectionRefV1
    ChainRoot ChainRootRefV1
    CreatedAt, SourceTipObservedAt, ActionRunSnapshotAt WireTime
    EligibleActionRunCount, MatchedActionRunCount int64
    MissingAuditEventCount, DuplicateAuditEventCount int64
}
type SignedManifestV1 struct {
    Unsigned ManifestV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type CheckpointV1 struct {
    Kind string
    FormatVersion int
    Generation int64
    PreviousCheckpoint PreviousCheckpointRefV1
    FirstManifest, TerminalManifest ArtifactRefV1
    FromSequence, ToSequence, ManifestCount int64
    CatalogRowsDigest string
    ChainRoot ChainRootRefV1
    CanonicalVersionCounts [2]CanonicalCountV1
    ChainIntegrity, CaptureCompleteness string
    EligibleActionRunCount, MatchedActionRunCount int64
    MissingAuditEventCount, DuplicateAuditEventCount int64
    SourceTipSequence int64
    SourceTipObservedAt WireTime
    ApprovalEnvelopeSHA256, OperationIntentDigest string
    CreatedAt WireTime
}
type SignedCheckpointV1 struct {
    Unsigned CheckpointV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type RecoveryIndexV1 struct {
    Kind string
    FormatVersion int
    Generation, PreviousGeneration int64
    PreviousIndexSHA256 string
    Checkpoint, TerminalManifest ArtifactRefV1
    TerminalSequence int64
    TerminalRootHash string
    UpdatedAt WireTime
}
type SignedRecoveryIndexV1 struct {
    Unsigned RecoveryIndexV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type KeyPurpose string
const (
    PurposeChainRoot KeyPurpose = "chain_root_signing"
    PurposeManifest KeyPurpose = "archive_manifest_signing"
    PurposeCheckpoint KeyPurpose = "archive_checkpoint_signing"
    PurposeRecoveryIndex KeyPurpose = "recovery_index_signing"
    PurposePLOApproval KeyPurpose = "plo_approval_signing"
    PurposePLOResult KeyPurpose = "plo_result_receipt_signing"
    PurposeKillSwitch KeyPurpose = "kill_switch_signing"
    PurposeScrubReceipt KeyPurpose = "archive_scrub_receipt_signing"
    PurposeScrubHead KeyPurpose = "archive_scrub_head_signing"
)
type TrustedKey struct {
    KeyID       string
    Algorithm   string
    PublicKey   string
    Fingerprint string
    Purpose     KeyPurpose
    Protocol    string
    ValidFrom   time.Time
    ValidUntil  time.Time
    RevokedAt   *time.Time
    RevokeReason string
}

type Keyring interface {
    Lookup(keyID string, purpose KeyPurpose, protocol string, signedAt time.Time) (TrustedKey, error)
}
type PurposeSigner interface {
    audit.Signer
    Purpose() KeyPurpose
    Protocol() string
}

type LocalAuditSourceReader interface {
    Tip(context.Context) (int64, string, error)
    List(context.Context, int64, int64) ([]audit.Event, error)
    VerifyChain(context.Context, int64, int64) (*audit.ChainProblem, error)
    LatestRoot(context.Context) (audit.ChainRoot, error)
}

type VerificationCode string

const (
    VerificationOK                 VerificationCode = "ok"
    VerificationObjectHashMismatch VerificationCode = "object_hash_mismatch"
    VerificationManifestSignature  VerificationCode = "manifest_signature_invalid"
    VerificationManifestGap        VerificationCode = "manifest_gap"
    VerificationSequenceGap        VerificationCode = "sequence_gap"
    VerificationBrokenLink         VerificationCode = "broken_link"
    VerificationEventHashMismatch  VerificationCode = "event_hash_mismatch"
    VerificationRootSignature      VerificationCode = "root_signature_invalid"
)

type VerificationReport struct {
    Code             VerificationCode
    FirstBadSequence int64
    Detail           string
    VerifiedRows     int64
    VerifiedObjects  int64
    VerifiedRootHash string
}

type CompatibilityError struct { Code string; FormatVersion, CanonicalVersion int }
type OperationalError struct { Code string; Cause error }
type IntegrityError struct { Report VerificationReport }
```

- `EncodePayload(io.Writer, []audit.Event) (EncodedObjectV1, error)` writes the
  exact NDJSON v1 bytes.
- `EncodeProjection(io.Writer, environment string, []audit.Event) (EncodedObjectV1, error)`
  writes only the approved HTTP item fields.
- `SignManifest(ManifestV1, PurposeSigner) (SignedManifestV1, error)` signs the
  domain-separated manifest hash.
- `VerifySegment(ctx context.Context, manifest SignedManifestV1, payload io.Reader,
  projections map[string]io.Reader, rootKeyring Keyring, manifestKeyring Keyring,
  previous *SignedManifestV1) (VerificationReport, error)` returns deterministic
  integrity/format results separately from typed operational errors; it never rewrites input.

`VerificationReport` has no verifier-outdated code. Unknown archive/canonical versions
return `*CompatibilityError`; I/O/KMS/keyring/discovery failures return
`*OperationalError`; the Query adapter alone turns a failed report into
`*IntegrityError`. CLI/HTTP mapping uses `errors.As` only.

`ArchiveEventV1` is a frozen wire struct distinct from `audit.Event`. Its strict
decoder rejects unknown/duplicate/missing/null fields, zero/unknown canonical
versions and final partial lines before mapping to `audit.Event`.

- [ ] **Step 1: Capture the fresh staging evidence snapshot and STOP on drift**

Record database/environment fingerprint, UTC capture time, event min/max/count,
gap/duplicate checks, canonical counts, root count and eligible/matched/missing/
duplicate ActionRun counts in the evidence file. Use only read-only fixed SQL.
If the result differs from AUD0's dated 108/103/5/0 snapshot, explain and obtain
acknowledgement before freezing fixtures; never edit expected values to hide drift.

- [ ] **Step 2: Freeze mixed v1/v2 and negative golden data**

Create a two-event fixture where sequence 1 uses `CanonicalV1`, sequence 2 uses
`CanonicalV2`, and both hashes are independently precomputed with the frozen
canonical implementation. Check in literal payload/manifest/signature bytes and
fixed SHA-256 values; expected bytes must not be generated by the encoder under test.
Also check in literal `ChainRootRefV1`、`ManifestV1`、`CheckpointV1`、
`RecoveryIndexV1` unsigned/signed bytes and their fixed digest list, plus missing/zero
canonical, duplicate/unknown/null/misordered field and truncated-line goldens.
The fixture contains no credential-like values or personal data.

- [ ] **Step 3: Write deterministic format/strict decoder failures first**

Add tests:

```go
func TestEncodePayloadIsByteDeterministic(t *testing.T)
func TestPayloadPreservesAllPersistedFieldsAndCanonicalVersion(t *testing.T)
func TestDecodeRejectsMissingZeroNullUnknownAndDuplicateFields(t *testing.T)
func TestDecodeRejectsUnknownCanonicalAsVerifierOutdated(t *testing.T)
func TestDecodeRejectsTruncatedFinalLineAndMissingLF(t *testing.T)
func TestArtifactWireLiteralBytesAndSHA256AreStable(t *testing.T)
func TestArtifactWireRejectsUnknownDuplicateMissingNullMisorderedAndBadTime(t *testing.T)
func TestChainRootRefWireExcludesPublicKeyExportedAtAndExportTarget(t *testing.T)
func TestFirstManifestRequiresExactGenesisRef(t *testing.T)
func TestLaterManifestRequiresPreviousBucketKeyVersionAndSHA(t *testing.T)
func TestProjectionContainsOnlyHTTPAllowlistedFields(t *testing.T)
func TestProjectionSeparatesEnvironments(t *testing.T)
func TestSegmentCutsAtRowOrByteBoundaryWithoutPartialRecord(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestEncode|TestDecode|TestArtifactWire|TestChainRootRef|TestFirstManifest|TestLaterManifest|TestPayload|TestProjection|TestSegment' -count=1 -v
```

Expected: FAIL because the package/functions do not exist.

- [ ] **Step 4: Implement frozen wire structs, strict decoder and NDJSON encoding**

Use explicit field-presence tracking/token decoding rather than a plain Go struct
zero value. Reuse the existing frozen canonical v1/v2 only after strict wire
validation; archive missing/zero never takes `Event.Canonical()`'s in-memory v1 fallback.

- [ ] **Step 5: Prove format green and byte-stable**

Run the Step 3 command twice and compare the fixture/output SHA-256 in the test.
Expected: PASS both times with the same digest.

- [ ] **Step 6: Write verifier/key-policy red tests**

Add exact mutations and expected codes:

```go
func TestVerifyMixedCanonicalVersions(t *testing.T)
func TestVerifyRejectsUnknownCanonicalVersionAsVerifierOutdated(t *testing.T)
func TestVerifyDetectsChangedPayloadByte(t *testing.T)
func TestVerifyDetectsSequenceGapAndBrokenLink(t *testing.T)
func TestVerifyDetectsMissingOverlappingAndReorderedManifest(t *testing.T)
func TestVerifyRejectsWrongManifestKeyAndWrongRootKey(t *testing.T)
func TestVerifyRejectsArtifactEmbeddedPublicKeySelfAuthentication(t *testing.T)
func TestVerifyRejectsWrongPurposeProtocolFingerprintAndExpiredKey(t *testing.T)
func TestKeyringRejectsSameRawKeyAcrossPurposes(t *testing.T)
func TestKeyValidityUsesSignedAtHalfOpenBoundary(t *testing.T)
func TestManifestCheckpointIndexAndPLODomainReplayFails(t *testing.T)
func TestVerifyRejectsRevokedKeyForNewSignature(t *testing.T)
func TestVerifySeparatesOperationalFailureFromIntegrityReport(t *testing.T)
func TestVerifierOutdatedIsOnlyTypedCompatibilityError(t *testing.T)
func TestVerifyRejectsProjectionHiddenFields(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run TestVerify -count=1 -v
```

Expected: FAIL until structured verification is implemented.

- [ ] **Step 7: Retire embedded-key root export and implement full verification**

Implement the spec §5.3 order exactly. The signature payload is:

```text
xm-audit-archive-manifest-v1
sha256=<64hex>
```

Make new exported root JSON the exact `ChainRootRefV1` wire (no `public_key` or
derived `signed_payload`) and remove the `pub` argument/trust
path from `ExportRoot`; verification requires `chain_root_signing/v1` Keyring lookup.
Make export a pure filesystem emitter: remove the `MarkChainRootExported` query and
generated method, and remove every `exported_at/export_target` DB write from `ExportRoot`.
Keep legacy columns untouched in the forward-only schema but never read them as trust or
archive state. Add `TestExportRootPerformsZeroDatabaseWritesOnSuccessAndFailure` and
`TestExportRootRetryDoesNotMutateLegacyMarker`. Historical JSON public keys are ignored
hints. Unknown versions return only typed `*CompatibilityError`; object/keyring I/O
returns typed `*OperationalError`, never tamper.

- [ ] **Step 8: Add a local-only CLI surface**

`cmd/audit-archive` supports only:

```text
audit-archive plan --from <seq> --to <seq>
audit-archive export-local --to <seq> --root-id <uuid>
audit-archive verify-local --manifest <approved-local-path>
```

`plan` is read-only and prints range/rows/estimated bytes. `export-local` always starts
at sequence 1 and emits the exact genesis predecessor; it rejects any `--from` flag.
Later non-local continuation belongs to AUD3 and requires a trusted exact previous
manifest locator. `export-local` refuses production environment and writes only below the configured local archive root.
No command accepts SQL, object key, bucket, arbitrary executable or Delete flag.

- [ ] **Step 9: Test stable CLI exit codes and no-secret output**

```go
func TestCLIPlanDoesNotWriteFiles(t *testing.T)
func TestCLIExportLocalRejectsProduction(t *testing.T)
func TestCLIExportLocalRejectsPartialFirstSegmentAndFromFlag(t *testing.T)
func TestCLIVerifyWalksExactPreviousRefsFromTerminalToGenesis(t *testing.T)
func TestCLIVerifyMapsTamperToExitOneAndConfigToExitTwo(t *testing.T)
func TestCLIVerifyMapsOutdatedOperationalAndConflictSeparately(t *testing.T)
func TestCLIOutputNeverContainsKeyMaterial(t *testing.T)
```

Run:

```powershell
go test ./cmd/audit-archive ./internal/platform/audit/archive -count=1 -v
```

Expected: PASS.

- [ ] **Step 10: Document frozen wire, v1 caveat and external trust**

`ARCHIVE-FORMAT-v1.md` copies the exact payload/projection/manifest fields,
signing payload, limits, strict decode rules and typed codes from the approved spec.
Document that canonical v1 is collision-prone/non-injective and a valid v1 root is
not equivalent to v2 semantics. Update existing anchor/runbook docs in the same PR.

- [ ] **Step 11: Run full AUD1 verification**

Run all global gates. Also run:

```powershell
git diff --check
rg -n "Delete|Overwrite|TRUNCATE|DROP TABLE audit\.audit_event|DELETE FROM audit\.audit_event" internal/platform/audit/archive cmd/audit-archive docs/modules/audit
```

Expected: full gates exit 0; the search finds only explanatory prohibition text,
never an executable deletion capability.

- [ ] **Step 12: Commit AUD1**

```powershell
git add internal/platform/audit/archive internal/platform/audit/anchor.go internal/platform/audit/anchor_test.go db/queries/audit.sql internal/platform/audit/gen/audit.sql.go cmd/audit-archive docs/modules/audit docs/evidence/EV-<date>-audit-archive-baseline.md
git commit -m "feat(audit): add deterministic archive format and verifier"
```

Stop after PR creation and human review; do not start AUD2 from an unmerged AUD1 branch.

---

## Task 2（AUD2）: Exact-version Object Store、Recovery Index 与可重建 Catalog

**Approval gate:** AUD1 merged；迁移、新对象存储、供应商/region、Object Lock
模式/期限、KMS、数据驻留、PII/WORM 结论及**精确 SDK 版本**已回写批准记录。
如果批准记录没有精确 SDK module/version，AUD2 必须保持 blocked，不能自行挑依赖。

**Files:**

- Dynamically create: `db/migrations/${AUDIT_ARCHIVE_MIGRATION_ID}_audit_archive_catalog.up.sql`
- Dynamically create: `db/migrations/${AUDIT_ARCHIVE_MIGRATION_ID}_audit_archive_catalog.down.sql`
- Modify: `db/queries/audit.sql`
- Regenerate: `internal/platform/audit/gen/*`
- Create: `internal/platform/audit/archive/catalog.go`
- Create: `internal/platform/audit/archive/catalog_test.go`
- Create: `internal/platform/audit/archive/catalog_integration_test.go`
- Create: `internal/platform/audit/archive/objectstore.go`
- Create: `internal/platform/audit/archive/objectstore_test.go`
- Create: `internal/platform/audit/archive/checkpoint.go`
- Create: `internal/platform/audit/archive/checkpoint_test.go`
- Create: `internal/platform/audit/archive/recovery_index.go`
- Create: `internal/platform/audit/archive/recovery_index_test.go`
- Create: `internal/platform/audit/archive/catalog_rebuild_test.go`
- Create: `internal/platform/audit/archive/receipt_journal.go`
- Create: `internal/platform/audit/archive/receipt_journal_test.go`
- Create: `internal/platform/audit/archive/provider_qualification.go`
- Create: `internal/platform/audit/archive/provider_qualification_test.go`
- Create: `internal/platform/audit/archive/filesystem_store.go`
- Create: `internal/platform/audit/archive/filesystem_store_test.go`
- Create after provider approval: `internal/platform/audit/archive/s3_store.go`
- Create after provider approval: `internal/platform/audit/archive/s3_store_test.go`
- Modify after provider approval: `go.mod`
- Modify after provider approval: `go.sum`
- Modify after provider approval: `VERSIONS.lock`
- Modify: `deploy/compose/.env.example`
- Modify: `docs/modules/audit/DATA-MODEL.md`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Create: `docs/evidence/TEMPLATE-audit-archive-provider-qualification.md`

**Interfaces:**

```go
type ObjectWriteIntentV1 struct {
    OperationID uuid.UUID
    Ordinal int32
    BucketID, Key, SHA256 string
    SizeBytes int64
    ContentType, EncryptionMode, KMSKeyID, ObjectLockMode string
    RetainUntil WireTime
    ProviderIdempotencyToken string
}
type FixedLocator struct { ApprovedConfigRef string }
type IndexVersion struct { Generation int64; SHA256, ProviderVersion string }
type ExpectedIndex struct { Generation int64; SHA256, ProviderVersion string }
type OperationIntentV1 struct {
    OperationID uuid.UUID
    ApprovalEnvelopeSHA256, DeterministicBytesDigest string
    Objects []ObjectWriteIntentV1
}
type OperationReceiptV1 struct {
    Intent OperationIntentV1
    PutResults []ObjectVersionV1
    TerminalResultRef *ArtifactRefV1
    TerminalResultDigest string
}

type ObjectWriter interface {
    PutIfAbsent(context.Context, ObjectWriteIntentV1, io.Reader) (ObjectVersionV1, error)
    RecoverPutResult(context.Context, ObjectWriteIntentV1) (ObjectVersionV1, error)
}
type ExactObjectReader interface {
    HeadVersion(context.Context, ObjectVersionV1) (ObjectVersionV1, error)
    GetVersion(context.Context, ObjectVersionV1) (io.ReadCloser, ObjectVersionV1, error)
}
type RecoveryIndexReader interface {
    LoadCurrent(context.Context, FixedLocator) (SignedRecoveryIndexV1, IndexVersion, error)
}
type RecoveryIndexCASWriter interface {
    CompareAndSwap(context.Context, FixedLocator, ExpectedIndex, SignedRecoveryIndexV1) (IndexVersion, error)
}
type ReceiptJournal interface {
    BeginIntent(context.Context, OperationIntentV1) error
    AppendPutResult(context.Context, uuid.UUID, int32, ObjectVersionV1) error
    AppendTerminalResult(context.Context, uuid.UUID, ArtifactRefV1, string) error
    LoadOperation(context.Context, uuid.UUID) (OperationReceiptV1, error)
}

type CommittedSegment struct {
    ID uuid.UUID
    Manifest SignedManifestV1
    ManifestObject ObjectVersionV1
    CheckpointSHA256 string
    RecoveryGeneration int64
    CommittedAt time.Time
    VerifiedAt time.Time
}

type CatalogReader interface {
    Latest(context.Context) (CommittedSegment, error)
    ListBefore(context.Context, int64, int32) ([]CommittedSegment, error)
}
type CatalogWriter interface { CommitCoveredSegment(context.Context, CommittedSegment) error }
```

There is intentionally no staged catalog type, latest-object read, arbitrary List,
Delete or general Overwrite. RecoveryIndex CAS is a separate fixed-locator interface.

- [ ] **Step 1: Resolve the migration number from the fresh base**

Use PowerShell without assuming `000014` remains free:

```powershell
$migrationNumbers = Get-ChildItem db/migrations/*.up.sql |
  ForEach-Object { [int]($_.BaseName.Split('_')[0]) }
$auditArchiveMigrationId = '{0:D6}' -f (($migrationNumbers | Measure-Object -Maximum).Maximum + 1)
$env:AUDIT_ARCHIVE_MIGRATION_ID = $auditArchiveMigrationId
Write-Output $env:AUDIT_ARCHIVE_MIGRATION_ID
```

Record the resulting concrete filename in the AUD2 Task Spec and PR before editing.
Do not create the migration until Step 2's tests are red. Re-run the number calculation
immediately before drafting；a changed max migration is STOP and requires a fresh base.

- [ ] **Step 2: Write migration contract tests first**

Create integration assertions for:

```text
archive_segment accepts one valid committed row
UPDATE changes zero rows
DELETE changes zero rows
duplicate from/to/payload hash/manifest hash is rejected
invalid range/count/hash is rejected
catalog cannot skip latest.to+1 through CatalogWriter.CommitCoveredSegment
catalog rejects a row not covered by signed checkpoint/recovery generation
empty catalog rebuilds deterministically from RecoveryIndex without original DB rows
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestCatalog' -count=1 -v
```

Expected: FAIL because schema/catalog do not exist.

- [ ] **Step 3: Draft the exact migration/query diff, obtain approval, then generate**

Draft the exact numbered up/down files and audit queries. The table contains only rows
covered by checkpoint/recovery generation and both no-update/no-delete rules. Record
base SHA + `git diff --binary ... | git hash-object --stdin` digest, then **STOP**.
Do not apply migration, run sqlc, edit provider/runtime code or stage files until the
migration owner approves those exact bytes. Upstream migration, renumbering or one-byte
DDL/query change invalidates approval: refresh base, regenerate, and STOP again.

Only after exact-diff approval run:

```powershell
go tool sqlc generate
git diff --exit-code -- sqlc.yaml
```

Expected: sqlc generation succeeds; `sqlc.yaml` does not change merely to add
queries to the existing audit query file.

- [ ] **Step 4: Implement transactional contiguous Commit**

`CatalogWriter.CommitCoveredSegment` takes a dedicated PostgreSQL advisory transaction lock, reads
the latest committed range, requires `new.from=latest.to+1`, then inserts once.
It never writes audit events or changes existing catalog rows.

- [ ] **Step 5: Write exact-version object/recovery red tests**

```go
func TestObjectStoreInterfaceHasNoDeleteOrOverwrite(t *testing.T)
func TestPutIfAbsentIsIdempotentForSameContent(t *testing.T)
func TestPutIfAbsentRejectsSameKeyDifferentContent(t *testing.T)
func TestPutReturnsCompleteObjectVersionV1FromExactReadback(t *testing.T)
func TestAmbiguousSuccessfulPutRecoversSameExactVersionWithoutLatestOrList(t *testing.T)
func TestUnrecoverableAmbiguousPutFailsProviderQualification(t *testing.T)
func TestObjectKeyRejectsTraversalAndUnknownEnvironment(t *testing.T)
func TestHeadAndGetReturnRecordedHashAndSize(t *testing.T)
func TestReaderRequiresExactVersionIDAndNeverUsesLatest(t *testing.T)
func TestReadbackRejectsWrongKMSKeyAndObjectLockMetadata(t *testing.T)
func TestFilesystemStoreNeverWritesOutsideRoot(t *testing.T)
func TestCheckpointBindsTerminalManifestKeyVersionAndCatalogDigest(t *testing.T)
func TestRecoveryIndexRequiresIndependentSignatureAndFixedLocatorCAS(t *testing.T)
func TestRecoveryIndexCanRebuildEmptyCatalogAfterDatabaseLoss(t *testing.T)
func TestDatabaseLossWalksTerminalManifestExactRefsBackToGenesis(t *testing.T)
func TestDatabaseLossRecoveryNeverCallsListLatestOrOriginalCatalog(t *testing.T)
func TestReceiptIntentMustBeDurableBeforeFirstPut(t *testing.T)
func TestReceiptJournalRejectsSameOperationDifferentBytesOrVersion(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestObject|TestPutIfAbsent|TestFilesystem' -count=1 -v
```

Expected: FAIL before implementations, then PASS after minimal interface and
filesystem fixture are added.

- [ ] **Step 6: Pin and implement the approved S3-compatible adapter**

Use only the exact module/version recorded by approval, update `VERSIONS.lock`,
and construct credentials through `SecretProvider`/CredentialRef. Configure
conditional create (`If-None-Match: *` or provider-equivalent), checksum,
server-side KMS encryption and exact Object Lock headers. Persist provider VersionID;
all readback uses that VersionID and validates KMS key/lock mode/retain-until. Redirects, public
ACL and endpoint hosts outside the configured allowlist fail closed.

The approval record must also name the provider-native operation/idempotency primitive
used by `RecoverPutResult`. A generic S3 compatibility claim is insufficient. If an
ambiguous success can only be recovered with latest Head, arbitrary ListObjectVersions,
or a second Put, mark the adapter `ProviderQualificationFailed` and STOP AUD2.

- [ ] **Step 7: Test the provider adapter without production credentials**

Use an `httptest` protocol fixture for request signing/headers and a disposable
WORM-capable integration bucket only when the approved CI secret is present.
No test logs headers or secret values.

Inject a response drop after the provider has committed Put but before VersionID reaches
the caller. The adapter must recover the same exact `ObjectVersionV1` through the approved
intent-token primitive. Record SDK version, request/result digest, recovered VersionID
hash, KMS/Lock facts and PASS/FAIL in the provider-qualification evidence; never record
credentials or payload.

```powershell
go test ./internal/platform/audit/archive -run 'TestS3' -count=1 -v
```

Expected: protocol tests PASS; live provider test reports explicit SKIP when its
dedicated test CredentialRef is absent.

- [ ] **Step 8: Prove commit happens only after readback verification**

Use fake ObjectWriter/ExactObjectReader/RecoveryIndexReader/RecoveryIndexCASWriter/
ReceiptJournal/CatalogReader/CatalogWriter to cover every failure point:

```go
func TestCommitProtocolDoesNotCatalogPartialUpload(t *testing.T)
func TestCommitProtocolDoesNotCatalogFailedHeadOrGet(t *testing.T)
func TestCommitProtocolDoesNotCatalogHashOrSignatureFailure(t *testing.T)
func TestCommitProtocolCatalogsExactlyOnceAfterAllObjectsVerify(t *testing.T)
func TestConcurrentCommitOnlyAcceptsOneContiguousSegment(t *testing.T)
func TestRetryReusesContentAddressedOrphanObjects(t *testing.T)
func TestCrashAfterPutBeforeResponseUsesRecoverPutResultAndReceipt(t *testing.T)
func TestReceiptFailureBeforePutMakesZeroProviderCalls(t *testing.T)
func TestCatalogNeverLeadsRecoveryIndex(t *testing.T)
func TestCatalogReconcilesAfterIndexCASBeforeDatabaseWriteCrash(t *testing.T)
```

The order is durable intent -> payload/projection exact-version verify + append result
-> manifest exact-version verify + append result
-> signed checkpoint exact-version verify -> RecoveryIndex CAS terminal commit ->
idempotent catalog materialization. Manifest cannot be signed until payload/projection
VersionIDs and protection metadata are known. Catalog failure after CAS is repaired
from checkpoint; it never rolls back the index.

- [ ] **Step 9: Run AUD2 full gates and inspect migration safety**

In addition to global gates:

```powershell
git diff --check
rg -n "func .*Delete|func .*Overwrite|DELETE FROM audit\.audit_event|TRUNCATE audit\.audit_event|DROP TABLE audit\.audit_event" internal/platform/audit/archive db/migrations db/queries
$actualDiffDigest = (git diff --binary <approved-base> -- db/migrations/$env:AUDIT_ARCHIVE_MIGRATION_ID`_audit_archive_catalog.up.sql db/migrations/$env:AUDIT_ARCHIVE_MIGRATION_ID`_audit_archive_catalog.down.sql db/queries/audit.sql | git hash-object --stdin)
if ($actualDiffDigest -ne '<approved-diff-git-blob-sha>') { throw 'STOP: migration/query diff changed; approval invalid' }
```

Expected: no executable destructive path; only catalog no-delete rule and
negative test/prohibition text may match. The migration/query diff must equal the
human-approved digest; a mismatch is STOP, not a request to approve after execution.

- [ ] **Step 10: Commit AUD2**

Stage the two exact approved migration paths explicitly rather than using a broad
directory/glob, then commit. Application rollback leaves the forward schema and
immutable objects/index in place; no down migration is executed as release rollback:

```powershell
git add db/migrations/$env:AUDIT_ARCHIVE_MIGRATION_ID`_audit_archive_catalog.up.sql db/migrations/$env:AUDIT_ARCHIVE_MIGRATION_ID`_audit_archive_catalog.down.sql db/queries/audit.sql internal/platform/audit/gen internal/platform/audit/archive go.mod go.sum VERSIONS.lock deploy/compose/.env.example docs/modules/audit docs/evidence/TEMPLATE-audit-archive-provider-qualification.md
git commit -m "feat(audit): add immutable archive store and catalog"
```

Stop for migration/object-storage review and human merge.

---

## Task 3（AUD3）: Trusted PLO、Terminal CAS 与人工 Lifecycle Operation

**Approval gate:** AUD2 merged；PLO/Root/Manifest/Checkpoint/Recovery key purpose、
CredentialRef、fixed RecoveryIndex locator、Kill Switch、archive identities 与 exact
DB/IAM role matrix、AccessRecorder 独立 append-only security sink/retention approved；
DB role split merged。R2-10 缺失只允许 manual CLI。

**Files:**

- Create: `internal/platform/audit/archive/service.go`
- Create: `internal/platform/audit/archive/service_test.go`
- Create: `internal/platform/audit/archive/service_integration_test.go`
- Create: `internal/platform/audit/archive/envelope.go`
- Create: `internal/platform/audit/archive/envelope_test.go`
- Create: `internal/platform/audit/archive/receipt.go`
- Create: `internal/platform/audit/archive/receipt_test.go`
- Create: `internal/platform/audit/archive/kill_switch.go`
- Create: `internal/platform/audit/archive/kill_switch_test.go`
- Create: `internal/platform/audit/archive/scrub_receipt.go`
- Create: `internal/platform/audit/archive/scrub_receipt_test.go`
- Create: `internal/platform/audit/archive/scrub_head.go`
- Create: `internal/platform/audit/archive/scrub_head_test.go`
- Create: `internal/platform/audit/archive/access_recorder.go`
- Create: `internal/platform/audit/archive/access_recorder_test.go`
- Create: `internal/platform/audit/archive/access_recorder_integration_test.go`
- Create: `internal/platform/audit/archive/reconcile.go`
- Create: `internal/platform/audit/archive/reconcile_test.go`
- Create: `internal/platform/audit/archive/roles_integration_test.go`
- Create: `internal/platform/audit/archive/commit.go`
- Create: `internal/platform/audit/archive/commit_test.go`
- Modify: `cmd/audit-archive/main.go`
- Modify: `cmd/audit-archive/main_test.go`
- Create: `cmd/audit-archive/config.go`
- Create: `cmd/audit-archive/config_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Create: `deploy/bootstrap/003_audit_archive_grants_evidence.sql`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Create: `docs/runbooks/AUDIT-ARCHIVE.md`
- Create: `docs/evidence/TEMPLATE-audit-archive-run.md`
- Create: `docs/evidence/TEMPLATE-audit-access-recorder.md`

**Interfaces:**

```go
type ApproverV1 struct { PrincipalID, ApprovalRole string; ApprovedAt WireTime }
type TargetPolicyV1 struct {
    Provider, SDKProtocol, BucketID, Region, Prefix string
    KMSKeyID, ObjectLockMode string
    RetainUntil WireTime
    Residency string
}
type PLOApprovalEnvelopeV1 struct {
    Kind string
    FormatVersion int
    OperationID uuid.UUID
    ChangeID, ApprovalID string
    Approvers []ApproverV1
    Purpose, Environment, SourceDatabaseFingerprint string
    IsolatedTargetFingerprint string
    TargetPolicy TargetPolicyV1
    RecoveryIndexFixedLocatorRef string
    ExpectedSourceTip int64
    ExpectedRootHash string
    ExpectedIndexGeneration int64
    ExpectedIndexHash string
    FromSequence, ToSequence int64
    ArchiveFormatVersion int
    BuildCommit, BinarySHA256 string
    ValidFrom, ValidUntil WireTime
    Nonce, IdempotencyKey, DryRunDigest, KillSwitchSnapshotSHA256 string
}
type SignedPLOApprovalEnvelopeV1 struct {
    Unsigned PLOApprovalEnvelopeV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type KillSwitchSnapshotV1 struct {
    Kind string
    FormatVersion int
    Environment, Purpose string
    Generation int64
    Enabled bool
    ValidFrom, ValidUntil, IssuedAt WireTime
    Issuer string
}
type SignedKillSwitchSnapshotV1 struct {
    Unsigned KillSwitchSnapshotV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type PLOResultReceiptV1 struct {
    Kind string
    FormatVersion int
    OperationID uuid.UUID
    ApprovalEnvelopeSHA256, Purpose string
    StartedAt, FinishedAt WireTime
    ObjectResults []ObjectVersionV1
    Checkpoint ArtifactRefV1
    RecoveryGeneration int64
    RecoveryIndexSHA256 string
    VerificationReport VerificationReport
    ScrubReceipt ArtifactRefV1
    ExitCode int
    Outcome, ResultDigest string
}
type SignedPLOResultReceiptV1 struct {
    Unsigned PLOResultReceiptV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type ScrubReceiptV1 struct {
    Kind string
    FormatVersion int
    ReceiptID uuid.UUID
    Checkpoint ArtifactRefV1
    RecoveryGeneration int64
    RecoveryIndexSHA256, Scope, VerifierCommit, VerifierBinarySHA256 string
    StartedAt, VerifiedAt WireTime
    ManifestCount, ObjectCount, RowCount int64
    ChainIntegrity, CaptureCompleteness string
    EligibleActionRunCount, MatchedActionRunCount int64
    MissingAuditEventCount, DuplicateAuditEventCount int64
    Caveats []string
    ResultDigest string
}
type SignedScrubReceiptV1 struct {
    Unsigned ScrubReceiptV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}
type ScrubHeadV1 struct {
    Kind string
    FormatVersion int
    Generation, PreviousGeneration int64
    PreviousHeadSHA256 string
    Receipt ArtifactRefV1
    CheckpointSHA256 string
    UpdatedAt WireTime
}
type SignedScrubHeadV1 struct {
    Unsigned ScrubHeadV1
    UnsignedSHA256, SignatureAlgorithm, SignatureKeyID, Signature string
}

type SourceTipV1 struct { Sequence int64; Hash string; ObservedAt WireTime }
type EligibleActionRunV1 struct { ID uuid.UUID; FinishedAt WireTime; Status string }
type AuditSourceReader interface {
    BeginRepeatableReadSnapshot(context.Context) (AuditSourceSnapshot, error)
}
type AuditSourceSnapshot interface {
    Tip(context.Context) (SourceTipV1, error)
    ListRange(context.Context, int64, int64, int32) ([]audit.Event, error)
    VerifyChain(context.Context, int64, int64) (*audit.ChainProblem, error)
    LatestRoot(context.Context) (audit.ChainRoot, error)
    ListEligibleActionRuns(context.Context, WireTime, int32) ([]EligibleActionRunV1, error)
    Close(context.Context) error
}
type RootWriter interface { InsertRoot(context.Context, ChainRootRefV1) error }
type KillSwitchReader interface { LoadCurrent(context.Context, string) (SignedKillSwitchSnapshotV1, error) }
type AccessRecordV1 struct {
    OperationID uuid.UUID
    Phase, CheckpointSHA256, ObjectSHA256, MachineIdentity string
    ApprovalEnvelopeSHA256, Outcome string
    RecordedAt WireTime
}
type AppendOnlySecuritySink interface { Append(context.Context, AccessRecordV1) error }
type AccessRecorder interface {
    RecordRestrictedReadAttempt(context.Context, AccessRecordV1) error
    RecordRestrictedReadResult(context.Context, AccessRecordV1) error
}
type ScrubReceiptWriter interface { PutIfAbsent(context.Context, SignedScrubReceiptV1) (ArtifactRefV1, error) }
type ScrubHeadReader interface { LoadCurrent(context.Context, FixedLocator) (SignedScrubHeadV1, IndexVersion, error) }
type ScrubHeadCASWriter interface { CompareAndSwap(context.Context, FixedLocator, ExpectedIndex, SignedScrubHeadV1) (IndexVersion, error) }

type ArchivePlan struct {
    FromSequence       int64
    ToSequence         int64
    EstimatedRows      int64
    EstimatedBytes     int64
    ChainRootRequired  bool
}

type ArchiveResult struct {
    ChainRoot ChainRootRefV1
    Segments  []CommittedSegment
    Receipt SignedPLOResultReceiptV1
}

type Service struct {
    SourceReader AuditSourceReader
    RootWriter RootWriter
    CatalogReader CatalogReader
    CatalogWriter CatalogWriter
    ObjectWriter ObjectWriter
    ObjectReader ExactObjectReader
    RecoveryIndexReader RecoveryIndexReader
    RecoveryIndexWriter RecoveryIndexCASWriter
    AccessRecorder AccessRecorder
    ReceiptJournal ReceiptJournal
    KillSwitchReader KillSwitchReader
    ScrubReceiptWriter ScrubReceiptWriter
    ScrubHeadReader ScrubHeadReader
    ScrubHeadWriter ScrubHeadCASWriter
    RootSigner, ManifestSigner, CheckpointSigner, RecoverySigner PurposeSigner
    ResultSigner, ScrubReceiptSigner, ScrubHeadSigner PurposeSigner
    PLOKeyring, RootKeyring, ManifestKeyring, CheckpointKeyring, RecoveryKeyring Keyring
    ResultKeyring, KillSwitchKeyring, ScrubReceiptKeyring, ScrubHeadKeyring Keyring
}

func (s *Service) Plan(context.Context, TargetPolicyV1) (ArchivePlan, []byte, error)
func (s *Service) Archive(context.Context, SignedPLOApprovalEnvelopeV1) (ArchiveResult, error)
```

- [ ] **Step 1: Write PLO precondition failures**

```go
func TestArchiveRequiresTrustedUnexpiredApprovalEnvelope(t *testing.T)
func TestApprovalRequiresTwoDistinctEligibleHumanApproversAndAllFrozenFields(t *testing.T)
func TestApprovalAndResultReceiptAreDifferentSignedDomains(t *testing.T)
func TestArchiveRejectsWrongPurposeProtocolFingerprintAndChangedEnvelope(t *testing.T)
func TestArchiveRequiresExactTipRootAndIndexGeneration(t *testing.T)
func TestArchiveStartsAtLatestCommittedPlusOne(t *testing.T)
func TestArchiveRefusesBrokenDatabaseChain(t *testing.T)
func TestArchiveRefusesMissingOrUntrustedRootAndManifestKeys(t *testing.T)
func TestArchiveDryRunWritesNothing(t *testing.T)
func TestArchiveDoesNotExposeSecretsInErrorsOrLogs(t *testing.T)
func TestArchiveFailsClosedWhenKillSwitchUnavailableDisabledOrExpired(t *testing.T)
func TestArchiveRechecksBoundKillSwitchBeforeSignPutCASAndNextSegment(t *testing.T)
func TestEverySignerMatchesItsPurposeProtocolAndRecoverySignerIsPresent(t *testing.T)
func TestReaderWriterAccessRecorderCapabilitiesStaySeparated(t *testing.T)
func TestAccessRecorderAttemptFailurePreventsGetAndResultFailurePreventsCommit(t *testing.T)
func TestArchiveDBRolesHaveExactPositiveAndNegativeGrants(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestArchive' -count=1 -v
```

Expected: FAIL until orchestration exists.

- [ ] **Step 2: Implement pure Plan -> exact envelope candidate**

Plan reads tip/root/RecoveryIndex/catalog and approved target policy, then emits exact
`PLOApprovalEnvelopeV1` canonical bytes + digest for human approval. It includes the
two sorted HUMAN approvers, purpose, half-open validity, dry-run digest and bound signed
Kill Switch hash, but no unknowable object result or exit code. It does not sign approval,
create root, upload, CAS or write DB. Run accepts only a signed envelope artifact;
bare approval ID and range/bucket/KMS/target CLI overrides are rejected.

- [ ] **Step 3: Implement ActionRun reconciliation and deterministic receipt**

Use one repeatable-read source snapshot and approved settle window to freeze eligible
terminal ActionRuns and audit tip. Report eligible/matched/missing/duplicate by
`action_run_id`; keep `chain_integrity` and `capture_completeness` separate and surface
the v1 non-injective caveat. Before the first Put, persist `OperationIntentV1` with all
object bytes/hashes, IDs, signing times and idempotency tokens in ReceiptJournal; append
each exact VersionID result before moving on. After terminal CAS, sign and append a
separate `PLOResultReceiptV1`. Retry cannot mint new UUID/time/signature or conflate the
approval input with the result artifact.

- [ ] **Step 4: Select/reuse trusted pending root without `export_target`**

Verify RecoveryIndex/checkpoint first and reconcile lagging catalog, then inspect root.
If root.to is ahead, fully verify chain/root with the trusted root key and reuse it;
otherwise sign exactly through the envelope tip. Never call/update legacy
`export_target`. No-op requires index, reconstructed catalog and root at one trusted
tip and returns the prior receipt digest.

- [ ] **Step 5: Implement exact-version checkpoint/CAS/catalog protocol**

Follow spec §6.3: durable intent -> exact-version data verify -> manifest built from
complete `ObjectVersionV1` receipts and exact verify -> signed checkpoint exact verify ->
RecoveryIndex CAS terminal commit -> idempotent catalog materialization -> initial full
signed `ScrubReceiptV1` + signed CAS `ScrubHeadV1` -> signed PLO result receipt.
AccessRecorder attempt/result failure blocks Restricted Get delivery and commit. A later
segment cannot publish if an earlier one failed.

- [ ] **Step 6: Fault-inject every crash/CAS boundary**

```go
func TestRetryReusesSameEnvelopeBytesIDsTimesAndObjectVersions(t *testing.T)
func TestAmbiguousSuccessfulPutRecoversExactVersionOrStopsProvider(t *testing.T)
func TestCrashBeforeCASLeavesOnlyReusableOrphans(t *testing.T)
func TestAmbiguousCASReadsBackProposedExpectedOrConflict(t *testing.T)
func TestCrashAfterCASRepairsCatalogWithoutRepublish(t *testing.T)
func TestCatalogAheadOfIndexIsIntegrityIncident(t *testing.T)
func TestConcurrentRunsCannotForkManifestCheckpointOrIndex(t *testing.T)
func TestFailedEarlierSegmentCannotPublishLaterSegment(t *testing.T)
func TestCrashAfterIndexBeforeScrubHeadAndResultReceiptConvergesWithoutRepublish(t *testing.T)
```

- [ ] **Step 7: Constrain CLI and package lifecycle binary**

Approved command surface:

```text
audit-archive plan --policy <approved-readonly-config-ref>
audit-archive run --envelope <signed-approved-envelope-path>
audit-archive verify --recovery-index <approved-fixed-locator-ref>
```

Bucket/prefix/credential refs come from signed envelope + approved config, not flags.
Add a Docker target only; no daemon/compose/worker/cron/restart/default enablement.

- [ ] **Step 8: Prove exact DB/IAM roles and non-production round trip**

Run positive/negative `SET ROLE` and provider-policy tests separately for
AuditSourceReader snapshot fixed SELECT, RootWriter root INSERT, CatalogReader SELECT,
CatalogWriter INSERT, ObjectWriter fixed-prefix conditional Put/recover-result,
ExactObjectReader signed-version Get/Head, RecoveryIndexReader fixed load,
RecoveryIndexCASWriter fixed CAS, ReceiptJournal append, ScrubReceipt/Head writer and
AccessRecorder's independent append-only security sink. Each uses a distinct CredentialRef;
owner/superuser/bucket-admin/latest/list/delete or recorder writing `audit_event` is STOP.

Against the approved disposable test bucket and test database:

```text
plan -> approved test envelope -> root -> exact-version data/manifests/checkpoint
-> RecoveryIndex CAS -> catalog materialize -> independent verify/reconcile
```

Record object hashes and exit codes without recording payload contents or keys.

- [ ] **Step 9: Run AUD3 full gates and commit**

```powershell
git diff --check
git add internal/platform/audit/archive cmd/audit-archive deploy/docker/go.Dockerfile deploy/bootstrap/003_audit_archive_grants_evidence.sql docs/modules/audit docs/runbooks/AUDIT-ARCHIVE.md docs/evidence/TEMPLATE-audit-archive-run.md docs/evidence/TEMPLATE-audit-access-recorder.md
git commit -m "feat(audit): add trusted archive lifecycle operation"
```

Stop after PR. Absence of a scheduled job is an acceptance criterion, not missing work.

---

## Task 4（AUD4）: Transparent Hot/Cold Query 与 Coverage

**Approval gate:** AUD2/AUD3 merged；产品负责人批准新增顶层 `coverage` 与明确
503/error codes；安全负责人批准 cold projection 读取边界。若 coverage 未批准，
不以响应 header 或前端猜测绕过契约门。

**Files:**

- Create: `internal/platform/audit/query.go`
- Create: `internal/platform/audit/query_test.go`
- Create: `internal/platform/audit/cold_reader.go`
- Create: `internal/platform/audit/cold_reader_test.go`
- Create: `internal/platform/audit/query_integration_test.go`
- Modify: `internal/platform/httpapi/audit.go`
- Modify: `internal/platform/httpapi/audit_test.go`
- Modify: `internal/platform/httpapi/audit_integration_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `cmd/platform-api/config.go`
- Modify: `cmd/platform-api/config_test.go`
- Modify: `web/apps/admin-web/src/api/platform.ts`
- Modify: `web/apps/admin-web/src/api/platform.test.ts`
- Modify: `web/apps/admin-web/src/lib/audit.ts`
- Modify: `web/apps/admin-web/src/lib/audit.test.ts`
- Modify: `web/apps/admin-web/src/pages/AuditPage.tsx`
- Modify: `docs/modules/httpapi/README.md`
- Modify: `docs/modules/httpapi/PERMISSIONS.md`
- Modify: `docs/modules/audit/README.md`

**Interfaces:**

```go
type SourceTier string

const (
    TierHot  SourceTier = "hot"
    TierCold SourceTier = "cold"
)

type Coverage struct {
    Complete                  bool
    OldestAvailableSequence   int64
    ArchiveCheckpointSequence int64
    ArchiveCheckpointCommittedAt *time.Time
    ArchiveVerifiedAt         *time.Time
    ArchiveLagEvents          *int64
    ArchiveLagSeconds         *int64
    SourceTiers               []SourceTier
    ChainIntegrity            IntegrityState
    CaptureCompleteness       CompletenessState
    Caveats                   []string
    SnapshotID                *string
}

type Page struct {
    Events      []Event
    NextBefore  int64
    Coverage    Coverage
}

type Query interface {
    ListRecent(context.Context, string, int64, int32) (Page, error)
}
```

Typed results/errors map only through `errors.Is/As`:

```text
AUDIT_ARCHIVE_UNAVAILABLE             -> 503
AUDIT_ARCHIVE_QUERY_BUDGET_EXCEEDED   -> 503
AUDIT_ARCHIVE_VERIFIER_OUTDATED       -> 503
AUDIT_ARCHIVE_INTEGRITY_FAILED        -> 500 + integrity incident log
```

Operational object/KMS/keyring/discovery timeout -> unavailable；unknown archive/
canonical version -> verifier outdated；deterministic budget -> budget exceeded；
signature/hash/chain/protection mismatch -> integrity failed。Unknown errors remain
generic 500；no response exposes object key/VersionID/KMS/payload/key material.
`verifier_outdated` is only `*archive.CompatibilityError`, never a
`VerificationReport.Code`; one handler adapter owns the `errors.As` mapping and its tests.

- [ ] **Step 1: Write cross-tier Query red tests**

```go
func TestQueryUsesHotOnlyWhenPageIsCovered(t *testing.T)
func TestQueryCrossesHotColdBoundaryWithoutDuplicateOrMissingSequence(t *testing.T)
func TestQueryPreservesOpenBeforeSequenceCursor(t *testing.T)
func TestQueryFiltersEnvironmentWithoutTreatingGlobalGapsAsDamage(t *testing.T)
func TestQuerySkipsSegmentsWithZeroEnvironmentCount(t *testing.T)
func TestQueryNeverReadsFullPayloadForHumanList(t *testing.T)
func TestQueryReturnsNoPartialItemsWhenCommittedColdObjectUnavailable(t *testing.T)
func TestQueryFailsExplicitlyOnIntegrityAndVerifierVersionErrors(t *testing.T)
func TestQueryMapsOperationalErrorsWithoutCallingThemIntegrityFailures(t *testing.T)
func TestQueryEnforcesObjectByteAndCountBudget(t *testing.T)
func TestCoverageFieldsFollowCheckpointNotCatalogOrConfiguration(t *testing.T)
func TestCoverageSeparatesChainIntegrityCaptureCompletenessAndV1Caveat(t *testing.T)
func TestCoverageVerifiedAtOnlyUsesTrustedScrubHeadAndExactReceipt(t *testing.T)
func TestCoverageNeverInfersVerifiedAtFromCatalogHeadOrAttemptTime(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit -run 'TestQuery' -count=1 -v
```

Expected: FAIL before Query/cold reader exist.

- [ ] **Step 2: Implement cold projection verification and streaming**

Cold reader starts from verified RecoveryIndex/checkpoint, selects catalog ranges only
when catalog generation/digest matches, and reads the signed projection VersionID.
It checks key purpose/protocol, KMS/Lock/hash before strict decoding, skips zero-count
environments, enforces 32 objects/64 MiB/deadline and never opens payload objects.
Coverage separately verifies `ScrubHeadV1` and its exact `ScrubReceiptV1`; it uses scrub
facts only when their checkpoint hash equals the current RecoveryIndex checkpoint.

- [ ] **Step 3: Implement deterministic hot-first merge**

Read hot first. If fewer than limit and older committed ranges are required,
continue from the exact oldest hot sequence into cold. Deduplicate by sequence,
sort descending, enforce environment, compute one `NextBefore`, and produce
coverage from verified checkpoint/signed ScrubReceipt/source facts rather than catalog
max, object Head, last attempt or config.
Any required-tier failure returns no items；environment sequence gaps stay normal.

- [ ] **Step 4: Change HTTP tests before handler implementation**

Assert:

```json
"coverage": {
  "complete": true,
  "oldest_available_sequence": 1,
  "archive_checkpoint_sequence": 108,
  "archive_checkpoint_committed_at": "2026-08-28T10:00:00Z",
  "archive_verified_at": "2026-08-28T10:00:00Z",
  "archive_lag_events": 0,
  "archive_lag_seconds": 1200,
  "source_tiers": ["hot", "cold"],
  "chain_integrity": "verified",
  "capture_completeness": "gaps_found",
  "caveats": ["legacy_canonical_v1_non_injective"],
  "snapshot_id": "sha256:<checkpoint_sha256>"
}
```

Also assert the existing item keys remain byte-for-byte unchanged, caller-supplied
environment remains ignored, limit stays capped at 100, and a cold error response
contains no items/payload/object key. Pin every coverage field to spec §7.2, including
empty/no-checkpoint nulls；`complete` means request-window resolution only and never
substitutes for capture completeness.

- [ ] **Step 5: Implement handler/router/config wiring**

Replace the lister interface with `audit.Query`. Keep `RequireScope(audit.read)`.
Object/keyring configuration is optional only when hot holds the full requested
range; malformed configured cold access fails startup rather than silently disabling
archive coverage.

- [ ] **Step 6: Write frontend contract red tests**

```typescript
it("preserves existing audit item fields and parses coverage")
it("shows hot/cold source and archive verification time")
it("does not call an object URL or expose object metadata")
it("shows explicit unavailable state instead of end-of-history")
it("does not label environment sequence gaps as broken chain")
```

Run:

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web run test
```

Expected: FAIL before API types/UI are changed; PASS after minimal coverage rendering.

- [ ] **Step 7: Verify PII field exclusion mechanically**

Backend and frontend contract tests must reject these cold projection keys:

```text
reason, approval_id, trace_id, source_ip,
connector_request_summary, connector_response_summary,
payload_object_key, manifest_object_key, kms_key_id
```

- [ ] **Step 8: Run AUD4 full gates and commit**

```powershell
git diff --check
git add internal/platform/audit internal/platform/httpapi internal/platform/action cmd/platform-api web/apps/admin-web/src docs/modules/httpapi docs/modules/audit
git commit -m "feat(audit): add transparent verified archive queries"
```

Stop for backend contract, security and UI review.

---

## Task 5（AUD5）: 隔离恢复演练与受控生产激活

**Approval gate:** AUD1～AUD4 merged；隔离恢复数据库、恢复机器身份、trusted
keyring 分发、演练窗口与证据保存位置批准。生产自动调度还必须有 R2-10 与 DB
角色拆分的已合入证据；任一缺失则 AUD5 只做手工恢复工具/演练，不启用 scheduler。

**Files:**

- Create: `internal/platform/audit/archive/restore.go`
- Create: `internal/platform/audit/archive/restore_test.go`
- Create: `internal/platform/audit/archive/restore_integration_test.go`
- Create: `internal/platform/audit/archive/scrub.go`
- Create: `internal/platform/audit/archive/scrub_test.go`
- Create: `cmd/audit-restore/main.go`
- Create: `cmd/audit-restore/main_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Create only after R2-10/role gates: `internal/platform/jobs/audit_archive.go`
- Create only after R2-10/role gates: `internal/platform/jobs/audit_archive_test.go`
- Modify only after R2-10/role gates: `internal/platform/jobs/client.go`
- Modify only after R2-10/role gates: `cmd/platform-worker/config.go`
- Modify only after R2-10/role gates: `cmd/platform-worker/config_test.go`
- Modify only after R2-10/role gates: `cmd/platform-worker/main.go`
- Modify only after R2-10/role gates: `deploy/compose/launch.yaml`
- Modify only after R2-10/role gates: `deploy/compose/.env.example`
- Create: `docs/runbooks/AUDIT-RESTORE.md`
- Modify: `docs/runbooks/AUDIT-ARCHIVE.md`
- Create: `docs/evidence/TEMPLATE-audit-restore-drill.md`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Modify: `docs/handoffs/CODEX-PROJECT-HANDOFF.md` only if the approved activation reveals a new durable environment pitfall

**Interfaces:**

```go
type RestoreRequest struct {
    SignedEnvelope   SignedPLOApprovalEnvelopeV1 // purpose="restore"
    DryRun           bool
}

type RestoreReport struct {
    FromSequence int64
    ToSequence   int64
    RowCount     int64
    ObjectCount  int64
    BytesRead    int64
    VerifiedRootHash string
    StartedAt    time.Time
    FinishedAt   time.Time
}

type Restorer struct {
    RecoveryIndex  RecoveryIndexReader
    Objects        ExactObjectReader
    AccessRecorder AccessRecorder
    KillSwitchReader KillSwitchReader
    PLOKeyring      Keyring
    RootKeyring    Keyring
    ManifestKeyring Keyring
    CheckpointKeyring Keyring
    RecoveryKeyring Keyring
    Target         *pgxpool.Pool
}

func (r *Restorer) Restore(context.Context, RestoreRequest) (RestoreReport, error)
```

Restore source discovery must work with the original PostgreSQL/catalog unavailable:
fixed RecoveryIndex -> signed checkpoint -> terminal manifest exact VersionID ->
manifest chain -> deterministic in-memory catalog rows/digest. Restore target must
have completed the approved platform migrations/bootstrap (so
referenced `core.environment` rows exist), its audit table must be empty, and it must
be explicitly marked isolated. The importer preserves all original fields, including
recorded/occurred times, IDs, hashes and canonical version.

- [ ] **Step 1: Write destructive-safety red tests**

```go
func TestRestoreDryRunDoesNotOpenTransactionOrWriteRows(t *testing.T)
func TestRestoreRejectsNonEmptyAuditTable(t *testing.T)
func TestRestoreRejectsTargetWithoutIsolationMarker(t *testing.T)
func TestRestoreRejectsTargetMissingReferencedEnvironment(t *testing.T)
func TestRestoreRejectsMissingApprovalAndExpectedRoot(t *testing.T)
func TestRestoreRejectsUnsignedExpiredOrChangedEnvelopeAndWrongTargetFingerprint(t *testing.T)
func TestRestoreRequiresTrustedPLOKeyringAndBoundEnabledKillSwitch(t *testing.T)
func TestRestoreNeverUpdatesDeletesOrRehashesArchivedEvents(t *testing.T)
func TestRestoreStopsBeforeInsertOnAnyManifestObjectOrRootFailure(t *testing.T)
func TestRestoreDiscoversTerminalManifestAndRebuildsCatalogWithSourceDBGone(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestRestore' -count=1 -v
```

Expected: FAIL until restore safeguards exist.

- [ ] **Step 2: Implement dry-run verification before import**

Dry-run completes the entire manifest/object/root/chain validation, checks target
identity/emptiness and prints counts/bytes without beginning an insert transaction.

- [ ] **Step 3: Implement exact-field importer**

Use a dedicated, versioned importer with fixed INSERT columns. It does not invoke
`Store.Append`. Begin one target transaction only after object preflight；inside that
same transaction re-check isolation/emptiness, insert all rows, run a tx-bound
`VerifyChain(1, root.ToSequence)`, canonical counts/row count/tip/root and Query parity
checks, then COMMIT. Any verify failure ROLLBACKS all rows；no pool-based verifier may
observe another snapshot and no “commit then verify” path exists.

- [ ] **Step 4: Write full mixed-version round-trip test**

```go
func TestArchiveRestoreRoundTripPreservesEveryPersistedField(t *testing.T)
func TestRestoredMixedV1V2ChainMatchesTrustedRoot(t *testing.T)
func TestRestoredQueryMatchesPerEnvironmentCursorPages(t *testing.T)
func TestRestoreFailureRollsBackAllInsertedRows(t *testing.T)
func TestRestorePostInsertVerificationRunsInSameTransactionBeforeCommit(t *testing.T)
func TestRestoreRejectsMissingZeroCanonicalAndPartialFinalBoundary(t *testing.T)
```

Run against a loopback-only disposable PostgreSQL selected by the repository's
integration-test DSN guard:

```powershell
go test ./internal/platform/audit/archive -run 'TestArchiveRestore|TestRestored|TestRestoreFailure' -count=1 -v
```

Expected: PASS; no production/staging database is accepted as target.

- [ ] **Step 5: Add constrained restore CLI**

Supported surface:

```text
audit-restore plan --policy <approved-readonly-config-ref>
audit-restore run --envelope <signed-approved-restore-envelope-path>
```

Source fixed RecoveryIndex locator and target fingerprint come through signed envelope
+ approved config/CredentialRef；no arbitrary
SQL, table, object key, source URL or overwrite flag exists.

- [ ] **Step 6: Run and record an isolated staging drill**

The runbook sequence is fixed:

```text
trusted keyring -> RecoveryIndex/checkpoint -> rebuild catalog digest -> exact-version
manifest/object chain -> empty isolated DB same-tx import+audit-verify+root/query -> COMMIT
```

Measure source checkpoint time to obtain actual RPO and drill start-to-verified time
for actual RTO. Store only hashes/counts/timestamps/exit codes in evidence; no payload,
PII, DSN or key material.

- [ ] **Step 7: Add scheduler only if both hard gates are proven**

If R2-10 and DB role split are merged, first add failing tests:

```go
func TestAuditArchiveDisabledByDefaultUntilConfigured(t *testing.T)
func TestAuditArchiveRejectsNonPositiveIntervalAndRPO(t *testing.T)
func TestAuditArchiveUsesClusterLeaseAndSignedDeterministicEnvelope(t *testing.T)
func TestAuditArchiveDuplicateProbeReportsConcurrentOwner(t *testing.T)
func TestAuditArchiveKillSwitchPreventsRegistration(t *testing.T)
func TestAuditArchiveJobNeverDeletesHotEvents(t *testing.T)
func TestAuditArchiveScrubAndLagAlertsFailClosedAndPageAfterThreshold(t *testing.T)
```

Then register the hourly/10,000-event policy plus hourly current-checkpoint, daily
exact-object sample and weekly full chain/ActionRun scrub. Alert on lag events/seconds,
checkpoint age, scrub age, CAS conflict, catalog backlog and capture gaps using approved
thresholds. If either gate is absent, omit every scheduler file/config change and keep
manual-only；do not ship dormant code that looks production-ready.

- [ ] **Step 8: Verify RPO/RTO claims from evidence**

Only set runbook/status to “target met” when at least one fresh isolated drill shows:

```text
archive checkpoint lag <= 3600 seconds
restore start-to-full-verification <= 14400 seconds
full chain/root/query parity all PASS
capture completeness status/counts recorded (gaps cannot be hidden)
continuous scrub and lag alert evidence current
```

Otherwise report measured values and keep target status unmet. Rollback proof is
disable scheduler/cold read + previous binary while leaving forward schema, objects,
checkpoint/index and catalog intact；never run down migration, delete/rewrite object,
decrease Object Lock or CAS RecoveryIndex backward.

- [ ] **Step 9: Prove no physical slimming entered AUD1～AUD5**

```powershell
rg -n "DELETE FROM audit\.audit_event|TRUNCATE audit\.audit_event|DROP TABLE audit\.audit_event|DETACH PARTITION|VACUUM FULL" internal cmd db deploy docs/runbooks
```

Expected: no executable archive/restore path performs these operations. Existing
negative tests, rules or explanatory text must be manually classified in the PR.

- [ ] **Step 10: Run AUD5 full gates and commit**

```powershell
git diff --check
git add internal/platform/audit/archive cmd/audit-restore deploy/docker/go.Dockerfile docs/runbooks docs/evidence docs/modules/audit
```

If scheduler gates were proven, separately add only its approved files, then:

```powershell
git commit -m "feat(audit): add verified isolated archive restore"
```

Stop for human review. Do not activate production, merge, delete hot history or start
a physical-tiering slice from this PR.

---

## XM-C-AUD0 round-2 review closure matrix

| Review item | Normative closure | Red/green evidence location |
|---|---|---|
| DB-loss previous locator | `PreviousManifestRefV1` exact bucket/key/VersionID/SHA + unique genesis | AUD1 artifact wire tests；AUD2 terminal-to-genesis/no-DB/no-List tests |
| legacy root marker | AUD1 removes `MarkChainRootExported` query/generated method and all `export_target` writes | AUD1 zero-DB-write success/failure/retry tests + source search |
| frozen signed wire | independent `ChainRootRefV1/ManifestV1/CheckpointV1/RecoveryIndexV1` strict wire | AUD1 literal bytes/SHA, missing/null/order/time and cross-domain goldens |
| exact object proof | full `ObjectVersionV1` signed and compared to exact readback | AUD2 field-by-field KMS/Lock/checksum/VersionID tests |
| ambiguous Put | durable intent before Put + qualified `RecoverPutResult`; otherwise provider NO-GO | AUD2 response-loss/provider-qualification and receipt fault injection |
| precise capabilities | split Source snapshot/Root/Catalog/Object/Recovery/Receipt/Scrub/Access interfaces, credentials and `RecoverySigner` | AUD3 method-shape plus DB/IAM positive/negative integration evidence |
| PLO/Kill Switch | signed approval input, separately signed result receipt, bound/rechecked signed Kill Switch | AUD3 field/domain/validity/approver/recheck tests；restore repeats PLO verification |
| first boundary | AUD1 local export starts only at sequence 1；continuation requires trusted exact predecessor | AUD1 CLI rejection and terminal-to-genesis verifier tests |
| truthful verified time | append-only signed `ScrubReceiptV1` + signed CAS `ScrubHeadV1` | AUD3 initial receipt；AUD4 no-catalog/HEAD inference tests；AUD5 continuous receipts |
| typed compatibility | verifier-outdated exists only as `*CompatibilityError` | AUD1 verifier/CLI and AUD4 single HTTP `errors.As` mapping tests |

Every row is a mandatory acceptance criterion, not optional follow-up work.

## Final program audit after AUD5

Before anyone calls the audit archive program complete, assemble a requirement-to-
evidence matrix covering:

| Requirement | Required evidence |
|---|---|
| copy-only/no-delete | source search + DB row-count invariance + no-delete rules/tests |
| mixed canonical versions | v1/v2 golden + archive/restore round trip |
| frozen/strict wire | independent Event/ChainRootRef/Manifest/Checkpoint/RecoveryIndex literal byte/SHA goldens + missing/zero/duplicate/unknown/null/order/time/partial-line rejection |
| segment continuity | missing/overlap/reorder/broken-link red-green tests |
| object immutability | PutIfAbsent collision + complete ObjectVersionV1 exact readback + ambiguous-success recovery/provider qualification evidence |
| signature trust | embedded-key rejection + wrong purpose/protocol/fingerprint/expiry/revocation tests + independent keyring |
| DB-loss discovery | signed RecoveryIndex/checkpoint discovers terminal manifest and walks exact PreviousManifestRefV1 to genesis without DB/List/latest |
| terminal commit/retry | durable intent/put receipts + ambiguous Put/CAS crash matrix + same envelope bytes/IDs/times/VersionIDs/result digest |
| committed-only catalog | index never behind catalog + fault injection/reconciliation after every boundary |
| evidence semantics | chain_integrity separate from ActionRun capture_completeness + v1 caveat |
| environment/PII isolation | projection key allowlist + cross-environment denial tests |
| Query transparency | hot/cold cursor parity + ScrubReceipt-backed coverage + one typed HTTP error mapping/no partial items |
| PLO not Action | distinct signed approval/result artifacts + bound Kill Switch + route/action registry search + runbook evidence |
| R2-10/DB roles | merged refs + Reader/RootWriter/CatalogWriter/AccessRecorder positive/negative grants evidence |
| restore/RPO/RTO | RecoveryIndex-source catalog rebuild + same-tx verify-before-commit + continuous scrub/lag alert drill |
| migration governance | fresh number + exact base/diff digest approval STOP + no release down-migration |
| no physical slimming | explicit absence of deletion/detach/source-reclaim behavior |

Any missing, stale, indirect or skipped evidence means the program remains incomplete.
AUD5 后的下一项架构问题不是“can we delete hot rows now”，而是产品负责人是否愿意
另立 ADR/CR，重新解释宪法中的“审计永不删”。默认答案仍是 No。
