# 审计模块

## AUD1 确定性归档格式

`internal/platform/audit/archive` 提供本地/离线的 v1 冻结 wire、严格 decoder、
purpose/protocol 隔离 keyring、签名 manifest 与 verifier。完整字节契约见
[`ARCHIVE-FORMAT-v1.md`](ARCHIVE-FORMAT-v1.md)。它目前只建立 copy-only 格式和本地
核验能力；没有对象存储、catalog、RecoveryIndex CAS、worker、HTTP cold read、恢复或生产
激活。

新的 Chain Root 导出只写 `ChainRootRefV1`：不再把 artifact 自带 `public_key` 当作信任，
也不再回写 `chain_root.exported_at/export_target`。验证方必须从数据库和归档 bucket 之外的
独立 trusted keyring 按 key ID + purpose + protocol + validity + fingerprint 取得公钥。

`cmd/audit-archive` 仅暴露 `plan`、非 production 的 `export-local` 与 `verify-local`；
路径必须位于配置的本地 archive root，首个本地 export 必须从 sequence 1/Genesis 开始。
`local-fixture` 保护元数据不代表 WORM/KMS/生产资格。

规格 §4.4 的审计模型：完整字段事件、**防篡改哈希链**、Chain Root 签名与库外锚点。

## 为什么是哈希链

append-only 规则能挡住误操作，但挡不住拥有 DBA 权限的人——他可以先
`DISABLE RULE` 再改数据。哈希链解决的是另一个问题：**改了能被发现**。

每条事件的 `event_hash = SHA256(规范化字段 ‖ prev_hash)`。改动任何一条，
其哈希与存储值不符；删掉任何一条，序号出现缺口。`VerifyChain` 能精确定位
到第一个出问题的 `sequence`。

实测（`cmd/audit-verify`）：篡改 sequence=4 的 `reason` 字段后，
校验输出 `sequence=4 kind=hash_mismatch` 并以退出码 1 结束。

## 三类问题

| Kind | 含义 |
|---|---|
| `hash_mismatch` | 内容被改动 |
| `sequence_gap` | 记录被删除 |
| `broken_link` | `prev_hash` 与上一条的 `event_hash` 对不上 |

## Chain Root 与外部锚点

`ComputeAndSignRoot` **先全链校验再签名**——给一条已经断掉的链盖章，
等于用签名给篡改背书。签名覆盖 `from/to/root_hash/computed_at` 四项，
不只是根哈希：只签哈希的话，旧签名可以被挪到别的区间上冒充。

`ExportRoot` 把根写成 JSON（含公钥与被签载荷，**不含私钥**），供存放到
异故障域。文件可离线独立验签——有测试断言这一点。

签名密钥只经 `secrets.SecretProvider` 按 CredentialRef 解析（ADR-014），
`SignerFromSecret` 是本包唯一接触密钥材料的入口。

## 对外读取（XM-0025）

`Store.ListRecent(ctx, environment, beforeSeq, limit)` 是给人看的读取入口，
经 `GET /api/v1/audit/events` 暴露（权限 `audit.ScopeRead`，见
`docs/modules/httpapi/PERMISSIONS.md`）。

它与用于链校验的 `List(from, to)` **不能合并**，尽管都在读同一张表：

| | `List` | `ListRecent` |
|---|---|---|
| 用途 | `VerifyChain` 校验 | 看板展示 |
| 顺序 | sequence 升序 | sequence **降序** |
| 过滤 | 不过滤（必须连续） | 按 environment |
| 分页 | 区间 [from, to] | 游标 + limit（上限 100） |

关键在过滤：审计链的 sequence 是**全局**的，按环境过滤后序号必然带缺口，
而「有缺口」正是 `VerifyChain` 要报的 `sequence_gap`。让校验读一份过滤过的
数据，等于让它对着自己造出来的缺口报警。

分页游标用 `sequence` 而不是 `occurred_at`：序号由链唯一且严格递增，
时间戳会撞（同一微秒内两条），撞了就会翻页重复或漏读。
`before_seq` 是**开区间**上界（严格小于），所以「上一页最后一条的 sequence」
可以直接当下一页的游标。`before_seq=0` 表示从最新一条开始。

`limit` 上限 100，超出静默夹到上限而不是报错：审计事件带前后摘要，一条能有几 KB，
不封顶的话一次 `limit=100000` 就能把整条链拖走——既是内存风险，也是取数便利。

读路径**不校验链**：返回的 `event_hash` / `prev_hash` 是库里的原样值。
「这条记录可不可信」由 `VerifyChain` 与 Chain Root 签名回答，不由列表接口顺带回答——
让读接口顺手做校验，会让一次翻页变成一次全链扫描。

### 索引与投影（XM-0031）

`ListRecentAuditEvents` 走迁移 `000006` 建的 `(environment, sequence DESC)`
复合索引。没有它时，规划器只能沿 `audit_event_sequence_key` 倒扫全局链、逐行
过滤 environment：production 事件远多于 staging 时，翻一页 staging 要扫过中间
所有 production 行；查一个还没有事件的环境更是扫完整条链才返回空——一个
`limit=100` 的请求变成全表扫描，那是低成本的数据库 DoS 路径。

查询同时改成**显式列投影**，不再 `SELECT *`。两个 connector 摘要
（`connector_request_summary` / `connector_response_summary`）是无大小约束的
jsonb，而读 API 根本不返回它们；`SELECT *` 会把它们一路解码搬进进程内存，于是
`limit=100` 只是行数上限，不是字节上限——一页也可能是几十 MB。

代价：`Store.ListRecent` 返回的 `Event` 是**读投影**，两个 connector 摘要恒为空，
**不能对它调用 `ComputeHash`**（缺两个摘要必然对不上）。链校验走 `List` /
`VerifyChain`，那条路径取全列。两者在类型来源上就分开，比留一句注释可靠。

## 边界

- **`Canonical()` 的字段集合与顺序一旦上线即冻结**：改动会使全部历史链失效。
  需要新增字段时应发新版规范化函数并在 chain_root 标注版本，而不是就地修改。
- **摘要在入链口强制脱敏**（XM-0031）：`ActionSink.Append` 在写链之前把
  `Before/After` 过一遍 `RedactDefault`。这是纵深防御，不是把责任从 Handler
  挪过来——Handler 仍然该脱敏（只有它拦得住以别的名字混进来的凭据），但整条链
  上必须有一个机械保证：一个新写的 Handler 忘了脱敏，凭据就会一路进链再经
  `/api/v1/audit/events` 回显。宪法 7 条要求边界机械 fail closed，而 ActionSink
  是所有 Action 审计事件唯一的入链口。
  脱敏必须在**写入**侧：链不可篡改，明文一旦进链就永远在库、在链根、在备份里，
  改读 API 只是不再显示它。
- `Redact`/`RedactDefault` 按**键名**匹配（含大小写变体与嵌套 map、切片下钻），
  **不判断值本身是否敏感**。藏在值里的凭据要靠上游拦——比如 `endpoint` 里的
  `user:pass@`，由 `registry.Service.Validate` 在登记时就拒掉。
- 与 Action 内核的接线（每次执行都写审计事件）**不在本模块**，见 follow-up。

## 两个只有真实数据库能发现的坑（已修）

1. **时间精度**：PostgreSQL `timestamptz` 到微秒，Go `time` 是纳秒。
   用 `RFC3339Nano` 会让写入前算的哈希与读回后重算的对不上，链一读就断。
   现用固定微秒格式并在 `Normalize()` 截断。
2. **jsonb 数字类型**：读回后数字一律是 `float64`。调用方传 `int` 时表示不一致，
   大整数（>2^53）尤其明显。`Append` 前走 JSON 往返归一化。

   注意本包的归一化方向与 `internal/platform/ops` **相反**且刻意如此：ops 读
   `value_json` 用 `json.Decoder.UseNumber()` 保 `json.Number`，因为那里装的是
   金额 minor units，float64 会永久丢精度；本包的目标不是保精度，而是让写入前
   与读回后的表示**逐字节相同**，链才校验得过。把 UseNumber 搬进本包会让全部
   历史事件的哈希一次性对不上。审计摘要不承载金额口径；真要放金额，正确做法是
   让调用方以 decimal string 写入。
