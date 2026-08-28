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
3. 每段由 manifest 绑定 payload/projection 的哈希、相邻段、Chain Root 与
   `canonical_version` 分布，并由独立可信 keyring 中的公钥验证签名；
4. 对象写入只支持 `PutIfAbsent`，接口层不提供 Delete/Overwrite；生产对象存储
   必须启用 versioning、WORM/Object Lock、KMS 加密，且位于 PostgreSQL 之外的
   独立故障域；
5. `GET /api/v1/audit/events` 未来可通过统一 Query 在 hot/cold 间透明翻页，
   仍只允许调用者自己的 environment、仍使用 `audit.read`，并显式返回覆盖率、
   来源层与验证时刻；
6. 归档、验证、恢复是 Platform Lifecycle Operation，不是普通 Action；
7. 默认只做 copy-only。**copy-only 不会释放 PostgreSQL 容量**。任何物理瘦身、
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
| 事件链 | staging 现有 sequence 1～108；103 条 v1、5 条 v2 | 测试夹具必须覆盖真实的混合版本链 |
| Chain Root | 库表与签名/导出库代码存在；staging root 数为 0 | 第一个归档段前必须先产生并导出可信 root |
| 验证工具 | `cmd/audit-verify` 只验证 PostgreSQL 区间 | 必须新增对象、manifest、跨段与恢复验证 |
| Query | `/api/v1/audit/events` 按 environment + sequence 游标读取热表 | 冷读不能改变权限、游标或字段裁剪 |
| 对象存储 | compose、依赖与运行栈均不存在 | 生产 adapter 与供应商/地域必须先审批 |
| 备份恢复 | 有审计恢复 runbook，没有平台数据库真实备份服务 | 归档不能冒充完整数据库备份 |
| 数据库权限 | 当前应用账号仍是超级用户和表 owner | 生产自动归档必须依赖 DB 角色拆分完成 |
| 多副本任务 | River 有周期唯一性，但无 R2-10 集群级所有权 | 自动调度必须等待 R2-10；先做人工 PLO |

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
         ├─ signed manifest（对象哈希 + 边界 + root + prev manifest）
         └─ committed catalog（全部对象回读验真后才 INSERT）

GET /api/v1/audit/events
    └─ AuditQuery
         ├─ hot PostgreSQL reader（优先）
         └─ cold projection reader（只在所需范围不在 hot 时使用）
```

权威 payload 与人读 projection 必须分开：前者要能重算链哈希，所以保存所有字段；
后者只允许出现现有 HTTP 响应字段。projection 不是新的证据链，它的可信度来自
manifest 对 projection hash 的签名以及它与同段权威 payload 的 sequence 映射。

## 4. 分段与文件格式

### 4.1 分段边界

- payload 段按全局 sequence 升序、闭区间 `[from_sequence, to_sequence]`；
- 第一段 `from_sequence=1`，首行 `prev_hash=GenesisHash`；后续段必须满足：
  `from = previous.to + 1` 且首行 `prev_hash = previous.last_event_hash`；
- 单段在 25,000 行或 64 MiB 未压缩 payload 时切分，以先到者为准；
- 单行超过 64 MiB 时允许形成一行段，并在 manifest 记录
  `oversized_record_count=1`；不得截断 summary；
- v1 首版不压缩。这样对象哈希、离线查看和 Range/流式读取没有压缩层歧义；
  后续压缩格式属于新的 `format_version`，不得就地改变 v1；
- 同一归档批次可产生多个段，但最后一段的 `to_sequence` 必须等于所引用
  Chain Root 的 `to_sequence`。

### 4.2 权威 payload

媒体类型为 `application/x-ndjson; charset=utf-8`：UTF-8、无 BOM、LF 换行、
sequence 升序、每行一个 JSON object、末尾保留一个 LF。每行必须包含当前
`audit.Event` 的全部持久化字段：

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
- 不添加展示字段、推导字段或二次脱敏；任何变换都会让恢复内容偏离原链；
- verifier 必须把每行恢复为 `audit.Event`，按该行 `canonical_version` 调用现有
  `ComputeHash`；未知版本返回“需要更新 verifier”，不得误报为篡改；
- v1/v2 实现与历史 golden fixture 永不删除。

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

对象键由 sequence 范围与内容哈希组成，不能由调用方提供任意路径：

```text
audit/v1/payload/seq-<from19>-<to19>-<sha256>.ndjson
audit/v1/projection/<environment>/seq-<from19>-<to19>-<sha256>.ndjson
audit/v1/manifest/seq-<from19>-<to19>-<manifest_sha256>.json
```

`environment` 必须通过现有 environment ID 校验并编码，不能含 `/`、`..` 或
控制字符。相同内容重试命中相同键；同键内容不同时必须报 collision 事故。

## 5. Manifest、签名与链连续性

### 5.1 unsigned manifest 字段

每个段的 unsigned manifest 至少包含：

| 组 | 必填字段 |
|---|---|
| 身份 | `kind=xingmang-audit-archive-manifest`、`format_version=1`、`exporter_version`、`exporter_commit` |
| 区间 | `from_sequence`、`to_sequence`、`row_count`、`first_prev_hash`、`first_event_hash`、`last_event_hash` |
| 连续性 | `prev_manifest_sha256`；第一段为 64 个 `0` |
| 编码 | `canonical_version_counts`、`oversized_record_count` |
| payload | `object_key`、`sha256`、`size_bytes`、`content_type` |
| projections | 每 environment 的 `object_key`、`sha256`、`size_bytes`、`row_count` |
| Chain Root | `chain_root_id`、`root_from_sequence`、`root_to_sequence`、`root_hash`、`root_signature`、`root_key_id` |
| 加密 | `encryption_mode`、`kms_key_id`；只记标识，不记密钥材料 |
| 时点 | `created_at`、`source_tip_observed_at` |

Manifest 不声称 Chain Root 的 `from_sequence` 是整条链起点；当前 root 的
`root_hash` 是链尖，签名覆盖其原有载荷。验证归档段时仍必须从 Genesis 或上一
manifest 边界连续重算，不能只信 root 的区间文字。

### 5.2 Manifest 签名

Manifest 使用与 Chain Root **不同的签名 key**，避免协议复用：

1. unsigned manifest 按固定字段顺序、无多余空白编码；
2. `manifest_sha256 = SHA256(unsigned_manifest_bytes)`；
3. 签名载荷为：
   `xm-audit-archive-manifest-v1\nsha256=<manifest_sha256>\n`；
4. 外层 envelope 保存 `unsigned_manifest`、`manifest_sha256`、
   `signature_algorithm=Ed25519`、`signature_key_id`、`signature`；
5. verifier 从独立 trusted keyring 按 key ID 取公钥。对象内自带的公钥即使存在
   也不能成为信任来源。

Trusted keyring 必须独立于 PostgreSQL 和归档 bucket 保存，至少记录 key ID、
算法、公钥、指纹、有效期、撤销时刻与原因。Chain Root keyring 与 Manifest
keyring 分栏；私钥均只经 CredentialRef/SecretProvider 解析。

### 5.3 完整验证顺序

Verifier 必须按以下固定顺序 fail closed：

1. 用 trusted keyring 验 manifest signature；
2. 校验 `prev_manifest_sha256`，拒绝缺段、重排、分叉或回退；
3. HEAD + GET payload/projection，逐字节校验 size 与 SHA-256；
4. 检查行数、sequence 连续性、environment row count 与投影字段白名单；
5. 按每行 `canonical_version` 重算 `event_hash`；
6. 校验段内 prev/event hash 和跨段边界；
7. 用独立 Chain Root keyring 验 root signature；
8. 确认批次末段 `to_sequence/root_hash` 与所引用 root 链尖一致；
9. 输出结构化验证报告与明确退出码，不能把未知版本、网络失败和篡改混成一种错误。

## 6. 对象存储与 committed catalog

### 6.1 ObjectStore 能力边界

业务接口只允许：

```go
type ObjectStore interface {
    PutIfAbsent(ctx context.Context, key string, body io.Reader, size int64, sha256 string) (ObjectMeta, error)
    Head(ctx context.Context, key string) (ObjectMeta, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)
}
```

接口不得出现 `Delete`、`Overwrite`、任意 List 或由调用方拼接 bucket/path 的能力。
实现必须使用受限机器凭据；写身份只能向固定 archive prefix Put，读身份只允许
Get/Head，恢复身份单独授予。生产 bucket 必须：

- 位于数据库卷之外的独立故障域；
- versioning 开启；
- WORM/Object Lock 开启，生命周期规则无自动过期/删除；
- SSE-KMS；数据库备份密钥、Chain Root 私钥、Manifest 私钥不与对象共址；
- 访问日志进入独立审计/监控链；
- 禁止 public ACL、CDN 与面向浏览器的预签名 URL。

具体供应商、region、Object Lock 模式/期限、KMS 与数据驻留是审批项；同 compose
内的 MinIO 只能做开发 fixture，不满足“库外异故障域”。

### 6.2 Catalog 原则

未来迁移新增 `audit.archive_segment`，但本文件不预占迁移编号；AUD2 开工时必须
在其最新 base 上动态取得下一个编号。表只保存**已经 committed 的段**，不设置
`STAGING` 状态，也不 UPDATE 状态：

```text
id, format_version,
from_sequence, to_sequence, row_count,
first_prev_hash, last_event_hash,
canonical_version_counts, environment_counts,
payload_object_key, payload_sha256, payload_size_bytes,
projections,
manifest_object_key, manifest_sha256,
manifest_signature, manifest_key_id,
chain_root_id, chain_root_hash,
committed_at, verified_at
```

约束至少包括正区间、正行数、64-hex 哈希、唯一 `from_sequence`、唯一
`to_sequence`、唯一 payload/manifest hash。Catalog 自身也加 no-update/no-delete
规则。插入前在同一事务内取得归档 advisory lock，并确认：

```text
from_sequence = COALESCE(latest_committed.to_sequence, 0) + 1
```

Commit 协议：

1. 生成 payload、projection、manifest；
2. `PutIfAbsent` 上传；
3. 对每个对象执行 Head + Get 回读；
4. 完成对象 hash、manifest signature、Chain Root、行数与边界验证；
5. 最后才 INSERT catalog committed 行。

任一步失败都不写 catalog。内容寻址的孤儿对象不删除，重试时复用；这是没有
Delete API 的刻意代价，优先于“失败清理器误删证据”。

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

建议在现有响应顶层新增：

```json
{
    "coverage": {
      "complete": true,
      "oldest_available_sequence": 1,
      "archive_checkpoint_sequence": 108,
    "archive_verified_at": "2026-08-28T10:00:00Z",
    "archive_lag_seconds": 1200,
    "source_tiers": ["hot", "cold"]
  }
}
```

这是响应契约变更，必须先审批。旧客户端可忽略新增顶层字段，但服务端与前端测试
都必须钉住其语义。`complete=true` 只表示本次游标范围内没有已知缺口，不代表
所有环境都有事件，也不把未来未归档的新事件判成缺口。
尚无 committed archive 时，`archive_checkpoint_sequence=0`，
`archive_verified_at` 与 `archive_lag_seconds` 必须为 `null`，不能用零时间或
零延迟假装已经验证。

### 7.3 失败与查询成本

- 请求只需 hot 且 hot 成功：正常返回，不让 cold 故障拖垮最新审计页；
- catalog 声称某范围已 committed，但对象缺失、hash/signature 不符或 cold
  暂不可达：返回 `503 AUDIT_ARCHIVE_UNAVAILABLE`，不返回部分 items，不把
  `next_before` 伪造成 0；
- manifest/链校验失败：返回内部错误并触发审计完整性事故，不自动回退到未验证对象；
- 未知 `canonical_version`：返回 `AUDIT_ARCHIVE_VERIFIER_OUTDATED`，指向升级
  verifier，不宣称数据被篡改；
- 单页最多读取 32 个 projection 对象、64 MiB、并受现有 30 秒请求期限约束；
  超预算返回 `AUDIT_ARCHIVE_QUERY_BUDGET_EXCEEDED`，不静默截断；
- environment counts 为 0 的段直接跳过，不发对象请求。

## 8. PII、环境与权限

权威 payload 包含现有 API 故意不返回的 `reason`、`approval_id`、`trace_id`、
`source_ip` 与 connector summaries；before/after 也可能携带人员或客户数据。
现有 `RedactDefault` 只按敏感键名处理凭据，不识别值里的 PII。归档不能在写后
二次删除/改写这些字段，否则 event hash 失效。因此：

- full payload 是 Restricted 数据，只给 archive writer、verifier、restore
  三种机器身份；
- Query projection 仍按 environment 分对象并由平台服务裁剪；
- global payload 因全局链可能混合 environment，供应商地域必须同时满足所有
  环境的数据驻留要求；
- 对象、catalog、日志只记录 CredentialRef/KMS key ID，不记录密钥；
- 访问 full payload 必须产生独立访问审计，且不能把“读取审计的审计”递归写成
  无界循环；建议写结构化安全日志并进入独立日志/告警链；
- WORM 无限保留与 PII 删除义务存在潜在法律冲突，生产启用前必须由安全/合规
  负责人明确裁定；工程实现不得自行解释。

首期不新增 human scope。若后续 UI 只展示归档健康/覆盖率，可另提
`audit.archive.read`；恢复、重签、对象访问永远不通过这个 scope 开放。

## 9. 生命周期操作、调度与依赖

### 9.1 为什么不是 Action

归档与恢复改变的是平台证据制品、数据库拓扑或恢复状态，属于 ADR-003 指定的
Platform Lifecycle Operation。首期只提供版本化 CLI；禁止 UI 触发、任意路径、
任意 SQL 或普通 Action contract。每次执行必须有：

- 变更单/批准号；
- build version/commit、request/run ID；
- 明确 source database 与 target bucket/environment；
- 预览区间、预计行数与字节数；
- 逐对象结果、最终验证报告与退出码；
- Kill Switch；恢复动作另需隔离环境与人工批准。

### 9.2 自动调度硬依赖

在以下两项合入并验证前，archive 只能人工执行，不能注册 River 周期任务：

1. R2-10 集群级租约、重复探测与幂等所有权；
2. DB 角色拆分：export reader 只有 audit SELECT，catalog writer 只有 catalog
   INSERT；运行账号不再是超级用户或表 owner。

满足依赖后，建议每小时或每 10,000 个新事件触发一次，以先到者为准；River
唯一任务只是第二道防线，catalog 锁与 content-addressed `PutIfAbsent` 才是最终
幂等边界。`XM_AUDIT_ARCHIVE_ENABLED=false` 是显式 Kill Switch；非法配置必须
拒绝启动，不静默回退。

Chain Root 的生成早于对象提交，因此必须支持恢复重试：如果最新 root 的
`to_sequence` 大于最新 committed catalog 的 `to_sequence`，说明上次运行可能在
签 root 后、catalog commit 前失败；下一次运行必须先全链复核并复用这枚 root，
继续导出未 committed 区间。只有 root 与 catalog 均覆盖当前 tip 时才是 no-op。
Manifest/catalog committed 后，`chain_root.export_target` 才回写为该 manifest 的
content-addressed object key；回写失败可重试，但不得因此重签或替换 root。

## 10. RPO、RTO 与恢复演练

建议目标，待审批：

- 库外 archive RPO：≤ 1 小时；
- 从已 committed 归档恢复审计链的 RTO：≤ 4 小时；
- 演练频率：每季度一次，以及 format、canonical、签名 key、KMS、对象供应商
  变更后各一次。

恢复必须在空的隔离数据库完成：

1. 从独立 trusted keyring 载入 root/manifest 公钥；
2. 按 manifest 链下载对象并完成 §5.3 全部验证；
3. 用版本化 importer 保留原 ID、sequence、时间、所有 summary、hash 与
   `canonical_version`；
4. 禁止向非空审计表追加“恢复行”，禁止手工补链；
5. 导入后运行数据库全链 `audit-verify`，链尖必须等于可信 Chain Root；
6. 对每个 environment 做 Query 数量、游标边界、随机样本与隐藏字段抽查；
7. 记录实测 RPO/RTO、对象数、字节数、失败重试和证据路径；
8. 未通过恢复演练的对象只能称“归档副本”，不能称“可恢复备份”。

数据库全库备份、KEK/SOPS/age 私钥恢复仍是独立能力；本流程不能替代它们。

## 11. 分片与 TDD 入口

配套计划 `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md`
把实现拆成五个独立 worktree/PR：

| 分片 | 可独立验收的产物 | 关键前置 |
|---|---|---|
| AUD1 | 确定性格式、本地 exporter、离线 verifier、mixed v1/v2 golden | 本设计获批 |
| AUD2 | `PutIfAbsent` ObjectStore、WORM/KMS 配置、动态编号 catalog migration | 供应商/地域/WORM/KMS + 迁移审批 |
| AUD3 | Chain Root + manifest PLO、trusted keyring、committed 协议；自动调度保持关闭 | DB 角色拆分；自动调度另依赖 R2-10 |
| AUD4 | hot/cold Query、coverage 与明确失败语义 | Query 契约审批；AUD2/AUD3 |
| AUD5 | 隔离恢复、RPO/RTO 演练、生产激活 runbook | AUD1～AUD4；恢复权限审批 |

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
| 是否接受 archive RPO ≤1h、restore RTO ≤4h？ | 仅作为建议，不作达标声明 |
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

明确否决信号：实现中出现审计 `DELETE/UPDATE/TRUNCATE/DROP`、Delete/Overwrite
对象 API、浏览器对象 URL、按 environment 拆权威链、重算 v1 为 v2、未验证即
写 committed catalog、cold 失败伪装成空页、普通 Action/AI 自动恢复，任一项
出现都应立即停止并把 PR 标为 `BLOCKED:`。
