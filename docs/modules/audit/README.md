# 审计模块

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

### 待办：索引

当前没有 `(environment, sequence DESC)` 复合索引，查询靠 `audit_event_sequence_key`
的反向扫描 + 过滤。事件量小、且各环境事件密度接近时够用；一旦生产事件远多于
staging，翻 staging 的页会退化成扫大量生产行。链上事件累积到十万量级前应补一条
forward-only 迁移加该索引（规格 §5.7：不得改已发布的迁移）。

## 边界

- **`Canonical()` 的字段集合与顺序一旦上线即冻结**：改动会使全部历史链失效。
  需要新增字段时应发新版规范化函数并在 chain_root 标注版本，而不是就地修改。
- 脱敏责任在调用方：本包提供 `Redact`/`RedactDefault`，但不代替调用方判断
  什么是敏感的。
- 与 Action 内核的接线（每次执行都写审计事件）**不在本模块**，见 follow-up。

## 两个只有真实数据库能发现的坑（已修）

1. **时间精度**：PostgreSQL `timestamptz` 到微秒，Go `time` 是纳秒。
   用 `RFC3339Nano` 会让写入前算的哈希与读回后重算的对不上，链一读就断。
   现用固定微秒格式并在 `Normalize()` 截断。
2. **jsonb 数字类型**：读回后数字一律是 `float64`。调用方传 `int` 时表示不一致，
   大整数（>2^53）尤其明显。`Append` 前走 JSON 往返归一化。
