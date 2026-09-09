# XM-ERRCODE-AUDIT：四个包的域错误映射复核

- **status:** implemented，未上线。**含三处对外可见的状态码/错误码变化**——
  见「变了什么」。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-LOG-INJECTED 提交），
  基线 `974afab`。
- **来源：** `docs/handoffs/slices/XM-KERNEL-ERRCODE0.md` 的 follow_up 第 2 条：
  「建议 `assurance`/`alerts`/`credentials`/`finance` 四个包的负责人各自确认
  一遍自己的 `domainError` 映射表……现在这些判断会真正生效，值得各自 owner
  过一遍。」

## 为什么值得做

那条内核 bug 期间，**四个包的映射表一个字都没生效过**——所有 Handler 错误
都被改写成 `EXECUTION_FAILED`（502）。也就是说这些表从写下来那天起就没被
真实流量验证过，而现在它们**开始决定调用方看到的 HTTP 状态**了。

复核结果：**找到三处真问题、一处死代码、一处过期注释。**

## 变了什么（对外可见）

| 位置 | 之前 | 之后 | 为什么 |
|---|---|---|---|
| `alerts` 确认/静默一个**不存在的 alert_id** | **502** `EXECUTION_FAILED` | **412** `PRECONDITION_FAILED`「指定的告警不存在」 | 这是调用方给错了 id，不是服务端故障 |
| `assurance` `expected_version` 冲突 | 412 `PRECONDITION_FAILED` | **409** `REVISION_CONFLICT` | RFC 9110 把 412 留给**条件请求头**；`expected_version` 在请求体里，「资源在你读它之后被改过」是 409 的经典场景 |
| `finance` 渠道绑定冲突 | 409 `CONFLICT` | 409 `REVISION_CONFLICT`（状态码不变，**错误码字符串变**） | 与上一行统一；`CONFLICT` 留给 approval 包那类**状态**冲突（已执行过／重复投票） |

**前端不受影响**：`grep` 确认过，前端没有任何一处按 `CONFLICT` /
`PRECONDITION_FAILED` 分支，只显示 `message`（`ActionErrorNote`）。

## 三处发现

### 1. `alerts` 的仓储错误是**裸返回**的（真 bug）

```go
before, err := store.Get(ctx, id)
if err != nil {
    return nil, err          // ← ErrNotFound 一路裸奔到内核
}
```

`store.Get` 对不存在的 id 返回 `ErrNotFound`（有文档），但 Handler 原样往上抛，
内核见到非 `*action.Error` 就归一成 `EXECUTION_FAILED`。于是**「确认一个不存在
的告警」返回 502**，运维会当成服务端故障去查。

本包此前**根本没有 `domainError`**（另外三个包都有）。补上了。

`ErrNotFound` 取 412 是跟随本仓多数派（assurance 的「声明不存在」、credentials
的「credential_ref 尚未登记」都是这个码）。

### 2. 同一个概念，两个包给了不同的状态码

`assurance.ErrVersionConflict` 与 `finance.ErrBindingConflict` 是**同一件事**
（乐观并发的版本冲突），却一个 412、一个 409。在内核 bug 期间两者都是 502，
这个分歧看不出来。

### 3. `CodeRevisionConflict` 是死代码

它在 `errors.go` 里声明、在 `StatusForCode` 里映射到 409，但**全仓一处未用**
——`grep` 只找得到声明与映射两行。而它的名字正是第 2 点那个概念。

这是本 session 第三次遇到「声明了却到不了」（前两次是 `ttl <= 0` 与
`oldest_pending_age_seconds` 缺失判断，都被删掉了）。这一次的处置相反：
**不是删掉它，是把两处都改成用它**——因为这个概念真实存在，缺的是接线。

### 4（顺带）`finance/actions.go` 的注释过期

`default` 分支写着「由 WriteError 统一收敛成 INTERNAL 并隐藏细节」。
实际上收敛发生在**内核**（XM-KERNEL-ERRCODE0 之后），结果码是
`EXECUTION_FAILED`（502）而不是 `INTERNAL`（500）。行为一直如此，注释没跟上。
改了注释，没改行为。

## 测试

- **`alerts/domain_error_test.go`（新增 2 条）** —— 映射表；以及「认不出的
  错误必须**原样返回**、不被包成任何 `*action.Error`」（用 `errors.As` 直接
  钉住「没有被包装」，比断言某个码更准）。
- **`alerts/audit_integration_test.go`（新增 1 条 + 强化 1 条）** ——
  `TestAcknowledgeUnknownAlertIsNotAServerError` **从 Action 入口**打进来；
  跨环境闸那条原本只断言 `err != nil`，现在补上 `PERMISSION_DENIED`——
  一次安全拒绝退化成 500，运维会当成故障去查服务端。
- **`finance/binding_error_mapping_test.go`（新增 2 条）** —— 本包此前
  **没有任何**测试覆盖这张表。
- **`assurance/actions_test.go`（改 1 行）** —— 跟随 412→409。
- **`httpapi/actions_errorcode_boundary_test.go`（新增 1 例）** ——
  `REVISION_CONFLICT` 在真实 HTTP 边界上是 409。

### 变异验证（五项）与一次自我纠正

| 变异 | 结果 |
|---|---|
| `alerts` handler 退回裸返回 | **第一次没红** → 见下 |
| `alerts` 的 `ErrNotFound` 不映射 | 红 |
| `assurance` 退回 412 | 红 |
| `finance` 退回 `CodeConflict` | 红 |
| （重做）`alerts` handler 退回裸返回 | 红 |

**第一次没红这件事值得写下来。** 我先只写了直测 `domainError` 的单测——它证明
了映射函数是对的，但**证明不了 Handler 会去调它**。把 `return nil, domainError(err)`
改回 `return nil, err`（也就是这一片修的那个 bug 本身），单测全绿。

补了一条从 Action 入口打进来的集成测试之后，同一个变异当场变红。这是本
session 第三次撞上同一条：**规则存在 ≠ 调用方走得到它**。

## risks

- **`ErrNotFound` 在三个包里映射成三个码**：assurance 412、credentials 412、
  finance 渠道绑定 404（`CodeNotRegistered`）、alerts 412（本片新加）。
  多数派是 412，但 finance 那一处是 404。本片**没有统一它**——那是一个跨包
  的约定选择，值得单独一片并说清楚「域对象不存在」到底该是 404 还是 412。
- `alerts` 的 `Silence` 相关 Action 里还有几处 `return nil, err`（第 185、
  223、227、241 行附近），它们的来源多是校验与 Store 写入。本片只改了
  `store.Get` 那一处——它是明确会返回 `ErrNotFound` 的那个。其余几处**没有
  已知的错分**，但也没有逐一核实到「每个来源的错误类型」这个粒度。

## follow_ups

- **统一 `ErrNotFound` 的映射**（见 risks 第 1 条）：先定「域对象不存在」用
  404 还是 412，再四个包一起改。今天四处里三处是 412，改动面最小的方向是把
  finance 那一处也改成 412；但 404 在 HTTP 语义上更直白，值得先决定。
- `alerts` 其余 `return nil, err` 的逐处核实（见 risks 第 2 条）。
- XM-KERNEL-ERRCODE0 的 follow_up 第 3 条（把内核那条行为写进
  `docs/modules/action/README.md` 正文）仍开着。
