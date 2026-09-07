# XM-0030b-api：审批中心的服务层与 HTTP 端点

- **status:** implemented，未上线。**接好但未启用**——`cmd/platform-api/main.go`
  既没给内核注入审批网关（:217），也没给路由传 `Approvals`（:463），所以生产
  行为一个字节不变。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-0030a-wire 提交），
  基线 `588d3f4`。
- **来源：** 设计稿 `docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md` §4。
- **前两片：** XM-0030a-approval-core（`9a7fc68`）、XM-0030a-wire（`588d3f4`）。

## 改了什么

**`internal/platform/approval/service.go`（新增）** —— `Service` 把 Store 与票数
策略接成审批中心，实现 `action.ApprovalGateway`。它是**领域错误到 Action 错误码
的唯一翻译处**：内核不该知道「单还没批」和「单已过期」的区别，HTTP 层更不该。

**`internal/platform/action/approval_execute.go`（新增）** —— `ExecuteApproved`
与 `precheck`。

**`internal/platform/httpapi/approvals.go`（新增）** —— 列表 / 详情 / 投票 /
撤回 / 执行五个 handler。

**`internal/platform/httpapi/actions.go`** —— 执行端点接受 `reason`；
`APPROVAL_REQUIRED` 走 **202** 而不是错误路径。

**`internal/platform/httpapi/router.go`** —— 挂载 `/api/v1/approvals*`；
`Approvals` 为 nil 时一概不挂载。

## 三个刻意的设计决定

### 1. 没有 `POST /api/v1/approvals`（**与设计稿的清单不同，说明如下**）

设计稿 §4 的 HTTP 一栏列了四个端点，第一个是 `POST /api/v1/approvals`，但它自己
在括号里写了「**由内核代落，一般不直调**」。我没有实现它，走的正是那个括号：

建单的唯一入口是 `POST /actions/{id}/versions/{v}/execute`——一次 L2+ 调用被受理
时返回 **202 + 单号**。单独开一个建单端点，就得在 HTTP 层把权限/环境/Schema 再
判一遍，否则谁都能往审批队列里灌单；而那正是 XM-0030a-wire 挪风险闸堵掉的洞，
不该在这里重新开一个。

### 2. 执行用的参数**始终**取自单上

`ExecuteApproved` 把 `claim.Params` 交给 Handler，从不用调用方当场给的那份。
这让「批准一件小事、执行一件大事」在**结构上**不成立，而不只是靠一道校验拦着。

调用方仍可在请求体里声明 `params`：给了就必须与单上冻结的一致，让客户端能证明
自己执行的正是它看到的那份；不给表示「按单上的来」。

另有一道与调用方无关的校验：`Peek` 每次都重算**库里那份** params 的哈希并与
`params_hash` 比对。它防的是有人只改了两列中的一列——正常写路径产生不了这种行。

### 3. 占用（写 `execution_run_id`）排在校验**之后**

与 XM-0030a-wire 挪风险闸同一个道理，但后果更直接：占用是一次性的，**占用即
作废**。排在校验之前的话，一个没有权限的人一次调用就能烧掉别人等了一天的单。

顺序是：读单 → 查注册表 → 对**执行者**跑完整校验链 → 等级漂移检查 → 占用 →
跑 Handler。

反过来说，Handler 失败之后单**不会**回到 APPROVED。这也是刻意的：「至多一跑」
比「一定跑成」更重要，重跑一个高风险动作要重新过审。

## 两条路径共用一份校验序列

`precheck` 是 `Execute` 与 `ExecuteApproved` 共用的固定校验链。抽出来而不是写
两份：审批只免掉**风险闸那一项**，身份类型、环境、权限、Schema 一项不少。两份
实现迟早分叉，而分叉的方向必然是审批那条更松——它是后加的、被测得少的那条。

`TestPrecheckIsSharedBetweenBothPaths` 逐项造只坏那一项的场景，要求两条路径给出
同一个错误码。

## 写测试时抓到的两个真缺陷

**1. `PgStore.Vote` 里造了个假 principal。** 上一片写的是：

```go
approverPrincipal := principal.Principal{
    ID: d.ApproverID, Type: d.ApproverType,
    Scopes: []string{ScopeDecide},   // ← 无条件授予
}
```

它**总是**给投票人 `approval.decide`，于是 `CanVote` 的权限判定在库层形同虚设。
上一片的 `TestMissingDecideScopeRejected` 抓不到，因为那条直接测 `Policy.CanVote`，
根本不经过 Store。

改成 `Store.Vote(ctx, id, approver principal.Principal, d Decision, policy)`：
投票人的身份三项（ID / Type / Privileged）一律由 Store 按 `approver` 覆写，
**调用方说自己是谁、说自己有特权，都不算数**。

**2. Service 里加了一道永不生效的守卫。** 我写了 `ttl <= 0 就拒绝落单`，而
`TTLFor` 从不返回 0——未配的等级会退化到**最短**的那一档。那是一段读起来像
保护、实际是死代码的东西，比没有更糟。

删掉之后改为钉住**真正的保护**：漏配一个等级时，三处各自朝安全的一侧倒
（TTL 退化到最短、`Settle` 对未配票数的等级一律返回 PENDING），合起来的结局是
「落得下单、批不了、很快过期」。

## 测试

**`internal/platform/action/approval_execute_test.go`（10 条）**
主路径跑单上冻结的动作且 run id 与占用一致；调用方参数不进 Handler 只用于比对；
**执行者缺权限/无身份时不占用**（带对照组）；没接审批中心 fail closed；
Peek/Claim 的错误原样透传且分别不占用/留痕；等级漂移拒绝（带对照组）；
单上那版被下线时拒绝；Handler 错误码不被内核盖掉；单上参数不合 Schema 时拒绝；
两条路径校验一致。

**`internal/platform/approval/service_integration_test.go`（9 条）**
落单冻结参数与 TTL；漏配等级三处 fail closed（带对照组）；篡改 params_json 后
Peek 拒绝（先确认未篡改时读得出来）；投票的五类领域错误各自映射对（AI 投票、
缺 decide、重复、单号形态、不存在）；特权取自投票时的 scope；占用至多一次；
声明参数漂移不烧单（对照组用**不同键序**的同一份参数，顺带钉住哈希与键序无关）；
过期不能执行（带对照组）；撤回归属。

**`internal/platform/httpapi/approvals_test.go`（10 条）**
队列形状与票数计算；拼错的 status 拒绝而非静默不过滤（带对照组）；limit 边界；
**取满上限时说出来、没取满不说**；五个端点的 scope 门禁逐个核对；投票人取自
身份而非请求体；请求体四类非法输入；执行端点不挂 scope 门禁；声明参数透传；
没接服务时五个端点全 404（**带对照组：接上之后全部非 404**）；只接服务没接内核
时唯独执行端点 404。

**`internal/platform/httpapi/actions_approval_test.go`（2 条）**
`APPROVAL_REQUIRED` 走 202 且响应体不是错误体、reason 传到内核；
其余错误码原样走错误路径。

### 变异验证（七项，逐条跑过）

| 变异 | 期望 | 结果 |
|---|---|---|
| 占用挪到校验之前 | 四条「不该烧掉单」变红 | 红 |
| `precheck` 里跳过权限校验 | 两条路径与既有权限测试一起红 | 红（5 个用例） |
| 还原 `Vote` 里的假 principal | 「缺 decide 权限」变红 | 红 |
| 去掉 202 分支 | 202 那条变红 | 红 |
| 把 202 那一支写宽（任何错误都当受理） | 「其余错误照常报错」变红 | 红 |
| 审批路由去掉 nil 门禁 | 「没接时 404」变红 | 红 |
| （上一片）`Vote` 去掉 `FOR UPDATE` | 并发那条变红 | 红 |

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`scripts/check-governance.sh` 退出 0、新增文件 `gofmt` 干净、
`gitleaks protect --staged` 无泄漏。

## risks

- **`Service.List` 是 N+1**：为了带上每张单的票，对每个 id 单独 `Get`。
  HTTP 层因此把上限压到 100（默认 50）。审批队列本就该是个位数到几十条——
  真到了上百条说明没人在看单，那是要告警的信号，不是要翻页的信号。
- **`ExpirePending` 仍然没有任何调度器在调**。在接上之前，过期只在读取那一刻
  由 `CanExecute` 判出来，库里的 `status` 会一直停在 PENDING——队列看起来比实际
  长。XM-0030c 要补这个定时任务。
- **审批单的 params 会原样返回给有 `approval.read` 的人**。目前 Action 的参数里
  不含凭据（凭据走 CredentialRef），但**日后新增 Action 时这条要复查**：一个把
  密钥当参数传的 Action 会让密钥出现在审批队列里。

## follow_ups

- **XM-0030b-ui**：「平台治理 → 变更与审批」页——待办队列（按等级分组）、单详情
  （参数视图 + 理由）、投票/驳回、执行按钮。导航禁用位已预留。
- **XM-0030c**：`PENDING > 4h` 告警规则 + Runbook（含「审批人失联怎么办」）+
  `ExpirePending` 的 River 定时任务。
- **启用是单独一片，要先问产品负责人**（登记进晨间清单）：给
  `cmd/platform-api/main.go` 注入 `approval.NewService(...)`。它同时修掉一个缺陷
  （`registry.connection.set_status` 与 `registry.connector.create` 声明 L2 却
  执行不了），但也会创建一个**需要有人看**的队列，所以要排在 XM-0030c 的告警
  规则之后。
- **再之后**：cards/sms/registry 里被迫降级成 L1 的 Action 恢复正确等级。那是
  行为变更（从「谁都能跑」变成「要人批」），也要负责人点头。
- 新增 `approval.read` / `approval.decide` / `approval.l4` 三个 scope 需要进
  RoleScopeMap 才有人拿得到。`approval.l4` 的首批持有人是设计稿 §7 的待拍板项。
