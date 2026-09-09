# XM-ERRCODE-ALERTS-ACK：确认一条刚恢复的告警不该报 502

- **status:** implemented，未上线。**含一处对外可见的状态码变化**（502 → 409）。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-DBTEST-CLEANUP 提交），
  基线 `aaba954`。
- **来源：** XM-ERRCODE-NOTFOUND 的 follow_up：「其余三个包的 Action handler 里
  逐条跟 `return nil, err` 的来源，看还有没有别的错分。」

## 结果：一个真 bug，而且是我上一片漏掉的

逐条跟了三个包的 16 处裸返回：

| 包 | 处数 | 来源 | 判断 |
|---|---|---|---|
| assurance | 4 | 全是 `callerPrincipal` | 已映射，无问题 |
| credentials | 5 | `callerPrincipal` / `refParam` | 已映射，无问题 |
| alerts | 7 | 见下 | **两处要修** |

alerts 的七处里：四处是 `callerPrincipal` / `intParam`（已映射），
一处是 `requireStore`（装配错误，生产不可达），剩下两处是真的：

### 1. `store.Acknowledge` —— 真 bug，运维会撞到

`Acknowledge` 会返回两个哨兵：`ErrNotFound`，以及
**`ErrNotAcknowledgeable`**（「只有 OPEN / REOPENED 可确认」）。两个都被裸返回，
内核归一成 `EXECUTION_FAILED`（**502**）。

**这条会真的被撞到**：评估器每一轮都会把不再命中的告警自动转 RESOLVED，
而运维点「确认」的那一刻，它可能刚好已经恢复了。这个再自然不过的竞态，
此前返回的是「服务端执行失败」。

**`ErrNotAcknowledgeable` → 409 `CONFLICT`**：告警在，只是状态与请求冲突——
这正是 409 的定义。412 那一档留给「参数指名的对象不存在」（`ErrNotFound`
已经在那一档）。

### 2. `store.CreateSilence` —— 防御性，当前不可达

`CreateSilence` 内部调 `Silence.Validate()`，会返回 `ErrMissingField` /
`ErrInvalidFormat`。但这个 handler **已经先校验过**：`rule_key` 走
`KnownRuleKey`（比 Validate 的正则更严）、`duration_minutes` 有范围判断、
`reason` 有空白判断，其余字段来自 Principal（内核已校验）。

所以这一支**今天走不到**。仍然映射它，理由写在代码注释里：Store 的契约确实
会返回它们，留一条正确的翻译比留一个 502 的口子便宜。**没有把它算作 bug**。

## 上一片漏了什么

XM-ERRCODE-AUDIT 修的 `store.Get` 与本片修的 `store.Acknowledge`
**在同一个 handler 里，相隔两行**。我上一片改了撞见的那一条就走了。

那一片的 follow_up 自己写着「没有把每个 handler 里的每一条 `return nil, err`
都跟到源头」——这次跟了，第一条就是它。

**教训**：修一个 handler 里的错误分类时，把那个 handler 从头到尾跟一遍，
不要只修撞见的那一条。

## 测试

`TestAcknowledgeResolvedAlertIsConflictNotServerError`（`audit_integration_test.go`）
从 **Action 入口**经真内核打进来：造一条告警 → 用 `Resolve` 让它自动恢复
（与评估器每轮做的一样）→ 确认它 → 断言 409 与文案。

**带对照组**：同一个 fixture 里再造一条**没有**被解决的告警，确认得掉——
证明上面拒的是状态，不是这条 Action 整个坏了。

### 变异验证

| 变异 | 结果 |
|---|---|
| `Acknowledge` 退回裸返回 | 红，`EXECUTION_FAILED`（逐字复现修前的形态） |
| `ErrNotAcknowledgeable` 不映射 | 红，同上 |

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`gofmt` 干净、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未改。

## 顺带评估、决定不做的一条

**XM-JOBS0 的「kind/state 筛选进 URL」**（本轮候选之一）：核过之后**不做**，
并说明为什么。

那个 follow_up 说的不是页面自己的状态——子页签早就在 URL 里（`?sub=`）。
它说的是 `DataTableV2` **内置的客户端筛选**，而那个组件：

- 把 7 个维度的状态全握在内部（搜索词、筛选、排序、可见列、密度、分页、选择）；
- **被 40 个文件在用**；
- 已经有另一套持久化机制（「保存的视图」，走 `ui.saved_view.*` Action）。

所以这件事是「改共享组件」+「这 7 个维度里哪几个该进 URL」的取舍，还要先想清
它与既有的保存视图是什么关系。不是一片能干净做完的，也不该由我单方面定。
**登记，等产品侧的意见。**

## follow_ups

- **上面那条 `DataTableV2` 的深链**（见「顺带评估」）：需要产品侧先回答
  「哪些表格状态应当可分享」，以及它与「保存的视图」的分工。
- `alerts` 的 `requireStore` 返回裸 `fmt.Errorf`，落到 502。它只在
  「注册表实例没绑 store」时触发，是装配错误、生产不可达；真要改，
  `CodeInternal` 比 `EXECUTION_FAILED` 更贴切。价值很低，记着即可。
- XM-ERRCODE-NOTFOUND 的另一条仍开着：`ACTION_NOT_REGISTERED` 字符串
  该不该改（14 个调用点 + 对外契约）。
