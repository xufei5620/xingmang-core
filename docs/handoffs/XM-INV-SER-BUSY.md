# XM-INV-SER-BUSY —— 把数据库序列化冲突判为瞬时争用，而不是投影失败

- status: ready-for-review（未合入发布线，未 push，未 tag）
- branch: `ai/claude/XM-INV-SER-BUSY`
- base: `bb48f6e`… 实际为 `ai/claude/XM-INV-DEAD-REQUEUE`（`bb6e...`，见 `git merge-base`）
- worktree: `K:/发票/wt-XM-INV-SER-BUSY`
- **这一片不在恢复用户服务的关键路径上**：2026-09-07 那三条死信的清理只需要
  DEAD-REQUEUE 分支上已有的两个工具。本片解决的是**复发**。

## 它修的是什么

2026-09-07 08:32:30，三条 `source_ingest_events` 同一秒判死，`balances` 与
`usage` 两条流因 `EVENTS_DEAD` 判不就绪，该来源实例下 **19 张可开票额度、
6 个用户**全部被打成 `source_unavailable`，用户端开票中心显示「来源同步不可用」
且提交被 `assertSourceFreshTx` 拒绝，持续约 21 小时。

根因链已由生产日志坐实（2026-09-07 07:55–08:40 窗口内 12 条）：

```
WARN source event projection failed ... error="ERROR: could not serialize access
due to concurrent update (SQLSTATE 40001)"
```

`RunOnce` 对 40001 **没有分支**——全仓搜 `40001` 只在 DEAD-REQUEUE 的一句注释里
出现过——于是它落进泛型的 `PROJECTION_FAILED` 分支：每 5 分钟消耗一次尝试预算，
八次之后永久判死。而 `processing_error` 里只留下 `PROJECTION_FAILED` 这一个词，
**行上不带任何底层原因**，这也是当天定位耗时的直接原因。

## 改法

复用已经存在的「瞬时争用」路径，而不是新造一条。`ErrAccountLockBusy` 早就走
`MarkSourceEventBusy`：状态回 `queued`、`attempt_count-1`（把认领消耗的那一次
还回去）、15 秒后重试。40001/40P01 的语义与之完全一致——数据库在说「你输了这场
竞争，请重跑」，不是「这条事件有毛病」。

**关键的结构性改动是把标记集合收成一处。** 原先字面量 `'ACCOUNT_LOCK_BUSY'`
在两个文件里各钉一份：写入方 `MarkSourceEventBusy`，与就绪宽限
`sourceReadinessHealthQuery` 的 `busy_within_grace` 判据。**按那个形状加第二个
标记，会让新标记被无限重投、却始终不被宽限覆盖——流因为一条系统自己认定为良性的
事件而判不就绪，且没有任何地方会报错。** 现在：

- `transientRequeueMarkers` 是唯一定义；
- `transientRequeueMarkerSQL` 由它渲染成 SQL 数组，直接嵌进就绪查询；
- `MarkSourceEventBusy` 多收一个 `marker` 参数，并对同一份列表做校验，
  不在列表里的标记**写不进去**。

两处于是物理上只剩一份，不可能漂开。方法论见记忆
`trusted-stale-guard-is-worse-than-none` 与 `two-places-pin-same-hash`。

## files_changed

| 文件 | 改动 |
|---|---|
| `backend/internal/postgresstore/source_sync.go` | 标记常量与 `transientRequeueMarkers`、`renderTransientRequeueMarkerSQL`、`isTransientRequeueMarker`、`IsTransientSerializationFailure`；`MarkSourceEventBusy` 增加 `marker` 参数并参数化 `processing_error`；就绪宽限判据改读渲染出的数组；`sourceReadinessHealthQuery` 由 `const` 改 `var` |
| `backend/internal/application/source_processor.go` | 新增 40001/40P01 分支、`serializationBusyRetry`、`logTransientRequeue`；既有 busy 调用点补传标记 |
| `backend/internal/application/source_serialization_busy_integration_test.go` | 新增，故障注入式分级测试（三例） |
| `backend/internal/postgresstore/source_readiness_transient_marker_integration_test.go` | 新增，宽限覆盖 + 写入方校验 + 渲染不变量 |

## tests_run

`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_serbusy?sslmode=disable`，
八个代理变量 `env -u`。`go build ./...` 退出 0，`go vet` 退出 0。

新增测试的**故障注入手法**值得单独说明：用一个 `BEFORE INSERT OR UPDATE`
触发器从投影自己的事务里抛出真实 SQLSTATE，因此错误走的是完整的生产路径
（store → `ProcessSourceEvent` → `RunOnce` 的分类），而不是把错误直接递给
分类函数。**这是必要的**：规则与调用规则的地方是两段代码，只测规则的话，
就算 `RunOnce` 根本不调用它，测试照样绿。

注入点选择本身是一次实测纠错，写进这里免得下一个人重走：

1. 先挂在 `source_economic_scan_cycles`（生产冲突真正发生的那张表）——
   **打中的是 `mark source event processed`**，说明扫描周期的写发生在
   `MarkSourceEventProcessed` 里，那已经是 RunOnce 的成功路径、远在被测分类之后。
2. 改挂 `source_account_eligibility_state`、再改 `source_account_stream_watermarks`
   ——**都没打中**：那条「策略起点之前的检查点」夹具走的是纯空操作路径，
   `ProcessSourceEvent` 一张表都不写。
3. 最终换成**已引导且 active 的账号 + 用量事实**，注入点落在
   `source_usage_events`，这才真正落在被测分支上。

### 变异验证

| # | 变异 | 该红的 | 实际 | 其余 |
|---|---|---|---|---|
| 1 | `if postgresstore.IsTransientSerializationFailure(...)` → `if false && ...`（**等价于旧实现**） | 两条瞬时用例 | 两条红 | 对照组 `P0001` 绿 |
| 2 | `IsTransientSerializationFailure` → 恒真 | 对照组 | 对照组红 | 两条瞬时用例绿 |
| 3 | `renderTransientRequeueMarkerSQL` 只渲染第一个标记 | `SERIALIZATION_BUSY` 的宽限用例 | 红 | `ACCOUNT_LOCK_BUSY` 绿（对照） |
| 4 | 宽限窗口 `10 minutes` → `10 years` | 「超时后重新计入」两段 | 两个标记都红 | —— |

变异 1 是这一片最重要的一条：它证明这些断言**在旧实现下会红**。变异 3 证明
「宽限覆盖范围」不是手列的——覆盖用例由 `transientRequeueMarkers` 迭代得出，
所以**以后新增标记的当天就自动被覆盖**，而不是等谁想起来补测试。

## not_run / risks

- **`MarkSourceEventProcessed` 路径上的 40001 仍未分级。**这是上面第 1 步实测
  发现的：扫描周期发布在那个调用里，它同样可能撞 40001，届时走
  `recordIsolated`，`RunOnce` 返回错误、事件停在 `processing` 直到租约过期
  （10 分钟）后被重新认领。**它不会判死、能自愈，所以不是闩**，与本次事故的
  路径也不同（生产日志那条来自 `logProjectionFailure`，即投影路径）。
  但它确实是未分级的一处，登记为 follow_up。
- **不消耗预算的重投在理论上可以无限循环。**有界之处是就绪宽限（10 分钟）：
  争用若不消退，流仍会判不就绪、readyz 转 503。也就是说本片把「永久判死」
  换成了「延后报警」，**没有**把报警消掉。另外新增的
  `logTransientRequeue` 用与 `logProjectionFailure` 不同的措辞，正是为了让
  「普通重试」与「真失败」在日志里可区分——这是当天定位慢的另一半原因。
- **未改根因本身。**投影侧 `FOR UPDATE` 与观测侧 `FOR SHARE` 在
  `source_economic_scan_cycles` 上的争用依旧存在，本片只保证它不再致死。
  降低争用本身是更大的一片，未纳入。
- **既有 gofmt 漂移（非本片引入）**：`source_sync.go` 第 655 行附近
  `claimBindingSelect` 的拼接空格不合 gofmt，来自 DEAD-REQUEUE 分支；本片
  改动起始于第 898 行，未触及。仓库整体为 CRLF，`gofmt -l` 在本机会列出全部
  文件，故以「LF 副本单独校验」方式确认本片两个文件干净。

## follow_ups

1. 给 `MarkSourceEventProcessed`（及其他 `recordIsolated` 调用点）同样的
   40001 分级，或明确记录「此处不分级」的理由。
2. `processing_error` 只存一个泛型词的问题：`PROJECTION_FAILED` 不带 SQLSTATE，
   行上看不出原因。考虑追加一个不含载荷的原因摘要。
3. 争用本身的削减（投影侧与观测侧对扫描周期行的加锁模式）。
