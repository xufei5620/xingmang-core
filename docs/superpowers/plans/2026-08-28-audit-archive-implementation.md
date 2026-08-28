# Audit Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不删除或改写任何审计事件的前提下，交付可签名验证、可透明查询、可隔离恢复的审计冷归档能力。

**Architecture:** PostgreSQL 继续保存全量 hot 事件；版本化 PLO 按全局 sequence 生成不可变 full payload、environment projection 与 signed manifest，全部对象回读验真后才写 append-only catalog。Query 优先读 hot，只有所需范围不在 hot 时才读已验证 projection；恢复从 trusted keyring、manifest 链与 Chain Root 开始，在空的隔离数据库重建并全链验证。

**Tech Stack:** Go 1.27、PostgreSQL 18、pgx/sqlc、Ed25519/SHA-256、S3-compatible WORM object storage（供应商与精确 SDK 版本须先审批）、React/TypeScript/Vitest、River（仅在 R2-10 与 DB 角色拆分后启用调度）。

**Spec:** `docs/superpowers/specs/2026-08-28-audit-archive-design.md`

## Global Constraints

- 本计划是未来实施顺序，不是 XM-C-AUD0 对实现的授权；必须先由人类审批 spec，再逐片批准 AUD1～AUD5。
- 每片使用独立 worktree、分支、commit 和 PR；base 为实施时最新的 `origin/release/v0.1-launch`，Codex 不自行合并。
- AUD1～AUD5 禁止对 `audit.audit_event` 执行 `DELETE`、`UPDATE`、`TRUNCATE`、`DROP`，也禁止增加审计保留天数。
- ObjectStore 接口禁止 Delete、Overwrite、任意 List 与浏览器预签名 URL；对象键只能由归档库按 spec 生成。
- 权威段按全局 sequence 连续分段；不得按 environment 拆证据链；v1/v2 `canonical_version` 必须逐行原样保存并分别验证。
- 归档、验证、恢复都是 Platform Lifecycle Operation；不增加普通 Action、不增加 UI 触发器。
- 生产对象存储必须是 PostgreSQL 之外的异故障域，并启用 versioning、WORM/Object Lock、SSE-KMS；开发同机 fixture 不算生产证据。
- 首期 human Query 继续使用 `audit.read`；full payload 只供 archive/verifier/restore 机器身份。
- Catalog 只有 committed 记录；对象全部 PutIfAbsent、Head、Get、hash/signature/root 验证成功后才允许 INSERT。
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
3. 独立 Chain Root/Manifest trusted keyring；
4. AUD1 开工。

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
- Create: `internal/platform/audit/archive/export.go`
- Create: `internal/platform/audit/archive/export_test.go`
- Create: `internal/platform/audit/archive/verify.go`
- Create: `internal/platform/audit/archive/verify_test.go`
- Create: `internal/platform/audit/archive/keyring.go`
- Create: `internal/platform/audit/archive/keyring_test.go`
- Create: `internal/platform/audit/archive/testdata/mixed-v1-v2.ndjson`
- Create: `internal/platform/audit/archive/testdata/mixed-v1-v2.manifest.json`
- Create: `internal/platform/audit/archive/testdata/root-keyring.json`
- Create: `internal/platform/audit/archive/testdata/manifest-keyring.json`
- Create: `cmd/audit-archive/main.go`
- Create: `cmd/audit-archive/main_test.go`
- Create: `docs/modules/audit/ARCHIVE-FORMAT-v1.md`
- Modify: `docs/modules/audit/README.md`
- Modify: `docs/modules/audit/RUNBOOK.md`

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

type ObjectDescriptor struct {
    Key         string
    SHA256      string
    SizeBytes   int64
    ContentType string
    RowCount    int64
}

type ProjectionDescriptor struct {
    Environment string
    ObjectDescriptor
}

type UnsignedManifest struct {
    Kind                   string
    FormatVersion          int
    ExporterVersion        string
    ExporterCommit         string
    Boundary               SegmentBoundary
    PrevManifestSHA256     string
    CanonicalVersionCounts map[int16]int64
    OversizedRecordCount   int64
    Payload                ObjectDescriptor
    Projections            []ProjectionDescriptor
    ChainRoot              audit.ChainRoot
    EncryptionMode         string
    KMSKeyID               string
    CreatedAt              time.Time
    SourceTipObservedAt    time.Time
}

type SignedManifest struct {
    Unsigned          UnsignedManifest
    ManifestSHA256    string
    SignatureAlgorithm string
    SignatureKeyID   string
    Signature        string
}

type TrustedKey struct {
    KeyID       string
    Algorithm   string
    PublicKey   string
    Fingerprint string
    ValidFrom   time.Time
    RevokedAt   *time.Time
    RevokeReason string
}

type Keyring interface {
    Lookup(keyID string, signedAt time.Time) (TrustedKey, error)
}

type AuditSource interface {
    Tip(context.Context) (int64, string, error)
    List(context.Context, int64, int64) ([]audit.Event, error)
    VerifyChain(context.Context, int64, int64) (*audit.ChainProblem, error)
    ComputeAndSignRoot(context.Context, audit.Signer) (audit.ChainRoot, error)
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
    VerificationVerifierOutdated   VerificationCode = "verifier_outdated"
)

type VerificationReport struct {
    Code             VerificationCode
    FirstBadSequence int64
    Detail           string
    VerifiedRows     int64
    VerifiedObjects  int64
    VerifiedRootHash string
}
```

- `EncodePayload(io.Writer, []audit.Event) (ObjectDescriptor, error)` writes the
  exact NDJSON v1 bytes.
- `EncodeProjection(io.Writer, environment string, []audit.Event) (ObjectDescriptor, error)`
  writes only the approved HTTP item fields.
- `SignManifest(UnsignedManifest, audit.Signer) (SignedManifest, error)` signs the
  domain-separated manifest hash.
- `VerifySegment(ctx context.Context, manifest SignedManifest, payload io.Reader,
  projections map[string]io.Reader, rootKeyring Keyring, manifestKeyring Keyring,
  previous *SignedManifest) VerificationReport` returns one structured report;
  it never rewrites input.

- [ ] **Step 1: Freeze mixed v1/v2 golden data**

Create a two-event fixture where sequence 1 uses `CanonicalV1`, sequence 2 uses
`CanonicalV2`, and both hashes are precomputed with existing `audit.Event.ComputeHash`.
The fixture contains no credential-like values or personal data.

- [ ] **Step 2: Write deterministic format failures first**

Add tests:

```go
func TestEncodePayloadIsByteDeterministic(t *testing.T)
func TestPayloadPreservesAllPersistedFieldsAndCanonicalVersion(t *testing.T)
func TestProjectionContainsOnlyHTTPAllowlistedFields(t *testing.T)
func TestProjectionSeparatesEnvironments(t *testing.T)
func TestSegmentCutsAtRowOrByteBoundaryWithoutTruncatingOneRow(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestEncodePayload|TestPayload|TestProjection|TestSegment' -count=1 -v
```

Expected: FAIL because the package/functions do not exist.

- [ ] **Step 3: Implement fixed structs and NDJSON encoding**

Use structs rather than `map[string]any` for top-level payload/manifest ordering.
Reuse the existing microsecond UTC and summary-normalization semantics; do not
duplicate or modify canonical v1/v2.

- [ ] **Step 4: Prove format green and byte-stable**

Run the Step 2 command twice and compare the fixture/output SHA-256 in the test.
Expected: PASS both times with the same digest.

- [ ] **Step 5: Write verifier red tests**

Add exact mutations and expected codes:

```go
func TestVerifyMixedCanonicalVersions(t *testing.T)
func TestVerifyRejectsUnknownCanonicalVersionAsVerifierOutdated(t *testing.T)
func TestVerifyDetectsChangedPayloadByte(t *testing.T)
func TestVerifyDetectsSequenceGapAndBrokenLink(t *testing.T)
func TestVerifyDetectsMissingOverlappingAndReorderedManifest(t *testing.T)
func TestVerifyRejectsWrongManifestKeyAndWrongRootKey(t *testing.T)
func TestVerifyRejectsRevokedKeyForNewSignature(t *testing.T)
func TestVerifyRejectsProjectionHiddenFields(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run TestVerify -count=1 -v
```

Expected: FAIL until structured verification is implemented.

- [ ] **Step 6: Implement manifest signing and full verification order**

Implement the spec §5.3 order exactly. The signature payload is:

```text
xm-audit-archive-manifest-v1
sha256=<64hex>
```

Unknown canonical versions return `verifier_outdated`; they never fall through
to `event_hash_mismatch`.

- [ ] **Step 7: Add a local-only CLI surface**

`cmd/audit-archive` supports only:

```text
audit-archive plan --from <seq> --to <seq>
audit-archive export-local --from <seq> --to <seq> --root-id <uuid>
audit-archive verify-local --manifest <approved-local-path>
```

`plan` is read-only and prints range/rows/estimated bytes. `export-local` refuses
production environment and writes only below the configured local archive root.
No command accepts SQL, object key, bucket, arbitrary executable or Delete flag.

- [ ] **Step 8: Test CLI exit codes and no-secret output**

```go
func TestCLIPlanDoesNotWriteFiles(t *testing.T)
func TestCLIExportLocalRejectsProduction(t *testing.T)
func TestCLIVerifyMapsTamperToExitOneAndConfigToExitTwo(t *testing.T)
func TestCLIOutputNeverContainsKeyMaterial(t *testing.T)
```

Run:

```powershell
go test ./cmd/audit-archive ./internal/platform/audit/archive -count=1 -v
```

Expected: PASS.

- [ ] **Step 9: Document v1 as frozen**

`ARCHIVE-FORMAT-v1.md` copies the exact payload/projection/manifest fields,
signing payload, limits and verification codes from the approved spec. State
that any later compression/field change requires format v2 and compatibility tests.

- [ ] **Step 10: Run full AUD1 verification**

Run all global gates. Also run:

```powershell
git diff --check
rg -n "Delete|Overwrite|TRUNCATE|DROP TABLE audit\.audit_event|DELETE FROM audit\.audit_event" internal/platform/audit/archive cmd/audit-archive docs/modules/audit
```

Expected: full gates exit 0; the search finds only explanatory prohibition text,
never an executable deletion capability.

- [ ] **Step 11: Commit AUD1**

```powershell
git add internal/platform/audit/archive cmd/audit-archive docs/modules/audit
git commit -m "feat(audit): add deterministic archive format and verifier"
```

Stop after PR creation and human review; do not start AUD2 from an unmerged AUD1 branch.

---

## Task 2（AUD2）: Immutable ObjectStore 与 Committed Catalog

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

**Interfaces:**

```go
type ObjectMeta struct {
    Key       string
    SHA256    string
    SizeBytes int64
    ETag      string
}

type ObjectStore interface {
    PutIfAbsent(context.Context, string, io.Reader, int64, string) (ObjectMeta, error)
    Head(context.Context, string) (ObjectMeta, error)
    Get(context.Context, string) (io.ReadCloser, ObjectMeta, error)
}

type CommittedSegment struct {
    ID uuid.UUID
    Manifest SignedManifest
    ManifestObject ObjectDescriptor
    CommittedAt time.Time
    VerifiedAt time.Time
}

type Catalog interface {
    Latest(context.Context) (CommittedSegment, error)
    Commit(context.Context, CommittedSegment) error
    ListBefore(context.Context, int64, int32) ([]CommittedSegment, error)
}
```

There is intentionally no staged catalog type and no Delete method.

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

- [ ] **Step 2: Write migration contract tests first**

Create integration assertions for:

```text
archive_segment accepts one valid committed row
UPDATE changes zero rows
DELETE changes zero rows
duplicate from/to/payload hash/manifest hash is rejected
invalid range/count/hash is rejected
catalog cannot skip latest.to+1 through Catalog.Commit
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestCatalog' -count=1 -v
```

Expected: FAIL because schema/catalog do not exist.

- [ ] **Step 3: Add append-only catalog migration and sqlc queries**

The table contains only committed rows and both no-update/no-delete rules. Add
queries to the existing `db/queries/audit.sql`, then run:

```powershell
go tool sqlc generate
git diff --exit-code -- sqlc.yaml
```

Expected: sqlc generation succeeds; `sqlc.yaml` does not change merely to add
queries to the existing audit query file.

- [ ] **Step 4: Implement transactional contiguous Commit**

`Catalog.Commit` takes a dedicated PostgreSQL advisory transaction lock, reads
the latest committed range, requires `new.from=latest.to+1`, then inserts once.
It never writes audit events or changes existing catalog rows.

- [ ] **Step 5: Write ObjectStore immutability red tests**

```go
func TestObjectStoreInterfaceHasNoDeleteOrOverwrite(t *testing.T)
func TestPutIfAbsentIsIdempotentForSameContent(t *testing.T)
func TestPutIfAbsentRejectsSameKeyDifferentContent(t *testing.T)
func TestObjectKeyRejectsTraversalAndUnknownEnvironment(t *testing.T)
func TestHeadAndGetReturnRecordedHashAndSize(t *testing.T)
func TestFilesystemStoreNeverWritesOutsideRoot(t *testing.T)
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
server-side KMS encryption and expected Object Lock headers. Redirects, public
ACL and endpoint hosts outside the configured allowlist fail closed.

- [ ] **Step 7: Test the provider adapter without production credentials**

Use an `httptest` protocol fixture for request signing/headers and a disposable
WORM-capable integration bucket only when the approved CI secret is present.
No test logs headers or secret values.

```powershell
go test ./internal/platform/audit/archive -run 'TestS3' -count=1 -v
```

Expected: protocol tests PASS; live provider test reports explicit SKIP when its
dedicated test CredentialRef is absent.

- [ ] **Step 8: Prove commit happens only after readback verification**

Use a fake ObjectStore and Catalog to cover every failure point:

```go
func TestCommitProtocolDoesNotCatalogPartialUpload(t *testing.T)
func TestCommitProtocolDoesNotCatalogFailedHeadOrGet(t *testing.T)
func TestCommitProtocolDoesNotCatalogHashOrSignatureFailure(t *testing.T)
func TestCommitProtocolCatalogsExactlyOnceAfterAllObjectsVerify(t *testing.T)
func TestConcurrentCommitOnlyAcceptsOneContiguousSegment(t *testing.T)
func TestRetryReusesContentAddressedOrphanObjects(t *testing.T)
```

- [ ] **Step 9: Run AUD2 full gates and inspect migration safety**

In addition to global gates:

```powershell
git diff --check
rg -n "func .*Delete|func .*Overwrite|DELETE FROM audit\.audit_event|TRUNCATE audit\.audit_event|DROP TABLE audit\.audit_event" internal/platform/audit/archive db/migrations db/queries
```

Expected: no executable destructive path; only catalog no-delete rule and
negative test/prohibition text may match.

- [ ] **Step 10: Commit AUD2**

Stage the dynamically numbered migration explicitly rather than using a broad
glob, then commit:

```powershell
git add db/migrations db/queries/audit.sql internal/platform/audit/gen internal/platform/audit/archive go.mod go.sum VERSIONS.lock deploy/compose/.env.example docs/modules/audit
git commit -m "feat(audit): add immutable archive store and catalog"
```

Stop for migration/object-storage review and human merge.

---

## Task 3（AUD3）: Chain Root、Manifest 与人工 Lifecycle Operation

**Approval gate:** AUD2 merged；Manifest/Chain Root public key distribution、
CredentialRef、archive/restore machine identities approved；DB role split merged。
R2-10 absent is allowed only because AUD3 remains manual—no River registration.

**Files:**

- Create: `internal/platform/audit/archive/service.go`
- Create: `internal/platform/audit/archive/service_test.go`
- Create: `internal/platform/audit/archive/service_integration_test.go`
- Create: `internal/platform/audit/archive/commit.go`
- Create: `internal/platform/audit/archive/commit_test.go`
- Modify: `internal/platform/audit/anchor.go`
- Modify: `internal/platform/audit/anchor_test.go`
- Modify: `cmd/audit-archive/main.go`
- Modify: `cmd/audit-archive/main_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Create: `docs/runbooks/AUDIT-ARCHIVE.md`
- Create: `docs/evidence/TEMPLATE-audit-archive-run.md`

**Interfaces:**

```go
type ArchiveRequest struct {
    ApprovalID        string
    FromSequence      int64
    ExpectedToSequence int64
    ControlEnvironment string // 执行目标，不是事件过滤器
    DryRun            bool
}

type ArchivePlan struct {
    FromSequence       int64
    ToSequence         int64
    EstimatedRows      int64
    EstimatedBytes     int64
    ChainRootRequired  bool
}

type ArchiveResult struct {
    ChainRoot audit.ChainRoot
    Segments  []CommittedSegment
    StartedAt time.Time
    FinishedAt time.Time
}

type RootExportMarker interface {
    MarkRootExported(context.Context, uuid.UUID, string) (audit.ChainRoot, error)
}

type Service struct {
    Source        AuditSource
    RootExports   RootExportMarker
    Catalog       Catalog
    Objects       ObjectStore
    RootSigner    audit.Signer
    ManifestSigner audit.Signer
    RootKeyring   Keyring
    ManifestKeyring Keyring
}

func (s *Service) Plan(context.Context, ArchiveRequest) (ArchivePlan, error)
func (s *Service) Archive(context.Context, ArchiveRequest) (ArchiveResult, error)
```

- [ ] **Step 1: Write PLO precondition failures**

```go
func TestArchiveRequiresApprovalIDAndExactExpectedTip(t *testing.T)
func TestArchiveStartsAtLatestCommittedPlusOne(t *testing.T)
func TestArchiveRefusesBrokenDatabaseChain(t *testing.T)
func TestArchiveRefusesMissingOrUntrustedRootAndManifestKeys(t *testing.T)
func TestArchiveDryRunWritesNothing(t *testing.T)
func TestArchiveDoesNotExposeSecretsInErrorsOrLogs(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit/archive -run 'TestArchive' -count=1 -v
```

Expected: FAIL until orchestration exists.

- [ ] **Step 2: Implement Plan as a pure read**

Plan reads current tip/latest committed segment, verifies requested range and
estimates rows/bytes. It does not sign, upload or write catalog.

- [ ] **Step 3: Implement checkpoint selection and crash-safe root reuse**

Read the latest committed catalog and latest Chain Root first. If root.to is
greater than catalog.to, fully re-verify the database chain and root signature,
then reuse that pending root—the previous run may have failed after root INSERT
but before object/catalog commit. If no pending root exists and tip advanced,
call existing `ComputeAndSignRoot`; export only through `root.ToSequence`, and
require the last segment to end exactly there. Only when root and catalog both
cover the current tip may the operation return a typed no-op.

- [ ] **Step 4: Implement upload-readback-verify-commit**

For each segment: build payload/projections, sign manifest, PutIfAbsent all
objects, Head+Get them, call `VerifySegment`, and only then `Catalog.Commit`.
A later segment cannot commit if an earlier segment in the batch failed.
After the manifest/catalog commit succeeds, mark the existing Chain Root
`export_target` as the content-addressed manifest object key. A failed mark is
retryable and must never cause root re-signing or object replacement.

- [ ] **Step 5: Test crashes at every boundary**

```go
func TestArchiveRetryAfterPayloadUploadReusesObject(t *testing.T)
func TestArchiveRetryAfterProjectionUploadReusesObjects(t *testing.T)
func TestArchiveRetryAfterManifestUploadCommitsOnce(t *testing.T)
func TestArchiveReusesPendingRootAfterCrashBeforeCatalogCommit(t *testing.T)
func TestArchiveMarksRootExportedOnlyAfterCatalogCommit(t *testing.T)
func TestArchiveRetriesRootExportMarkWithoutResigning(t *testing.T)
func TestArchiveFailureBeforeCatalogLeavesNoCommittedRange(t *testing.T)
func TestArchiveFailureAfterOneSegmentCannotSkipToThirdSegment(t *testing.T)
func TestTwoManualRunsCannotForkManifestChain(t *testing.T)
```

- [ ] **Step 6: Extend CLI without adding arbitrary capabilities**

Approved command surface:

```text
audit-archive plan --approval-id <id> --expected-tip <seq>
audit-archive run --approval-id <id> --expected-tip <seq>
audit-archive verify --from-manifest <content-addressed-key>
```

Bucket/prefix/credential refs come from approved service configuration, not CLI
flags. `run` prints build commit, interval, row/object counts, manifest hashes,
root ID and verification result; never prints DSN password, secret or full PII.

- [ ] **Step 7: Package the versioned lifecycle binary**

Add an explicit Docker target containing `audit-archive`; do not add a daemon,
compose service, worker registration, cron, restart policy or production default.

- [ ] **Step 8: Run a non-production object round trip**

Against the approved disposable test bucket and test database:

```text
plan -> root sign -> payload/projection/manifest PutIfAbsent -> Get -> verify -> catalog commit
```

Record object hashes and exit codes without recording payload contents or keys.

- [ ] **Step 9: Run AUD3 full gates and commit**

```powershell
git diff --check
git add internal/platform/audit/archive internal/platform/audit/anchor.go internal/platform/audit/anchor_test.go cmd/audit-archive deploy/docker/go.Dockerfile docs/modules/audit docs/runbooks/AUDIT-ARCHIVE.md docs/evidence/TEMPLATE-audit-archive-run.md
git commit -m "feat(audit): add approved archive lifecycle operation"
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
    ArchiveVerifiedAt         *time.Time
    ArchiveLagSeconds         *int64
    SourceTiers               []SourceTier
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

New typed errors map to:

```text
AUDIT_ARCHIVE_UNAVAILABLE             -> 503
AUDIT_ARCHIVE_QUERY_BUDGET_EXCEEDED   -> 503
AUDIT_ARCHIVE_VERIFIER_OUTDATED       -> 503
AUDIT_ARCHIVE_INTEGRITY_FAILED        -> 500 + integrity incident log
```

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
func TestQueryEnforcesObjectByteAndCountBudget(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/audit -run 'TestQuery' -count=1 -v
```

Expected: FAIL before Query/cold reader exist.

- [ ] **Step 2: Implement cold projection verification and streaming**

Cold reader selects committed catalog ranges, checks manifest/keyring/hash before
decoding projection, skips environments with zero rows, and enforces 32 objects,
64 MiB and request context deadline. It never opens payload objects.

- [ ] **Step 3: Implement deterministic hot-first merge**

Read hot first. If fewer than limit and older committed ranges are required,
continue from the exact oldest hot sequence into cold. Deduplicate by sequence,
sort descending, enforce environment, compute one `NextBefore`, and produce
coverage from verified facts rather than configuration intent.

- [ ] **Step 4: Change HTTP tests before handler implementation**

Assert:

```json
"coverage": {
  "complete": true,
  "oldest_available_sequence": 1,
  "archive_checkpoint_sequence": 108,
  "archive_verified_at": "2026-08-28T10:00:00Z",
  "archive_lag_seconds": 1200,
  "source_tiers": ["hot", "cold"]
}
```

Also assert the existing item keys remain byte-for-byte unchanged, caller-supplied
environment remains ignored, limit stays capped at 100, and a cold error response
contains no items/payload/object key.

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
    ApprovalID       string
    TargetEnvironment string
    ExpectedRootHash string
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
    Catalog        Catalog
    Objects        ObjectStore
    RootKeyring    Keyring
    ManifestKeyring Keyring
    Target         *pgxpool.Pool
}

func (r *Restorer) Restore(context.Context, RestoreRequest) (RestoreReport, error)
```

Restore target must have completed the approved platform migrations/bootstrap (so
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
func TestRestoreNeverUpdatesDeletesOrRehashesArchivedEvents(t *testing.T)
func TestRestoreStopsBeforeInsertOnAnyManifestObjectOrRootFailure(t *testing.T)
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
`Store.Append`, because Append would allocate new sequence/timestamps/hashes. It
inserts only into an empty isolated target after all objects are verified, then runs
database `VerifyChain(1, root.ToSequence)` and checks the final tip/root hash.

- [ ] **Step 4: Write full mixed-version round-trip test**

```go
func TestArchiveRestoreRoundTripPreservesEveryPersistedField(t *testing.T)
func TestRestoredMixedV1V2ChainMatchesTrustedRoot(t *testing.T)
func TestRestoredQueryMatchesPerEnvironmentCursorPages(t *testing.T)
func TestRestoreFailureRollsBackAllInsertedRows(t *testing.T)
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
audit-restore plan --approval-id <id> --expected-root <64hex>
audit-restore run --approval-id <id> --expected-root <64hex>
```

Source bucket and target DSN come through approved config/CredentialRef; no arbitrary
SQL, table, object key, source URL or overwrite flag exists.

- [ ] **Step 6: Run and record an isolated staging drill**

The runbook sequence is fixed:

```text
trusted keyring -> manifest chain -> object hashes -> event/cross-segment chain
-> empty isolated DB import -> DB audit-verify -> root match -> Query parity
```

Measure source checkpoint time to obtain actual RPO and drill start-to-verified time
for actual RTO. Store only hashes/counts/timestamps/exit codes in evidence; no payload,
PII, DSN or key material.

- [ ] **Step 7: Add scheduler only if both hard gates are proven**

If R2-10 and DB role split are merged, first add failing tests:

```go
func TestAuditArchiveDisabledByDefaultUntilConfigured(t *testing.T)
func TestAuditArchiveRejectsNonPositiveIntervalAndRPO(t *testing.T)
func TestAuditArchiveUsesClusterLeaseAndStableIdempotencyKey(t *testing.T)
func TestAuditArchiveDuplicateProbeReportsConcurrentOwner(t *testing.T)
func TestAuditArchiveKillSwitchPreventsRegistration(t *testing.T)
func TestAuditArchiveJobNeverDeletesHotEvents(t *testing.T)
```

Then register the hourly/10,000-event policy. If either gate is absent, omit every
scheduler file/config change and record manual-only status; do not ship dormant code
that looks production-ready.

- [ ] **Step 8: Verify RPO/RTO claims from evidence**

Only set runbook/status to “target met” when at least one fresh isolated drill shows:

```text
archive checkpoint lag <= 3600 seconds
restore start-to-full-verification <= 14400 seconds
full chain/root/query parity all PASS
```

Otherwise report the measured values and keep target status unmet.

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

## Final program audit after AUD5

Before anyone calls the audit archive program complete, assemble a requirement-to-
evidence matrix covering:

| Requirement | Required evidence |
|---|---|
| copy-only/no-delete | source search + DB row-count invariance + no-delete rules/tests |
| mixed canonical versions | v1/v2 golden + archive/restore round trip |
| segment continuity | missing/overlap/reorder/broken-link red-green tests |
| object immutability | PutIfAbsent collision tests + provider WORM/versioning evidence |
| manifest/root trust | wrong/revoked key tests + independently supplied keyring |
| committed-only catalog | fault injection at every upload/readback/verify boundary |
| environment/PII isolation | projection key allowlist + cross-environment denial tests |
| Query transparency | hot/cold cursor parity + explicit unavailable/budget behavior |
| PLO not Action | route/action registry search + CLI/runbook approval evidence |
| R2-10/DB roles | merged refs + duplicate-owner test + grants evidence |
| restore/RPO/RTO | isolated drill report with hashes/counts/timestamps/exit codes |
| no physical slimming | explicit absence of deletion/detach/source-reclaim behavior |

Any missing, stale, indirect or skipped evidence means the program remains incomplete.
AUD5 后的下一项架构问题不是“can we delete hot rows now”，而是产品负责人是否愿意
另立 ADR/CR，重新解释宪法中的“审计永不删”。默认答案仍是 No。
