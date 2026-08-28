# 审计归档设计（XM-C-AUD0）

> 状态：**待产品负责人、平台负责人、安全/合规负责人共同审批**
>
> 日期：2026-08-28
>
> 性质：路线图 C「审计归档方案」的 Plan/Design 制品
>
> 实施授权：**无**。本文件与配套实施计划获批前，不得创建迁移、对象存储、
> 新 scope、Action、周期任务或生产配置；获批后也必须按 AUD1～AUD5 分片逐片交付。

## 1. 决策摘要

推荐采用以下模型：

1. PostgreSQL 中的 `audit.audit_event` 继续保存全部事件，任何 AUD1～AUD5
   切片都不得 `DELETE`、`UPDATE`、`TRUNCATE` 或 `DROP` 审计历史；
2. 按**全局连续 sequence** 生成不可变的权威 payload 段，同时生成按 environment
   隔离、仅含现有 Query 字段的 projection；
3. 每段由冻结的 `ManifestV1` 绑定 payload/projection 的**精确对象 VersionID**、
   哈希、KMS/Object Lock 状态、`PreviousManifestRefV1`、冻结的 `ChainRootRefV1`
   与 `canonical_version` 分布；`ChainRootRefV1` 是沿用既有 root signature 的冻结
   signed leaf，`ManifestV1`、`CheckpointV1`、`RecoveryIndexV1` 才使用统一的
   `unsigned/unsigned_sha256/signature_*` envelope；四类各有独立 strict wire 与
   literal-byte golden，均不嵌入可演进运行时 struct；
4. 现有 Chain Root JSON 中内嵌的 `public_key` 不再是信任来源。Chain Root、
   manifest、ArchiveCheckpoint、RecoveryIndex 与 PLO approval envelope 分别使用
   purpose/protocol 隔离的 trusted key；任何对象自带公钥都必须忽略并拒绝自认证；
5. 每次成功归档产生不可变、独立签名的 `ArchiveCheckpoint`，再通过独立故障域中
   固定 locator 的签名 `RecoveryIndex` 做 CAS 推进。即使 PostgreSQL 与 catalog
   全丢，也能发现 terminal manifest 的 key + VersionID，验证整链并重建 catalog；
6. 对象写入只支持 `PutIfAbsent`，接口层不提供 Delete/Overwrite；生产对象存储
   必须启用 versioning、WORM/Object Lock、KMS 加密，且位于 PostgreSQL 之外的
   独立故障域；
7. `GET /api/v1/audit/events` 未来可通过统一 Query 在 hot/cold 间透明翻页，
   仍只允许调用者自己的 environment、仍使用 `audit.read`，并显式返回覆盖率、
   来源层与验证时刻；
8. `chain_integrity` 与 `capture_completeness` 是两个结论：前者验证已有事件链，
   后者将 terminal ActionRun 与 audit event 对账；哈希链通过不能掩盖审计漏写；
9. `archive_verified_at` 只来自 append-only signed `ScrubReceiptV1` 及其 signed CAS
   current pointer，绝不从 catalog 时间、HEAD 时间或配置推断；
10. 归档、验证、恢复是受信 Platform Lifecycle Operation，不是普通 Action；
11. 默认只做 copy-only。**copy-only 不会释放 PostgreSQL 容量**。任何物理瘦身、
   分区摘除、表空间迁移后删除源副本等动作均在本设计范围外，必须另立 ADR/CR
   并先裁定它是否违反「审计永不删」。

结论是：可以先建设可验证、可恢复的冷副本与 Query 抽象；当前没有授权把冷副本
当成删除热库历史的许可证。

## 2. 权威约束与现状

### 2.1 不可推翻的约束

- `PROJECT-CONSTITUTION.md` 第 11 条：审计 append-only，并在数据库外锚定
  签名摘要；第 22 条：备份必须通过恢复演练。
- ADR-003：数据库迁移、备份恢复等走版本化脚本、变更单、人工批准、制品校验
  与独立审计，不走普通 Action API。
- `internal/platform/jobs/retention.go` 已把审计口径钉为“一条都不删”并在每轮
  日志写入 `audit_events=never_pruned`。
- 现有审计链是跨 environment 的**一条全局链**。按 environment 过滤后的序号
  缺口不是链断裂，完整验证必须读取全局连续事件。
- `canonical_version=1` 是冻结的历史编码，只能用于历史校验；新事件使用
  长度前缀的 v2。归档与恢复必须逐行保留版本，不能重算或升级历史行。
- 凭据只经 CredentialRef；任何对象存储、KMS、签名密钥都不得以明文进入仓库、
  日志、前端或 AI 上下文。

### 2.2 2026-08-28 只读盘点

| 能力 | 当前事实 | 设计后果 |
|---|---|---|
| 审计表 | 普通非分区表；no-update/no-delete 规则存在 | 不能把常规 retention 改造成删除任务 |
| 事件链 | 2026-08-28 盘点时 staging 为 sequence 1～108；103 条 v1、5 条 v2 | 这是带时点的 planning snapshot，不是当前事实；开工证据必须刷新，测试夹具覆盖混合版本 |
| Chain Root | 库表与签名/导出代码存在；同次 snapshot 的 root 数为 0 | 现有导出把 `public_key` 放在根 JSON 里，属于 embedded-key 自认证，必须先退休 |
| 验证工具 | `cmd/audit-verify` 只验证 PostgreSQL 区间 | 必须新增对象、manifest、跨段与恢复验证 |
| Query | `/api/v1/audit/events` 按 environment + sequence 游标读取热表 | 冷读不能改变权限、游标或字段裁剪 |
| 对象存储 | compose、依赖与运行栈均不存在 | 生产 adapter 与供应商/地域必须先审批 |
| 备份恢复 | 有审计恢复 runbook，没有平台数据库真实备份服务 | 归档不能冒充完整数据库备份 |
| 数据库权限 | 当前应用账号仍是超级用户和表 owner | 生产自动归档必须依赖 DB 角色拆分完成 |
| 多副本任务 | River 有周期唯一性，但无 R2-10 集群级所有权 | 自动调度必须等待 R2-10；先做人工 PLO |

这张表只记录 AUD0 规划时看到的 snapshot，不能被后续 PR 引用为“staging 当前值”。
AUD1 开工前必须创建 `docs/evidence/EV-<date>-audit-archive-baseline.md`，记录脱敏的
database/environment fingerprint、查询时刻、min/max/count、sequence 缺口、
`canonical_version` 分布、Chain Root 数、terminal ActionRun 对账数与执行命令；
AUD2～AUD5 每片都引用一个不早于该片 base 的 fresh snapshot。缺少 snapshot 或
结果与本表不一致不是代码缺陷，但属于 **STOP：先解释漂移，禁止沿用 108**。

### 2.3 本方案不解决

- 不提供审计删除、保留天数或“到期清理”开关；
- 不把归档当作 PostgreSQL 全库备份，也不备份 KEK、SOPS/age 恢复私钥；
- 不增加浏览器直连对象存储、预签名下载或批量导出能力；
- 不改变现有审计事件 canonical 字段、哈希算法或全局链结构；
- 不修复 DB 角色拆分、R2-10、多副本限流等相邻路线图事项；只把它们声明为
  激活依赖；
- 不建设 UI 触发器，不新增普通 Action；
- 不承诺当前 staging 已满足 RPO/RTO，目标值只有通过持续运行与恢复演练后才成立。

## 3. 总体架构

```text
audit.audit_event（全量、append-only、hot source）
    │
    ├─ 全链校验 ──> Chain Root（既有 Ed25519 签名）
    │
    └─ 版本化归档 PLO
         ├─ global payload segment（全字段、sequence 连续）
         ├─ environment projection（现有 Query 字段）
         ├─ signed manifest（精确 VersionID + 哈希 + 边界 + root + prev manifest）
         ├─ signed ArchiveCheckpoint（terminal manifest + 可重建 catalog 摘要）
         ├─ signed RecoveryIndex（独立故障域 fixed locator，CAS terminal marker）
         └─ committed catalog（可由 checkpoint/index 重建的 PostgreSQL 投影）

GET /api/v1/audit/events
    └─ AuditQuery
         ├─ hot PostgreSQL reader（优先）
         └─ cold projection reader（只在所需范围不在 hot 时使用）
```

权威 payload 与人读 projection 必须分开：前者要能重算链哈希，所以保存所有字段；
后者只允许出现现有 HTTP 响应字段。projection 不是新的证据链，它的可信度来自
manifest 对 projection hash 的签名以及它与同段权威 payload 的 sequence 映射。

PostgreSQL catalog **不是**恢复的 root of trust，也不是 terminal marker。终态只由
trusted keyring 验证通过的 RecoveryIndex 当前 generation 指向的 ArchiveCheckpoint
定义；catalog 是服务 Query 的可重建 materialized projection。Reader、Writer 与
AccessRecorder 使用不同接口、CredentialRef 和最小权限身份，任何一个接口都不能
同时获得任意对象浏览、删除、数据库 owner 与安全日志改写能力。

## 4. 分段与文件格式

### 4.1 分段边界

- payload 段按全局 sequence 升序、闭区间 `[from_sequence, to_sequence]`；
- 第一段 `from_sequence=1`，首行 `prev_hash=GenesisHash`；后续段必须满足：
  `from = previous.to + 1` 且首行 `prev_hash = previous.last_event_hash`；
- 单段在 25,000 行或 64 MiB 未压缩 payload 时切分，以先到者为准；
- 单行超过 64 MiB 时允许形成一行段，并在 manifest 记录
  `oversized_record_count=1`；不得截断 summary；
- segmenter 只能在**完整 NDJSON 行已编码并计量后**决定边界。达到阈值时把完整行
  放入本段或下一段，绝不先写半行；对象末尾缺 LF、EOF 落在 JSON token 中、
  `size_bytes` 与实际字节不同都属于 `truncated_object`，不能当作较短合法段；
- v1 首版不压缩。这样对象哈希、离线查看和 Range/流式读取没有压缩层歧义；
  后续压缩格式属于新的 `format_version`，不得就地改变 v1；
- 同一归档批次可产生多个段，但最后一段的 `to_sequence` 必须等于所引用
  Chain Root 的 `to_sequence`。

AUD1 的 `export-local` 只允许从 sequence 1/Genesis 开始，不接受 `--from`。任何后来
需要从 `from>1` 开始的 writer，都必须由受信 RecoveryIndex/Checkpoint 找到并验签
`PreviousManifestRefV1` exact locator，再验证首行 `prev_hash`；调用方提供 hash、root ID
或本地文件路径本身均不能建立边界信任。

AUD3 continuation 必须从**已验签 RecoveryIndex 指向的 terminal manifest**取得下一段
`previous_manifest`，要求 `new.from_sequence=previous.to_sequence+1` 且新段首行
`prev_hash=previous.last_event_hash`。PostgreSQL catalog 只能用于对账，不能提供这个前驱。
现有 `audit.Store.VerifyChain(from>1,to)` 只验证给定区间内部，不能证明第一行前驱，禁止
拿它替代此检查；实现必须有独立 red/green 测试钉住 exact terminal locator 与首行边界。

### 4.2 权威 payload

媒体类型为 `application/x-ndjson; charset=utf-8`：UTF-8、无 BOM、LF 换行、
sequence 升序、每行一个 JSON object、末尾**恰好**一个 LF。归档 wire type
`ArchiveEventV1` 是独立冻结协议，不直接把可演进的 `audit.Event` 做 JSON
marshal/unmarshal。每行必须且只能包含以下全部字段：

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

确定性编码规则：

- 时间统一为 UTC 固定微秒精度；
- 字段顺序固定为上表顺序；summary 对象键按字节升序；
- 顶层 decoder 拒绝 unknown、duplicate、missing、显式 `null` 与错误 JSON type；
  不能依赖 Go struct 零值判断字段是否出现；
- `canonical_version` 在 archive wire 中必须显式存在且为 `1` 或 `2`。现有
  `audit.Event.Canonical()` 把内存零值 `0` 当 v1 只是历史测试便利，**不适用于归档**；
  missing/zero 必须返回 `archive_format_invalid`，不能降级成 v1；
- 不添加展示字段、推导字段或二次脱敏；任何变换都会让恢复内容偏离原链；
- strict decoder 先得到 `ArchiveEventV1`，完成字段存在性/类型检查后才显式映射为
  `audit.Event`，按该行 `canonical_version` 调用冻结的现有 `ComputeHash`；
- archive `format_version` 与 event `canonical_version` 是两个正交版本；增加任一
  字段、改变时间/JSON/LF 规则或压缩方式都必须发布 archive format v2；
- v1/v2 canonical 实现、wire bytes、manifest bytes、签名载荷与 negative golden
  永不删除。golden 的 SHA-256 写死在测试中，不能由待测 encoder 现场生成 expected。

`CanonicalV1` 已知不是单射编码：两个不同字段集合可能产生同一 canonical bytes。
因此“v1 行 hash/root 验证通过”只能证明归档忠实保存了数据库里的既有链表示，
不能证明 v1 字段语义不存在历史碰撞。报告必须保留 v1 数量并输出
`legacy_canonical_v1_non_injective` caveat；不得把它包装成与 v2 等价的完整性保证。

### 4.3 environment projection

每个 payload 段按出现过的 environment 生成一个 projection。字段必须逐字等同
现有 `/api/v1/audit/events` item：

```text
sequence, occurred_at, principal_id, principal_type,
action_id, action_version, action_run_id,
resource_type, resource_id, environment, request_id,
result, error_code, before_summary, after_summary,
event_hash, prev_hash
```

projection 禁止包含 `reason`、`approval_id`、`trace_id`、`source_ip`、两个
connector summary 或对象/KMS 地址。空环境 projection 不生成对象；manifest
中的 environment row count 让 Query 可以跳过不相关段。

### 4.4 对象键

对象 key 由 sequence 范围与内容哈希组成，不能由调用方提供任意路径；但 key
并不足以标识对象，所有签名引用都使用 `(bucket_id, key, version_id, sha256)`：

```text
audit/v1/payload/seq-<from19>-<to19>-<sha256>.ndjson
audit/v1/projection/<environment>/seq-<from19>-<to19>-<sha256>.ndjson
audit/v1/manifest/seq-<from19>-<to19>-<manifest_sha256>.json
audit/v1/checkpoint/seq-<to19>-<checkpoint_sha256>.json
```

`environment` 必须通过现有 environment ID 校验并编码，不能含 `/`、`..` 或
控制字符。相同内容重试命中相同键；同键内容不同时必须报 collision 事故。
即使同 key 被错误策略写出新版本，也只能读取签名 artifact 钉住的 VersionID，
不得用“latest”。RecoveryIndex 使用独立 discovery store 中预配置的 fixed locator，
不通过 bucket List 猜最新对象。

## 5. Manifest、签名与链连续性

### 5.1 冻结的 signed-artifact wire

`ChainRootRefV1`、`ManifestV1`、`CheckpointV1`、`RecoveryIndexV1` 是四个独立
wire type，不得直接 JSON marshal `audit.ChainRoot`、数据库 row、provider SDK type
或任一可演进运行时 struct。四类 wire 都使用 UTF-8、无 BOM/空白、末尾恰好一个 LF、
下列字段顺序、UTC 固定微秒时间、十进制整数、JSON string 的 RFC 8259 转义；禁止 map，
所有列表固定排序，Decoder 拒绝 unknown、duplicate、missing、显式 null、错误 type、
非规范时间/数字与多余字节。

`ChainRootRefV1` 是**唯一 envelope 例外**：它直接携带既有 `audit-chain-root/v1`
协议的 `signature/key_id`，Verifier 用冻结字段重建现有 root signing payload；不得再包一层
`unsigned/unsigned_sha256`，也不存在 `SignedChainRootRefV1`。其余三类分别有 unsigned
wire，并统一包进 §5.1 末尾唯一的 signed envelope。

`ObjectVersionV1` 的字段顺序固定为：

```text
bucket_id, key, version_id, sha256, size_bytes, content_type,
provider_checksum, etag, encryption_mode, kms_key_id,
object_lock_mode, retain_until, row_count
```

所有字段显式存在；`version_id`、provider checksum、`kms_key_id`、lock mode 与
`retain_until` 必须来自 exact-version readback，`encryption_mode` 固定
`SSE-KMS`。`row_count` 对 payload/projection 为非负；不适用的 manifest/checkpoint
对象也显式写 0，不能靠 missing/null 表示。

`PreviousManifestRefV1` 的字段顺序固定为：

```text
kind, bucket_id, key, version_id, sha256
```

- 第一段必须使用唯一 genesis 值：`kind="genesis"`、其余三个 locator string 为空，
  `sha256=db88be35e323689da04f28e9e651d7b8ca19c6b46f43b6d3da9f485c15e07386`
  （即 `SHA256("xm-audit-archive-manifest-genesis-v1\n")`）；
- 后续段必须使用 `kind="object"`，并填入上一 signed manifest 的非空
  `bucket_id + key + version_id + sha256`；hash-only、latest 或调用方临时拼接 locator
  均非法。

`ChainRootRefV1` 的字段顺序固定为：

```text
id, computed_at, from_sequence, to_sequence, root_hash,
signature, key_id
```

它刻意不含 `public_key`、`signed_payload`、`exported_at`、`export_target`，也不嵌入
`audit.ChainRoot`。Verifier 按这些冻结字段重建既有 root signing payload。

`ManifestV1` unsigned 字段按以下顺序且只能出现一次：

```text
kind="xingmang-audit-archive-manifest", format_version=1,
exporter_version, exporter_commit,
from_sequence, to_sequence, row_count,
first_prev_hash, first_event_hash, last_event_hash,
previous_manifest,
canonical_version_counts, oversized_record_count,
payload, projections, chain_root,
created_at, source_tip_observed_at, action_run_snapshot_at,
eligible_action_run_count, matched_action_run_count,
missing_audit_event_count, duplicate_audit_event_count
```

`canonical_version_counts` 是按 version 升序、恰含 `{version:1,row_count}` 与
`{version:2,row_count}` 的数组，不是 map；`projections` 按 environment UTF-8 bytes
升序，每项顺序固定为 `environment, object`。`payload`/projection 的 object 都是完整
`ObjectVersionV1`。Manifest 不声称 Chain Root 的 `from_sequence` 是整条链起点；
必须从 genesis 或验签 previous exact locator 连续重算。

`ManifestV1`、`CheckpointV1`、`RecoveryIndexV1` 的 signed envelope 字段顺序唯一固定为：

```text
unsigned, unsigned_sha256, signature_algorithm="Ed25519",
signature_key_id, signature
```

`ChainRootRefV1` 的既有签名 leaf，以及其余三类 unsigned/signed envelope，必须各有
checked-in literal bytes + 固定 SHA-256、strict negative golden 和跨 domain replay
golden；expected bytes 不得由待测 encoder 生成。

### 5.2 Manifest 签名

Manifest 使用与 Chain Root **不同的签名 key**，避免协议复用：

1. unsigned manifest 按固定字段顺序、无多余空白编码；
2. `unsigned_sha256 = SHA256(unsigned_manifest_bytes)`；
3. 签名载荷为：
   `xm-audit-archive-manifest-v1\nsha256=<unsigned_sha256>\n`；
4. 外层 envelope 只保存 `unsigned`、`unsigned_sha256`、
   `signature_algorithm=Ed25519`、`signature_key_id`、`signature`；
5. verifier 从独立 trusted keyring 按 key ID + purpose + protocol 取公钥。

现有 `ExportRoot` 产物中的 `public_key` 形成“对象说用这把钥签、对象自己又提供
这把钥”的自认证闭环，不能建立真实性。AUD1 必须修改既有 `anchor.go`、测试、
`db/queries/audit.sql`、生成代码、README、DATA-MODEL 与 RUNBOOK：新导出根不再写
`public_key`，验证 API 不接受 artifact-supplied key；历史 JSON 即使含公钥也只把
它当不受信 hint，并要求外部 trusted keyring 中存在相同 key ID/fingerprint，否则
fail closed。

AUD1 同时退休 `ExportRoot -> MarkChainRootExported -> export_target/exported_at`
DB 回写路径：新导出函数是纯 filesystem emitter，成功或失败都对 PostgreSQL **零写**；
`MarkChainRootExported` query/generated method 从可执行代码移除，旧列因 forward-only
迁移留在 schema 但不再作为状态或信任输入。这样不存在“文件已写、DB marker 失败”
或同一 root 被无条件改写 target 的半提交状态。

Trusted keyring 必须独立于 PostgreSQL、archive bucket 与 RecoveryIndex store，
每条 key record 必须包含：

```text
key_id, algorithm=Ed25519, public_key,
fingerprint=sha256(raw_public_key),
purpose, protocol, valid_from, valid_until,
revoked_at, revoke_reason
```

`purpose` 固定枚举至少为 `chain_root_signing`、`archive_manifest_signing`、
`archive_checkpoint_signing`、`recovery_index_signing`、`plo_approval_signing`、
`plo_result_receipt_signing`、`kill_switch_signing`、
`archive_scrub_receipt_signing`、`archive_scrub_head_signing`；
`protocol` 分别固定到其 v1 domain。Lookup 必须重算 fingerprint，要求签名时刻落在
`[valid_from, valid_until)`，并按撤销策略拒绝；key ID 相同但 purpose/protocol
不符也必须拒绝。一个公钥/私钥材料不得登记到多个 purpose。私钥只经各自独立
CredentialRef/SecretProvider 解析，不能用 Chain Root key 签 manifest/checkpoint。

Domain bytes 固定为：既有 Chain Root 保持现行 `xm-audit-root\n...`（protocol
`audit-chain-root/v1`，不得为归档而改历史协议）；其余 envelope 统一签
`<domain>\nsha256=<unsigned_sha256>\n`，domain 分别是
`xm-audit-archive-manifest-v1`、`xm-audit-archive-checkpoint-v1`、
`xm-audit-recovery-index-v1`、`xm-audit-archive-plo-approval-v1`、
`xm-audit-archive-plo-result-v1`、`xm-audit-archive-kill-switch-v1`、
`xm-audit-archive-scrub-receipt-v1`、
`xm-audit-archive-scrub-head-v1`。跨 domain 重放测试必须失败；keyring 注册时还必须
拒绝同一 raw public/private material 被登记到两个 purpose，并逐项测试
`valid_from` 前一微秒、起点、`valid_until` 前一微秒与终点边界。

### 5.3 完整验证顺序

Verifier 必须按以下固定顺序 fail closed：

1. 用 trusted keyring 验 manifest signature；
2. 校验 previous manifest 的 key + VersionID + SHA-256，拒绝缺段、重排、分叉或回退；
3. 用签名描述符的**精确 VersionID** HEAD + GET payload/projection；拒绝 latest、
   delete marker、错误 KMS key、Object Lock mode/retain-until 回退；
4. 逐字节校验 size、provider checksum 与 SHA-256；
5. strict decode，拒绝 duplicate/unknown/missing/null、zero canonical 与 partial line；
6. 检查行数、sequence 连续性、environment row count 与投影字段白名单；
7. 按每行显式 `canonical_version` 重算 `event_hash`；
8. 校验段内 prev/event hash 和跨段边界；
9. 用 `chain_root_signing` trusted key 验 root signature；
10. 确认批次末段 `to_sequence/root_hash` 与所引用 root 链尖一致；
11. 输出结构化验证报告与明确退出码。

验证结果与运行故障必须是两个通道：`Verify(...)(VerificationReport, error)` 中，
`VerificationReport` 只承载可重复证明的 format/signature/hash/chain 成功或失败；
未知 archive/canonical version **不进入 report code**，唯一返回 typed
`*CompatibilityError{Code: verifier_outdated}`。对象网络超时、KMS 拒绝、keyring/
discovery 不可达、context deadline、请求预算和本机 I/O 唯一返回 typed
`*OperationalError`。Query 在唯一 adapter 把 failed integrity report 包装为 typed
`*IntegrityError`；HTTP/CLI 只对这些 concrete errors 使用 `errors.Is/As`，任何 caller
不得字符串匹配或把 compatibility/operational error 改写成 tamper。

### 5.4 ArchiveCheckpoint 与 RecoveryIndex

每个成功批次生成一个 immutable `CheckpointV1`。`ArtifactRefV1` 固定字段顺序为
`bucket_id,key,version_id,sha256`，四项均非空。Checkpoint unsigned bytes 的字段顺序
固定为：

```text
kind="xingmang-audit-archive-checkpoint", format_version=1, generation,
previous_checkpoint, first_manifest, terminal_manifest,
from_sequence, to_sequence, manifest_count, catalog_rows_digest,
chain_root, canonical_version_counts,
chain_integrity, capture_completeness,
eligible_action_run_count, matched_action_run_count,
missing_audit_event_count, duplicate_audit_event_count,
source_tip_sequence, source_tip_observed_at,
approval_envelope_sha256, operation_intent_digest, created_at
```

`previous_checkpoint` 使用与 `PreviousManifestRefV1` 相同的 `kind + exact locator`
结构；第一代固定为 `kind="genesis"` 与
`sha256=8a3bb7ae096a84a371c3c3ad3ac7c7b65d11d49c0da8df617b288c5475a3a171`
（`SHA256("xm-audit-archive-checkpoint-genesis-v1\n")`），以后必须是 exact
`ArtifactRefV1`。`chain_root` 是冻结的 `ChainRootRefV1`；canonical counts 使用
§5.1 的固定二元素数组。

Checkpoint 由 `archive_checkpoint_signing/v1` key 独立签名，PutIfAbsent 后按精确
VersionID 回读验真。`RecoveryIndexV1` 位于与 PostgreSQL/archive bucket 不同的受管
discovery failure domain，locator 来自只读批准配置而非 CLI/bucket List。其 unsigned
字段顺序固定为：

```text
kind="xingmang-audit-recovery-index", format_version=1,
generation, previous_generation, previous_index_sha256,
checkpoint, terminal_manifest,
terminal_sequence, terminal_root_hash, updated_at
```

第一代 `previous_generation=0` 且 `previous_index_sha256` 固定为
`710bcb439199068a84d3a0d33d9286dd1a1903112308d127a1170f85aa434bf3`
（`SHA256("xm-audit-recovery-index-genesis-v1\n")`）；后续 generation 必须严格
`previous+1`。`checkpoint` 与 `terminal_manifest` 都是 exact `ArtifactRefV1`。
RecoveryIndex 使用 `recovery_index_signing/v1` key，并适用 §5.1 的 envelope、strict
decode 与 literal/domain golden 规则。

RecoveryIndex store 只暴露 `LoadCurrent(fixedLocator)` 与
`CompareAndSwap(fixedLocator, expectedGeneration, expectedHash, signedNext)`；这是窄化
terminal-pointer CAS，不是给业务 ObjectStore 增加任意 Overwrite。数据库全丢时：

1. 从受信配置得到 keyrings 与 fixed locator，读取并验 RecoveryIndex；
2. 精确读取并验 ArchiveCheckpoint；
3. 从 terminal manifest 按每段 `PreviousManifestRefV1` 的
   `(bucket_id,key,VersionID,sha256)` 逆向走到唯一 genesis；hash-only、List/latest
   或无法精确定位上一段立即 fail closed；
4. 逐段执行 §5.3，重算 `catalog_rows_digest`；
5. 在空 catalog 的单事务中 INSERT 全部派生行并再次校验 range/digest 后提交。

因此“能连接原 PostgreSQL/catalog”绝不能成为发现 terminal manifest 的前提。

### 5.5 两类完整性结论

- `chain_integrity` 只回答“已捕获的 audit_event bytes/sequence/hash/root 是否连续且
  与签名 artifact 一致”；值为 `verified`、`failed` 或 `not_verified`，v1 行存在时
  另带 `legacy_canonical_v1_non_injective` caveat；
- `capture_completeness` 回答“应当产生审计的 terminal `action.action_run` 是否恰有
  一条匹配 audit_event”。在同一只读 repeatable-read snapshot 内，冻结 audit tip
  与 `action_run.finished_at <= snapshot_at - settle_window` 的 eligible 集合，按
  `action_run_id` 对账 missing/duplicate；值为 `verified`、`gaps_found` 或
  `not_checked`；
- PLO/系统事件若不属于 ActionRun 集合必须由明确 event class 排除，不能用 orphan
  数量猜测；`audit_write_failed` 对应 missing ActionRun 必须进入事故与 checkpoint
  caveat，不能因 chain hash 完好而把 coverage 标成完整。

## 6. 对象存储与 committed catalog

### 6.1 ObjectWriter / ExactObjectReader / AccessRecorder 能力边界

数据对象接口按职责拆开，只允许：

```go
type ObjectWriter interface {
    PutIfAbsent(ctx context.Context, intent ObjectWriteIntentV1, body io.Reader) (ObjectVersionV1, error)
    RecoverPutResult(ctx context.Context, intent ObjectWriteIntentV1) (ObjectVersionV1, error)
}
type ExactObjectReader interface {
    HeadVersion(ctx context.Context, ref ObjectVersionV1) (ObjectVersionV1, error)
    GetVersion(ctx context.Context, ref ObjectVersionV1) (io.ReadCloser, ObjectVersionV1, error)
}
type AccessRecorder interface {
    RecordRestrictedReadAttempt(ctx context.Context, record AccessRecordV1) error
    RecordRestrictedReadResult(ctx context.Context, record AccessRecordV1) error
}
```

`ObjectVersionV1` 就是 §5.1 的完整冻结类型；没有稳定 VersionID、provider checksum、
SSE-KMS 或 Object Lock 证明的 provider 不合格。`ObjectWriteIntentV1` 钉住 operation ID、
对象序号、bucket/key、content hash/size、approved protection 与 provider idempotency token。
接口不得出现
`Delete`、任意 List、latest Get、通用 Overwrite 或由调用方拼 bucket/path 的能力。
Writer 只能向固定 prefix conditional Put；Reader 只能对签名 locator 精确 Get/Head；
full payload 每次读前后都经独立 AccessRecorder 记录 operation/checkpoint/object hash、
机器身份、批准 envelope 与结果，禁止写回 `audit.audit_event` 造成递归。生产 bucket：

- 位于数据库卷之外的独立故障域；
- versioning 开启；
- WORM/Object Lock 开启，生命周期规则无自动过期/删除；
- SSE-KMS；数据库备份密钥、Chain Root 私钥、Manifest 私钥不与对象共址；
- 访问日志进入独立审计/监控链；
- 禁止 public ACL、CDN 与面向浏览器的预签名 URL。

bucket ID、供应商、region、Object Lock mode/期限、KMS key ARN/ID 与数据驻留均是
**精确审批项**；manifest/readback 任一实际值与批准值不同都 STOP。同 compose 内
的 MinIO 只能做开发 fixture，不满足“库外异故障域”。

AccessRecorder 必须是 PostgreSQL audit schema 与 archive bucket 之外、具备自身
append-only/WORM 或等价防改写策略的受信 security sink，使用独立 CredentialRef。
attempt 写失败时禁止发起 Restricted Get；result 写失败时已经读取的 bytes 不得交给
上层、不得 commit/CAS，并 page recorder failure。测试中的内存 fake 不能作为部署证据。

数据库与 terminal pointer 能力也必须按 method + credential 拆开：

```text
AuditSourceReader: BeginRepeatableReadSnapshot only
AuditSourceSnapshot: Tip, ListRange, VerifyChain, ListEligibleActionRuns, Close only
RootWriter: InsertRoot only
CatalogReader: Latest, ListRange only
CatalogWriter: CommitCoveredSegment only
RecoveryIndexReader: LoadCurrent(fixed locator) only
RecoveryIndexCASWriter: CompareAndSwap(fixed locator, expected, signed next) only
ReceiptJournalReader: LoadOperation only
ReceiptJournalWriter: BeginIntent, AppendPutResult, AppendTerminalResult only
```

这些接口不得由一个“Store”暗中恢复任意 SQL/List/latest/Delete 权限。Service 可以组合
不同实现，但每项都由不同 CredentialRef 构造；`RootSigner`、`ManifestSigner`、
`CheckpointSigner`、`RecoverySigner`、PLO/result/scrub signer 逐 purpose 注入并在签名前
核对 keyring record，不能只依赖字段名暗示用途。

#### ReceiptJournal 的实际 backing 与恢复边界

`ReceiptJournal` 不是内存 fake、容器本地文件或待 provider 决定的抽象名词。AUD2 的
exact migration 必须在 PostgreSQL `audit` schema 创建三张 append-only 表：

```text
audit.archive_operation_intent:
  operation_id PK, approval_envelope_sha256, deterministic_bytes_digest,
  canonical_intent_bytes, created_at

audit.archive_put_receipt:
  operation_id + ordinal PK/FK, object_version_bytes,
  object_version_sha256, recorded_at

audit.archive_terminal_receipt:
  operation_id PK/FK, signed_result_bytes, terminal_result_digest,
  optional_artifact_ref_bytes, recorded_at
```

三表只接收冻结 canonical bytes 与 SHA-256，不保存 payload、凭据、DSN 或 key material；
provider idempotency token 属 Restricted operation metadata，只存在于
`canonical_intent_bytes`，不得进入日志/证据。三表均有 no-update/no-delete rule，禁止
TRUNCATE；同 operation/ordinal 相同 bytes 为幂等成功，不同 bytes/version 为 conflict。

权限拆成两个独立 capability/login identity 与 CredentialRef：

- `xm_audit_archive_receipt_reader`：只允许三表固定列 SELECT；
- `xm_audit_archive_receipt_writer`：只允许三表 fixed-column INSERT；
- 两者均非 owner/superuser，不获 UPDATE/DELETE/TRUNCATE/DDL、audit event/catalog 权限；
- Service 可组合 reader/writer，但实现和连接池不可合并成通用 audit Store。

AUD2 的 migration approval digest 必须同时覆盖三表、约束、rules、queries、sqlc 输出与
正反向 `SET ROLE` 测试；只批准 `archive_segment` 不足以开工。

恢复语义固定为：进程/容器崩溃但 PostgreSQL 尚在时，从 journal 复用同一 intent bytes、
idempotency token 与 exact VersionID；PostgreSQL 在 RecoveryIndex CAS **之前**丢失时，
外部对象仍是未 committed orphan，恢复流程不得猜测或自动继续旧 operation；CAS 之后
RecoveryIndex/checkpoint 才是 committed root of trust，catalog 可据此重建。若 DB 丢失时
原 PLO result receipt 尚未发布，必须以新的、另获批准的 archive reconciliation run
产生其自身结果并引用既有 checkpoint，不得伪造原 operation 的 UUID/时间/签名。
三表纳入数据库备份，但绝不替代 RecoveryIndex。

### 6.2 Catalog 原则

未来同一 exact-approved migration 新增 `audit.archive_segment` 与 §6.1 三张
ReceiptJournal 表，但本文件不预占迁移编号；AUD2 开工时必须
在其最新 base 上动态取得下一个编号。表只保存**已经 committed 的段**，不设置
`STAGING` 状态，也不 UPDATE 状态：

```text
id, format_version,
from_sequence, to_sequence, row_count,
first_prev_hash, last_event_hash,
canonical_version_counts, environment_counts,
payload_object_key, payload_version_id, payload_sha256, payload_size_bytes,
projections,
manifest_object_key, manifest_version_id, manifest_sha256,
manifest_signature, manifest_key_id,
chain_root_id, chain_root_hash,
checkpoint_sha256, recovery_generation,
committed_at, verified_at
```

约束至少包括正区间、正行数、64-hex 哈希、唯一 `from_sequence`、唯一
`to_sequence`、唯一 payload/manifest hash。Catalog 自身也加 no-update/no-delete
规则。插入前在同一事务内取得归档 advisory lock，并确认：

```text
from_sequence = COALESCE(latest_committed.to_sequence, 0) + 1
```

Catalog 只接纳已由当前 RecoveryIndex generation 覆盖的行；可以落后并重建，
绝不能领先 terminal marker。Catalog writer 以 checkpoint hash + generation 做
幂等 INSERT；同 range 同 checkpoint 重放为成功，不同 checkpoint/range 冲突为事故。

### 6.3 Terminal marker CAS / crash 状态机

一次 run 的顺序固定如下：

1. 验证 signed PLO approval envelope 与 signed Kill Switch snapshot，读取
   `(catalog tip, RecoveryIndex generation/hash, source tip/root)` 并冻结 expected state；
2. 确定全部 bytes、UUID、签名时点、object intent/idempotency token，先以 envelope hash
   在独立 durable `ReceiptJournal` 执行 append-only `BeginIntent`；同 operation ID 不同
   bytes 拒绝，journal 不可用时不发外部 Put；
3. 逐对象 conditional Put；取得 VersionID/KMS/Lock 元数据后精确 Head+Get 验证，并在
   进入下一对象前 append exact `ObjectVersionV1` result；
4. 用这些 exact locators 生成/签名 manifest，上传、精确回读并 append result；
5. 生成/签名 CheckpointV1，上传、精确回读并 append result；
6. 再验 Kill Switch，以 Step 1 的 expected generation/hash 对 fixed RecoveryIndex
   执行一次 CAS；
   **CAS 成功是唯一 terminal commit point**；
7. CAS 成功后在 PostgreSQL 事务中幂等 materialize/reconcile catalog，生成 initial
   full `ScrubReceiptV1`，并 append signed PLO result receipt。Catalog/receipt 写失败不
   回滚 RecoveryIndex，下一次只从 journal/index 收敛，不能重签或发布下一代。

Crash/timeout 判定表：

| 观察状态 | 判定与唯一动作 |
|---|---|
| Put 明确失败 | 外部未创建；保留 intent；同 envelope 可重试同一 conditional Put |
| Put 可能已成功但响应/VersionID 丢失 | 只调用 provider-qualified `RecoverPutResult(intent)`；必须返回同内容对象的 exact VersionID/保护元数据并追加 receipt。不得 latest/List/再传；无法唯一恢复即 fail provider qualification 并 STOP |
| CAS 前其他失败 | 外部尚未 committed；保留 orphan、不删除；同 envelope 重试逐项复用 journal 中相同 bytes/key/VersionID |
| CAS 返回不确定 | `LoadCurrent` 并验签；若正好等于 proposed index hash/generation，视为 committed；若仍是 expected，重试同一 CAS；若为第三值，报 concurrent/conflict 并 STOP |
| CAS 成功、catalog 未写 | 外部已 committed；从 checkpoint 重放 catalog，禁止重签、重传或推进下一代 |
| catalog 与 index 同代 | no-op success；输出同一 result digest |
| catalog 领先 index | 不可能状态/integrity incident；禁止自动“修正” index |
| index 领先多个 generation | 逐 checkpoint 验证并补齐 catalog；任一断链 STOP |

确定性 retry envelope 钉住 operation ID、approval digest、source DB fingerprint、
environment、expected source tip/root、expected index generation/hash、from/to、format、
bucket/prefix、KMS/Object Lock、binary commit/digest 与 `valid_until`。同 operation ID
换任一字段必须拒绝；相同 envelope 的对象 bytes、签名时间、checkpoint generation
与 result digest 必须复用持久化 receipt，不能在每次重试用 `time.Now()`/新 UUID
产生另一套“同一次”制品。

Provider qualification 必须以真实批准 SDK/fixture 证明 conditional Put 的 ambiguous-
success 恢复语义：`RecoverPutResult` 只能用 provider 原生、与 intent token 绑定的结果
查询或等价原子 receipt，且能返回唯一 exact VersionID；普通 S3 兼容标签不算证明。
若供应商只能在 412 后做 latest HEAD/ListObjectVersions、或无法区分同 key 的多个版本，
AUD2 对该 provider 为 **NO-GO**，不得靠重新上传、猜 latest 或放宽 wire 继续。
批准 provider 的真实 disposable WORM bucket qualification 结果若缺失、SKIP、过期或
不是 exact approved SDK/protocol，同样是 **AUD2 STOP**；`httptest` 只证明客户端请求形状，
不能替代 provider ambiguous-success 证据。没有 test CredentialRef 时不得提交 S3 adapter/
依赖/部署配置，可将 filesystem 与纯协议部分留在更早的非 provider 片审查。

内容寻址 orphan 不删除是刻意代价；RecoveryIndex 未引用它就不是 committed 历史，
但 scrub 可报 orphan 指标，绝不能自动清理。

## 7. Hot/Cold Query 契约

### 7.1 权限与数据源

- URL、`before_seq` 开区间游标、默认/最大 limit 与 item 字段保持不变；
- 继续使用 `audit.read`，不因冷存储新建一个能看到更多内容的 scope；
- environment 仍只来自 Principal，客户端不能指定或跨环境；
- Query 只读 environment projection，永不读取或返回权威 payload 的隐藏字段；
- PostgreSQL 有所需行时优先读 hot；只有缺少所需 sequence 区间时才读 cold；
- copy-only 阶段 PostgreSQL 仍全量，因此生产 Query 默认只命中 hot，cold reader
  先由 fixture/恢复演练验证，不把未用到的复杂度伪装成容量收益。

### 7.2 Coverage

Coverage 的 scrub 状态来自两个冻结制品：

```text
ScrubReceiptV1 unsigned fields:
kind="xingmang-audit-archive-scrub-receipt", format_version=1,
receipt_id, checkpoint, recovery_generation, recovery_index_sha256,
scope="full", verifier_commit, verifier_binary_sha256,
started_at, verified_at, manifest_count, object_count, row_count,
chain_integrity, capture_completeness,
eligible_action_run_count, matched_action_run_count,
missing_audit_event_count, duplicate_audit_event_count,
caveats, result_digest

ScrubHeadV1 unsigned fields:
kind="xingmang-audit-archive-scrub-head", format_version=1,
generation, previous_generation, previous_head_sha256,
receipt, checkpoint_sha256, updated_at
```

`checkpoint`/`receipt` 均为 exact `ArtifactRefV1`；caveats 按 UTF-8 bytes 排序。
ScrubReceipt append-only PutIfAbsent 并用 `archive_scrub_receipt_signing/v1` 签名；
`ScrubHeadV1` 用不同的 `archive_scrub_head_signing/v1` key/protocol，位于独立 fixed
locator，验签后 CAS 推进。第一代 previous hash 固定为
`de53e1eb55079716ea7357015fdd0070df05d3fb755fe2b0cb1644f93f653a09`
（`SHA256("xm-audit-scrub-head-genesis-v1\n")`）。初次 terminal commit 的全量验证
也必须产生第一张 receipt；AUD5 的人工/周期 scrub 只追加新 receipt/head。
Scrub `result_digest` 同样只哈希它之前的 result facts、排除自身，再签整个 unsigned。

Query 只有在 RecoveryIndex、ScrubHead、receipt 都验签，receipt exact checkpoint hash
等于当前 checkpoint，且 `scope=full` 时才使用其 `verified_at` 与 integrity/completeness
状态。不得从 catalog `verified_at/committed_at`、对象 HEAD、进程启动时间、配置值或
最后一次尝试时间推断；receipt/head 缺失或不匹配返回 null/`not_checked`。

建议在现有响应顶层新增：

```json
{
    "coverage": {
      "complete": true,
      "oldest_available_sequence": 1,
      "archive_checkpoint_sequence": 108,
      "archive_checkpoint_committed_at": "2026-08-28T10:00:00Z",
      "archive_verified_at": "2026-08-28T10:00:01Z",
      "archive_lag_events": 0,
      "archive_lag_seconds": 0,
      "source_tiers": ["hot", "cold"],
      "chain_integrity": "verified",
      "capture_completeness": "gaps_found",
      "caveats": ["legacy_canonical_v1_non_injective"],
      "snapshot_id": "sha256:<checkpoint_sha256>"
    }
  }
}
```

这是响应契约变更，必须先审批。旧客户端可忽略新增顶层字段，但服务端与前端测试
都必须逐字段钉住语义：

| 字段 | 唯一语义 |
|---|---|
| `complete` | 本请求的 open `before_seq` 窗口已由所需 tier 完整解析，直到凑满 limit 或到达 `oldest_available_sequence`；environment 的正常全局 sequence 间隙不算缺口；它不表示 capture 完整 |
| `oldest_available_sequence` | 全局链中 Query 已知连续可判定的最小 sequence 下界，不是当前 environment 的第一条事件；空链为 0 |
| `archive_checkpoint_sequence` | 当前验签 RecoveryIndex 指向 checkpoint 的 terminal global sequence；不是 catalog max 的猜测 |
| `archive_checkpoint_committed_at` | RecoveryIndex CAS terminal commit 时刻 |
| `archive_verified_at` | 验签 `ScrubHeadV1 -> ScrubReceiptV1` 指向该 exact checkpoint 的最近一次完整 scrub 成功时刻；只做 Head/catalog read 不得刷新 |
| `archive_lag_events` | 当前 hot source tip sequence 减 checkpoint sequence；无法读取当前 tip 为 null |
| `archive_lag_seconds` | tip 已领先时，当前 hot tip `recorded_at` 与 checkpoint terminal event `recorded_at` 的非负差；已追平为 0；当前 tip 不可知为 null |
| `source_tiers` | 本页实际读取并验过的 tier，固定顺序 hot、cold；不能写配置中“可用”的 tier |
| `chain_integrity` / `capture_completeness` | 直接承载当前 checkpoint 的 signed full ScrubReceipt 中 §5.5 两个独立状态，不能互相推导 |
| `caveats` | 确定性排序的 machine-readable caveat；存在 v1 就必须带 legacy caveat |
| `snapshot_id` | 产生这些 archive/completeness 事实的 signed checkpoint SHA-256 |

尚无 committed RecoveryIndex/checkpoint 时，sequence 为 0，所有 archive 时间/lag、
两个 integrity 状态与 snapshot ID 均按契约返回 null/`not_checked`，不能用 catalog
零值、当前时间或零延迟假装已验证。若 required cold tier 不能完整解析，请求直接
失败，因此成功响应不得以 `complete=false` 搭配部分 items 偷渡降级。

### 7.3 失败与查询成本

- 请求只需 hot 且 hot 成功：正常返回，不让 cold 故障拖垮最新审计页；
- RecoveryIndex/checkpoint 已 committed、但 exact VersionID 暂不可达、KMS/keyring/
  discovery timeout：typed operational error 映射 `503 AUDIT_ARCHIVE_UNAVAILABLE`，
  不返回部分 items，不把 `next_before` 伪造成 0；
- manifest/checkpoint/index signature、hash、chain 或保护元数据不符：typed integrity
  result 映射 `500 AUDIT_ARCHIVE_INTEGRITY_FAILED` 并触发事故，不回退未验证对象；
- 未知 archive format/canonical version：typed compatibility error 映射
  `503 AUDIT_ARCHIVE_VERIFIER_OUTDATED`，不宣称数据被篡改；
- 单页最多读取 32 个 projection 对象、64 MiB、并受现有 30 秒请求期限约束；
  超预算返回 `AUDIT_ARCHIVE_QUERY_BUDGET_EXCEEDED`，不静默截断；
- environment counts 为 0 的段直接跳过，不发对象请求。

HTTP handler 只能使用 `errors.Is/As` 对稳定 typed code 映射；未知内部错误为通用
500 且不泄漏 object key、VersionID、KMS ID、payload 或 key material。CLI 对应
固定退出码：0 verified/success、1 integrity failed、2 invalid config/envelope、
3 verifier outdated、4 transient operational、5 CAS conflict；测试逐项钉住。

## 8. PII、环境与权限

权威 payload 包含现有 API 故意不返回的 `reason`、`approval_id`、`trace_id`、
`source_ip` 与 connector summaries；before/after 也可能携带人员或客户数据。
现有 `RedactDefault` 只按敏感键名处理凭据，不识别值里的 PII。归档不能在写后
二次删除/改写这些字段，否则 event hash 失效。因此：

- full payload 是 Restricted 数据；source Reader、object Writer、exact-version
  verifier Reader、restore Reader 与 AccessRecorder 使用不同 CredentialRef；
- Query projection 仍按 environment 分对象并由平台服务裁剪；
- global payload 因全局链可能混合 environment，供应商地域必须同时满足所有
  环境的数据驻留要求；
- 对象、catalog、日志只记录 CredentialRef/KMS key ID，不记录密钥；
- 访问 full payload 必须通过 AccessRecorder 产生独立 append-only 安全记录，且
  不能把“读取审计的审计”写回本审计链形成无界递归；AccessRecorder 不可用时
  Restricted Get fail closed；
- WORM 无限保留与 PII 删除义务存在潜在法律冲突，生产启用前必须由安全/合规
  负责人明确裁定；工程实现不得自行解释。

首期不新增 human scope。若后续 UI 只展示归档健康/覆盖率，可另提
`audit.archive.read`；恢复、重签、对象访问永远不通过这个 scope 开放。

DB role split 的验收不是一句“REVOKE”而是以下精确目标；所有 runtime role 必须
`NOSUPERUSER NOBYPASSRLS NOINHERIT`、不是任何目标表 owner，public/schema 默认权限
已撤销，并用 `SET ROLE` 正反向集成测试证明：

| 身份 | 只允许 | 必须拒绝 |
|---|---|---|
| `xm_audit_archive_source_reader` | CONNECT；USAGE `audit`,`action`,`core`；SELECT 指定列于 `audit_event`,`chain_root`,`action_run`,`core.environment` | 任意 INSERT/UPDATE/DELETE/TRUNCATE、DDL、catalog 写、函数/任意 SQL |
| `xm_audit_anchor_writer` | 仅 fixed-column INSERT `audit.chain_root`；tip/root 读取由 source reader 身份完成；不再 UPDATE `export_target` | SELECT 任意业务表、UPDATE/DELETE root、audit_event 写、catalog 写 |
| `xm_audit_archive_catalog_writer` | SELECT + INSERT 于 `audit.archive_segment`/catalog materialization；advisory xact lock | audit_event/action_run 读取或写、catalog UPDATE/DELETE/TRUNCATE |
| `xm_audit_archive_receipt_reader` | 三张 journal 表 fixed-column SELECT | audit_event/action_run/catalog/object 权限及任意写 |
| `xm_audit_archive_receipt_writer` | 三张 journal 表 fixed-column INSERT | SELECT 业务表、UPDATE/DELETE/TRUNCATE/DDL 与其它 audit 写 |
| `xm_audit_restore_writer` | 只在 fingerprint 匹配的 isolated target，事务内 fixed-column INSERT `audit_event` + SELECT verify | 非空/非隔离目标、UPDATE/DELETE、source/production DSN、DDL |

对象 IAM 同样拆成 `ObjectWriter` conditional Put 固定 prefix、`ExactObjectReader`
指定 version Get/Head、`RecoveryIndexWriter` fixed locator CAS、`AccessRecorderWriter`
append-only security sink。一个配置文件可以组合这些引用，但不得把权限合并成一把
bucket admin key。grants/IAM evidence 必须列实际 allow 与 explicit deny；当前
`xingmang` superuser/owner 状态只允许本地原型，不能激活 staging/production。

## 9. 生命周期操作、调度与依赖

### 9.1 为什么不是 Action

归档、full verify、scrub 与恢复会访问 Restricted payload，并改变证据制品、验证 head、
数据库拓扑或恢复状态，全部属于 ADR-003 指定的 Platform Lifecycle Operation。首期只提供版本化 CLI；禁止 UI 触发、任意路径、
任意 SQL 或普通 Action contract。`--approval-id anything` 不是批准。执行前输入与执行后
结果是两个不同的冻结制品，不能把事后结果伪装成事前批准。

`Plan` 只生成不带批准事实的 `PLOCandidateV1`：冻结 source/index/root/range、target policy、
build/binary 与 `dry_run_digest`。它**不能**填写 approval/change ID、approvers、批准时刻、
validity、nonce、idempotency key 或 Kill Switch hash，也不能签 approval。独立人类批准系统
读取 candidate exact bytes/digest、两名适格 HUMAN 的实际批准记录和当时已验签的
KillSwitch snapshot，构造并以 `plo_approval_signing/v1` 签署下列完整 envelope。
运行 CLI 只接受 signed envelope 文件；candidate/digest、裸 approval ID 或命令行字段均
不能升级成执行授权。

`PLOCandidateV1` canonical 字段顺序固定为：

```text
kind="xingmang-audit-plo-candidate", format_version=1,
purpose, environment, source_database_fingerprint, isolated_target_fingerprint,
target_policy, recovery_index_fixed_locator_ref,
expected_source_tip, expected_root_hash,
expected_index_generation, expected_index_hash,
from_sequence, to_sequence, archive_format_version,
build_commit, binary_sha256, dry_run_digest
```

candidate 不使用 approval signing key；批准系统把其 exact SHA-256 连同批准事实写入审批
记录，再构造 envelope。Envelope 必须逐字段复核 candidate 的上述事实，不能只信
`dry_run_digest` 字符串。

`PLOApprovalEnvelopeV1` unsigned 字段顺序固定为：

```text
kind="xingmang-audit-plo-approval", format_version=1,
operation_id, change_id, approval_id, approvers, purpose,
environment, source_database_fingerprint, isolated_target_fingerprint,
target_policy, recovery_index_fixed_locator_ref,
expected_source_tip, expected_root_hash,
expected_index_generation, expected_index_hash,
from_sequence, to_sequence, archive_format_version,
build_commit, binary_sha256,
valid_from, valid_until, nonce, idempotency_key,
candidate_sha256, dry_run_digest, kill_switch_snapshot_sha256
```

`approvers` 按 stable human ID UTF-8 bytes 排序，至少两名不同且满足批准策略的 HUMAN，
每项固定为 `principal_id,approval_role,approved_at`；`purpose` 仅允许 `archive`、
`verify`、`scrub`、`restore`。`target_policy` 精确绑定 bucket/region/prefix、KMS key、Object Lock
mode/retain-until、residency 与 provider/SDK protocol；不适用的 isolated target
fingerprint 使用显式空 string。Envelope 用 `plo_approval_signing/v1` trusted key 签名。

独立 `KillSwitchSnapshotV1` unsigned 字段固定为：

```text
kind="xingmang-audit-kill-switch", format_version=1,
environment, purpose, generation, enabled,
valid_from, valid_until, issued_at, issuer
```

它用 `kill_switch_signing` / `xm-audit-archive-kill-switch-v1` 单独验签；approval
envelope 绑定其 signed SHA-256。所有 purpose 启动时都重读 fixed locator，要求
hash/generation 不回退、`enabled=true` 且当前时刻位于 `[valid_from,valid_until)`；
不可达/变化/禁用/过期即 fail closed。archive 还要在签 root、每个 Put、Checkpoint、
RecoveryIndex CAS 与下一段前重验；verify/scrub 在每个 Restricted Get 与 ScrubHead CAS
前重验；restore 在开启写事务前、第一条 INSERT 前和 COMMIT 紧前重验，失败必须
ROLLBACK，不能用启动时 snapshot 撑完整个长操作。

`PLOResultReceiptV1` 是事后 append-only signed artifact，字段顺序固定为：

```text
kind="xingmang-audit-plo-result", format_version=1,
operation_id, approval_envelope_sha256, purpose,
started_at, finished_at, object_results,
checkpoint, recovery_generation, recovery_index_sha256,
verification_report, scrub_receipt,
exit_code, outcome, result_digest
```

`checkpoint` 与 `scrub_receipt` 使用冻结 `OptionalArtifactRefV1`：字段固定为
`kind,bucket_id,key,version_id,sha256`；`kind="none"` 时其余四项必须空，
`kind="object"` 时四项必须全非空。这样 envelope 已接受但在对象/checkpoint 前失败的
operation 仍能产生唯一 strict result，而不是使用 null、伪造 locator 或跳过 result。
未推进 index 时 `recovery_generation=0`、`recovery_index_sha256` 显式空 string，
`verification_report` 显式记录 `not_run` 或实际报告。

`object_results` 按 intent ordinal 排序且每项保存 `ObjectVersionV1`；receipt 使用
`plo_result_receipt_signing/v1` key。Plan 只读产生 approval candidate/dry-run digest；
`result_digest` 是其前面全部 result facts（排除 `result_digest` 自身）的 canonical
bytes SHA-256，随后整个 unsigned receipt 再按通用 envelope 签名。
Run 拒绝任何字段漂移、过期、自签或 key purpose 错误，并在恢复时要求非空、匹配的
isolated target fingerprint。CLI 只能选择 signed approval envelope，不能覆盖 range/
bucket/KMS/target。AccessRecorder 只保存 envelope/result digest，不保存敏感值。

archive、verify、scrub、restore 在 signed envelope 被接受后，都必须把 success/failure
写成独立 `SignedPLOResultReceiptV1`，通过 append-only result journal/PutIfAbsent 持久化；
任何 API/CLI 返回的普通 report 只是该 signed result 的投影，不能替代它。若原数据库在
terminal CAS 后、result 发布前丢失，新的人工批准 archive reconciliation run 只能发布
自己的 operation/result，并引用既有 checkpoint；不得补签或伪造丢失的原 operation。

### 9.2 自动调度硬依赖

在以下两项合入并验证前，archive 只能人工执行，不能注册 River 周期任务：

1. R2-10 集群级租约、重复探测与幂等所有权；
2. DB 角色拆分：export reader 只有 audit SELECT，catalog writer 只有 catalog
   INSERT；运行账号不再是超级用户或表 owner。

满足依赖后，建议每小时或每 10,000 个新事件触发一次，以先到者为准；River
唯一任务只是第二道防线，RecoveryIndex CAS 与 deterministic envelope 才是最终
幂等/terminal 边界。`XM_AUDIT_ARCHIVE_ENABLED` 只允许默认 false 的本地禁用，
不能单独授予执行；每轮另读独立受信 Kill Switch（environment、generation、签名、
valid-until）。任一 disabled/过期/不可达都在签名、Put、CAS 与下一段之间 fail closed。

不再使用 `chain_root.export_target` UPDATE 充当归档状态。AUD1 退休 embedded-key
root export 后，AUD3 只复用已验 trusted key 的 pending root；RecoveryIndex 落后时
按 §6.3 收敛。已发布 index/checkpoint/manifest 永不回退或替换。应用/配置 rollback
只允许关闭 scheduler/冷读、回滚 binary 并保留新 schema/objects/index；数据面采用
forward repair。禁止执行 down migration、删除对象、降低 retain-until 或把 index
CAS 回旧 generation。

生产激活后，continuous scrub 至少每小时验证 current RecoveryIndex/checkpoint、
每日从 checkpoint 抽取并精确读取对象、每周完成全 manifest/chain/ActionRun 对账；
任何 format/key/KMS/provider 变更后立即全 scrub。监控同时计算
`lag_events`、`lag_seconds`、`checkpoint_age_seconds`、`last_full_scrub_age_seconds`、
CAS conflict、catalog reconciliation backlog、capture gap；RPO 阈值连续两个周期
或 15 分钟（先到者）超限 page 受信 PLO 值班链，不能只在季度演练时发现。

## 10. RPO、RTO 与恢复演练

建议目标，待审批：

- 库外 archive RPO：≤ 1 小时；
- 从已 committed 归档恢复审计链的 RTO：≤ 4 小时；
- 演练频率：每季度一次，以及 format、canonical、签名 key、KMS、对象供应商
  变更后各一次。

恢复必须在空的隔离数据库完成，且不依赖原 PostgreSQL catalog：

1. 从独立 trusted keyring + fixed RecoveryIndex locator 发现并验证 checkpoint/
   terminal manifest exact VersionID；
2. 重建并核验 catalog digest，再按 manifest 链完成 §5.3 全部验证；
3. 用版本化 importer 保留原 ID、sequence、时间、所有 summary、hash 与
   `canonical_version`；
4. 禁止向非空审计表追加“恢复行”，禁止手工补链；
5. 在**同一个数据库事务、同一 tx-bound verifier** 内完成 fixed-column 导入、
   `VerifyChain(1,to)`、row count/canonical counts/root match，全部通过后才 COMMIT；
   任一失败 ROLLBACK，禁止“先提交再验”；
6. 对每个 environment 做 Query 数量、游标边界、随机样本与隐藏字段抽查；
7. 记录实测 RPO/RTO、对象数、字节数、RecoveryIndex/checkpoint/manifest VersionID、
   ActionRun 对账、失败重试和证据路径；
8. 未通过恢复演练的对象只能称“归档副本”，不能称“可恢复备份”。

数据库全库备份、KEK/SOPS/age 私钥恢复仍是独立能力；本流程不能替代它们。

## 11. 分片与 TDD 入口

配套计划 `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md`
把实现拆成五个独立 worktree/PR：

| 分片 | 可独立验收的产物 | 关键前置 |
|---|---|---|
| AUD1 | 冻结 event/root/manifest wire/strict decoder、typed verifier、purpose keyring、退休 embedded-key 与 legacy export marker、mixed v1/v2/negative goldens | 本设计获批 + fresh staging snapshot |
| AUD2 | exact-version Object Reader/Writer、ambiguous-Put qualification、durable receipt、WORM/KMS、CheckpointV1/RecoveryIndexV1、可重建 catalog migration | 供应商/地域/WORM/KMS + exact migration diff 审批 |
| AUD3 | signed approval/result、Kill Switch、CAS crash state machine、ActionRun 对账、精确 Reader/Writer/AccessRecorder、initial signed ScrubReceipt/Head、手工 lifecycle；自动调度保持关闭 | DB 角色拆分；自动调度另依赖 R2-10 |
| AUD4 | hot/cold Query、只信 ScrubReceipt 的 coverage 与明确失败语义 | Query 契约审批；AUD2/AUD3 |
| AUD5 | RecoveryIndex 起步的 catalog rebuild、同 tx 恢复验证、continuous scrub/RPO/RTO/rollback runbook | AUD1～AUD4；恢复权限审批 |

每片必须先写失败测试、确认 red，再做最小实现、全绿后交付；每片独立分支、
worktree、PR，不自行合并。AUD1～AUD5 均不得包含物理瘦身或审计删除。

## 12. 审批问题与默认值

未获得逐项答案前，右列默认值生效：

| 审批问题 | 默认值（未批准时） |
|---|---|
| “永不删”是否允许已验证后的物理迁移、分区摘除或源副本回收？ | **不允许**；copy-only |
| 对象存储供应商、region、WORM 模式/期限、KMS 与数据驻留？ | 不接生产对象存储；AUD2 停在审批门 |
| WORM 无限保留与 PII 删除/驻留义务如何裁定？ | 不激活生产归档；不扩大读取面 |
| 是否接受 Query 顶层 `coverage` 契约？ | 不改 HTTP 契约；AUD4 不实施 |
| 是否接受 archive RPO ≤1h、restore RTO ≤4h 及 continuous scrub/lag alerts？ | 不激活 scheduler；仅报告实测，不作达标声明 |
| 是否新增 `audit.archive.read` 状态 scope？ | 不新增；事件仍只用 `audit.read` |
| 是否允许 UI/Action 触发归档或恢复？ | 不允许；仅人工批准 PLO |
| 是否允许在 R2-10/DB 角色拆分前自动调度？ | 不允许；仅手工 CLI |

## 13. 本设计的审批判定

批准本设计只表示同意上述架构边界与 AUD1～AUD5 顺序，并不授权一次性实现所有
切片。至少还需要：

1. AUD1 开工批准；
2. AUD2 的迁移、对象供应商、依赖与部署配置批准；
3. AUD4 的 HTTP 契约批准；
4. AUD5 的恢复身份、隔离目标与演练窗口批准；
5. 任何物理瘦身另立 ADR/CR，默认 No-Go。

明确否决信号：实现中出现审计 `DELETE/UPDATE/TRUNCATE/DROP`、任意 Delete/List/
latest-object API、ambiguous Put 后重传/猜 VersionID、浏览器对象 URL、artifact
embedded key 自认证、legacy `export_target` DB marker、运行时 struct 直接充当 signed
wire、按 environment 拆权威链、missing/zero canonical 降级 v1、重算 v1 为 v2、
catalog 领先 RecoveryIndex、CAS 不确定时盲重试、从 catalog/HEAD 推断
`archive_verified_at`、恢复先 commit 后 verify、cold 失败伪装成空页、
普通 Action/AI 自动恢复，任一项出现都应立即停止并把 PR 标为 `BLOCKED:`。
