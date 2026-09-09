# XM-RISK-RESTORE：把被迫降级的 Action 恢复成设计上正确的风险等级

> ## 🚫 本片不能单独上线：必须与 XM-ACTION-REASON 同批发布
>
> **单独上线会让这 6 个动作在管理端直接不可用**——不是「体验变差」，是点了就报错。
> 两个已核实的洞（由并行的普查 agent 实测，本片未碰前端、未验证，照录）：
>
> 1. **前端从不发 `reason`。** `web/apps/admin-web/src/api/platform.ts:252-254`
>    的统一执行函数请求体只有 `{ params }`，全文件 `grep -c reason` = 0。
>    而内核 `internal/platform/action/kernel.go:219-224` 对 L2+ **要求非空
>    `reason`**，否则 `INVALID_PARAMS`。开卡 / 关停 / 提现 / 买号 / 租号 /
>    买邮箱这 6 个，改完之后在界面上点了就报错。
> 2. **前端不认 202。** L2+ 走 202 + `approval_request_id`，而
>    `platform.ts:257` 只找 `action_run_id`，拿不到就返回 `runId: ""`——界面
>    会把「已受理为审批单 X」显示成一次 run_id 为空的**成功**。全前端零处理 202。
>
> 这两处由 **XM-ACTION-REASON** 修（另一个 agent），本片按分工**一个 `web/`
> 文件都没碰**。发布顺序上两片是一个原子单位：
>
> - 先上 XM-ACTION-REASON 再上本片 —— 安全（前端先学会发 reason、认 202，
>   此时 6 个动作还是 L1，多发一个 reason 字段无害）。
> - 先上本片 —— **6 个动作当场不可用**。
> - 同批上 —— 可以。
>
> **后端这道闸本身是有测试的**（`action/kernel_approval_test.go` 的
> `TestApprovalRequiresReason`：L2 缺 reason → `INVALID_PARAMS` 且不落单）。
> 缺的是**另一半**——没有任何用例说过「L0/L1 不受这道闸约束」，本片已补
> （见「测试」一节）。跨前后端那一段仍然没有测试保护，属 XM-ACTION-REASON 的面。

> ## ⚠ 这是行为变更，上线前需要产品负责人点头
>
> **六个 Action 从「有权限就能直接跑」变成「要先落一张审批单、由人批准后再执行」。**
> 其中提现这一个还要求**两个不同的人**——提交人自己批不了自己的提现。
>
> | Action | 之前 | 现在 | 谁的日常会变 |
> |---|---|---|---|
> | `cards.card.issue`（开卡） | 直接执行 | 落单，一票即可（可自批） | 运营开卡 |
> | `cards.card.delete`（关停卡） | 直接执行 | 落单，一票即可（可自批） | 运营关卡 |
> | `cards.withdraw.execute`（提现） | 直接执行 | **落单，两票且审批人≠提交人** | fund-operator |
> | `sms.number.purchase`（买号） | 直接执行 | 落单，一票即可（可自批） | sms-operator |
> | `sms.rent.purchase`（租号） | 直接执行 | 落单，一票即可（可自批） | sms-operator |
> | `sms.email.purchase`（买邮箱） | 直接执行 | 落单，一票即可（可自批） | sms-operator |
>
> **上线前必须确认的两件事**（否则这几条路会当场断掉）：
>
> 1. **`approval.read` / `approval.decide` 已经发到人手上。** 它们在
>    `DefaultRoleScopeMap` 的 admin 一档里，但 fund-operator 与 sms-operator
>    这两个专门角色**不含**它们。持 `fund.withdraw` 却不持 `approval.decide`
>    的人，提得起提现、但没人批得了——除非他同时是 admin。
> 2. **提现要有第二个人。** L3 要两票且不许自批。今天如果只有一个人同时是
>    admin 和 fund-operator，**提现在实践上是做不成的**：他提得起单，但凑不
>    出第二票。这不是缺陷，是 L3 的定义；但它是不是此刻想要的，得负责人拍。
>
> 买号这一条的日常摩擦最大（一个号几毛到几块，却要走一遍落单—批准—执行），
> **最有可能被负责人要求退回 L1**。退回只需改一处 `RiskLevel` 与对应契约，
> 测试断言的是行为不是字面等级，退回后 `TestHumanPurchaseActionsLandAsApprovalRequests`
> 会变红——那正是它该做的事，删掉对应子用例即可。

- **status:** implemented，**未提交、未上线**（按派工要求改完即停，不 commit/push）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `e673aef`。
- **前置：** `5df1541`（XM-0030-ENABLE 启用审批中心）。没有它这一片一行都不能做。
- **来源：** `docs/handoffs/slices/XM-0030c.md` 的 follow_ups 最后一条——
  「cards/sms/registry 里被迫降级成 L1 的 Action 恢复正确等级。那是行为变更」。

## 为什么现在能做

审批中心实装并在 platform-api / platform-worker 两端注入之后
（`action.WithApprovalGateway(approvalService)`），内核对 L2+ 不再是
`ADVANCED_CONTROLS_REQUIRED`（501 拒绝），而是受理成一张审批单（202 + 单号）。
`approval.DefaultPolicy()` 对三档都有真实规则，**L4 也不是空壳**：

| 等级 | 票数 | 可否自批 | 特权票 | 有效期 |
|---|---|---|---|---|
| L2 | 1 | **可** | 否 | 24h |
| L3 | 2 | 否 | 否 | 24h |
| L4 | 2 | 否 | **需 1 张 `approval.l4`** | 4h |

「L2 可自批」这一行是本片好几个判断的支点——**L2 不等于第二人审批**。

## 逐个 Action 的判定与依据

判定只用仓库里写下来的话，不用直觉。下面每一行的「依据」都能在仓库里指出。

### 改了的（6 个）

**`cards.card.issue` L1 → L2**
依据是这个 Action 自己的注释与
`contracts/actions/cards.card.issue.v1.json` 的 notes **两处都写了同一句**：
「Foundation-B 落地后应重估——**届时升到 L2** 才是真的加了控制」。等级由原文
指定，不是我挑的。ADR-003 的 L2 = 「预览 + 幂等 + 写后确认 + 完整审计」。

**`cards.card.delete` L1 → L2**
`funds_actions.go:262` 原注释：「声明成 **L2** 会让它变成永远跑不起来的摆设」
——被排除的那一级就是 L2。契约 notes 同句。动作性质是不可逆关停
（`compensation_mode: NOT_POSSIBLE`），对应 ADR-003 L2 的「启停资源」。

**`cards.withdraw.execute` L1 → L3**（**唯一一个我选了比原文更高一级的**）
- 原注释的字面反事实是 L2（「声明成 L2 会让提现变成永远跑不起来的摆设」）；
- 但同一段注释说缺的是「**平台今天没有第二人审批这道闸**」，而按上表
  **L2 一票且允许自批，给不了第二人审批**——恢复成 L2 会把那句注释删掉、
  却把它描述的窟窿原样留着，比不改更糟；
- `docs/handoffs/slices/XM-0030a-approval-core.md:16-17` 把这一行连同
  `funds_actions.go`、`sms/actions.go` 一起列为「被迫把**本该 L2/L3** 的
  Action 声明成 L1」——L3 在这份文档给的区间内；
- 宪法条款 9：「L3/L4 必须审批」。

**`sms.number.purchase` / `sms.rent.purchase` / `sms.email.purchase` L1 → L2**
`sms/actions.go:62` 原注释：「声明成 **L2** 会让它们变成永远跑不起来的摆设。
这意味着平台今天没有第二人审批这道闸——**买号的护栏**是权限、未决防重、
台账与页面确认，不是审批流」。那段话点名的就是买号这一类；被排除的级别是 L2。
三个都只给人（`humanOnly`）、都用 `sms.purchase` 这把「花真钱」的钥匙。

### 没改的（逐条说明为什么）

**`sms.number.request`（要号）—— 花的是同一笔钱、用的是同一把权限，仍然 L1。**
这是本片最要紧的一条判断，也是最容易改错的一条。它按设计对**机器身份**开放
（XM-SMS4 #2，`PrincipalTypes` 含 SERVICE），而机器凑不出审批人；这条链路上
花钱的闸是**消费者配额**（`sms.quota.set`——「配额不花钱，但决定一个机器一天
最多能花多少」，未登记的机器一次都要不到号）。把它一起抬到 L2 会让无人值守
的要号链路整条停摆。已在 `actions_request.go` 就地写明理由，并有正面用例
钉住（见下）。

**registry 的三个 Action —— 本来就是对的，一个字没动。**
派工里说「registry 里也有」，普查下来不是：`registry.connector.create` /
`registry.connection.set_status` 是 L2、`registry.connection.create` 是 L3，
它们从来没被降级过，只是在 Foundation-A 下**声明了却执行不了**
（这正是 XM-CARD0 handoff 里那条「建议核一下这两个 Action 是否实际可用」）。
启用审批中心之后它们自然可用，`registry/l2_enabled_integration_test.go` 已经
钉住。只把一句过期的段注释（「L2：Foundation-A 下会被内核拒绝」）改了措辞。

**assurance / credentials / localauth 的「全部 L1」—— 不是降级，不动。**
这三处的「全部 L1」后面跟的是**正面依据**（`ADR-019 决策·三`、
「修改低风险平台配置，ADR-003」），不是「因为内核跑不动」。与本片要恢复的
那一类不是一回事。

**cards 的 topup / redeem / freeze / unfreeze / usage.set / reveal，
withdraw 的 address.register / limit.set / address.set_enabled —— 保持 L1。**
它们没有任何「本该更高」的注释或契约记载，也没有对应的契约文件。按纪律
「找不到依据的保持原样」。

### 等负责人裁定

**提现是 L3 还是 L4？** ADR-003 的等级表把「**退款**」这类钱出去的动作放在
L4，而提现是平台里唯一一个把钱转到**平台之外**、不可逆也不可追回的动作，
按字面读它够得上 L4。没有直接定成 L4 的理由是：L4 需要一张 `approval.l4`
特权票，而该 scope 按设计**默认不发给任何人**（`oidcauth/rolemap.go` 的
`approval-l4` 角色，注释写明「首批特权票仅产品负责人」）。在有人持有它之前，
L4 的提现单会一直停在 PENDING 直到 4 小时后过期——**等于提现下线**。
这是「谁来持特权票」的授权决定，不是本片能替负责人做的判断。
改成 L4 只需改一处等级 + 契约，并指定持票人。

**买号是否值得这道摩擦？** 见文首。

## 顺带修的一处（不修的话本片会造出一个可见的假象）

> **归属确认**：`internal/platform/httpapi/actions.go:136-141` 与
> `router.go:270` 那处 `blocked_reason` 改动**是本片改的**（2026-09-07），
> 不是第三方改动。下面是原委。

`internal/platform/httpapi/actions.go` 的 `ListActionsHandler` **无条件**地对
每个 L2+ Action 回 `executable: false` + `blocked_reason: "需要 Action
Advanced Controls（Foundation-B）"`，它不知道审批中心接没接。这个串会被
`web/apps/admin-web/src/pages/ActionsPage.tsx` 原样显示。

本片把六个 Action 抬到 L2/L3 之后，操作台就会在**提现和开卡旁边写「功能待
上线」**——而它此刻是能用的，只是要先过审批。这是本片直接造出来的错误信息，
所以就地修了：handler 多收一个 `approvalsWired`，接了审批中心时改说
「需要审批：提交后落一张审批单，批准后由人触发执行」。

**`executable` 的布尔语义一个字没动**（L2+ 仍然是 `false`，因为它确实不能
**直接**执行），只改了解释文案——刻意不碰前端的可用性判断，那是 XM-0030b-ui
的地盘，而且此刻有另一个 agent 在同一个 worktree 改前端。
`router_test.go` 断言的是「原因非空」而不是具体文案，未受影响。

## 关于契约文件（**超出了派工的「只动后端 Go 文件」，请复核**）

派工写的是「只动后端 Go 文件」。我另外改了 6 个
`contracts/actions/*.v1.json` 的 `risk_level` 与相应 notes，理由是它们是**同一
个事实钉在两处**：契约里写 `"risk_level": "L1"` 而代码执行 L2/L3，比两边都
不改更糟——而且 XM-CARD0 的 handoff 正是拿契约当权威在引用它
（「两份契约声明为 L2」）。仓库没有任何代码或门禁读 `contracts/actions/`，
所以这 6 个文件纯属文档一致性，**要退回只需 `git checkout` 这 6 个文件**，
不影响任何测试。

`contracts/` 不在派工的禁改清单里（那份清单全是前端文件），前端 agent 也不
碰它，所以没有撞车风险。

## 改了什么

**风险等级（6 处）** —— `cards/actions.go`（开卡）、`cards/funds_actions.go`
（关停）、`cards/withdraw_actions.go`（提现）、`sms/actions.go`（买号）、
`sms/actions_extras.go`（租号、买邮箱）。

**注释重写** —— 旧注释说「Foundation-B 未实现所以只能 L1」，那句话现在是错的，
留着比删了更糟。每一处都改成「为什么是这个等级」，并写清哪些护栏是等级之外
的（幂等键、金额上限、地址白名单、页面二次确认——**一道没减**）。
另外四处过期表述一并修正：`action/risk.go` 的
`RequiresAdvancedControls` 文档（「返回 true 不等于拒绝执行」）、
`approval/approval.go` 的包注释、`oidcauth/rolemap.go` 的 fund-operator 段、
`registry/actions.go` 的段注释。

**`sms/actions_request.go`** —— 新增注释，写明要号**刻意**停在 L1 以及理由，
免得下一个人把它当成「漏改的一个」。

## 测试

**`cards/risk_levels_test.go`（新增 3 条）**
- `TestMoneyMovingCardActionsLandAsApprovalRequests` —— 开卡/关停/提现三个
  **一起点名**（本仓库栽过「只修撞见的那一处」的跟头），断言拿到
  `APPROVAL_REQUIRED`、真的落了单，且单上冻结的是这一次调用的 action 与理由。
- `TestRevealStillExecutesWithoutApproval` —— 反面：没被碰过的 reveal 仍然
  一步跑完并**返回卡面**（断言执行成功这件正事，不只是「没落单」）。
- `TestWithdrawRequiresASecondPerson` —— L3 与 L2 的实际差别。三段：提交人
  投自己的单拿 `ErrApproverIsRequester`；**别人投得了**（否则上一条可能只是
  因为这张单谁都投不了，会以错误的理由绿）；一票 `Settle` 仍是 PENDING、
  两票才 APPROVED。

**`sms/risk_levels_test.go`（新增 3 条）**
- `TestHumanPurchaseActionsLandAsApprovalRequests` —— 买号/租号/买邮箱，
  三个分散在两个文件里，一起点名。
- `TestMachineNumberRequestStillExecutesWithoutApproval` —— **本片最要紧的
  反面用例**：用机器身份要号，断言它真的跑出了 `succeeded` 结果且没落单。
  用例里先给这台机器登记配额——那正是这条链路上花钱的闸。
- `TestRoutingRuleStillExecutesWithoutApproval` —— 配置类没被改过头。

**`cards/audit_test.go`** —— 开卡的两条审计用例原先直接 `Execute`，现在开卡
是 L2，走不到 Handler 了。加了 `fakeApprovals`（审批中心那三个方法的替身）
与 `runThroughApproval` 辅助函数，让它们走完「落单 → 执行」再断言 Handler 的
审计贡献。原有断言一个字没改——把落单那一段的审计事件清掉，断言仍对着**执行**
那一条。

**删掉了 `TestIssueActionRiskLevelIsExecutableToday`。** 它断言开卡的等级不需
要 Advanced Controls，是「平台只跑得动 L1」那个天花板的化身。它自己的注释就
写了「Foundation-B 落地后它会自然失效，那正是重估风险等级的时机」——时机到
了。原地留了一段说明，指向新的行为用例。

### 变异验证（9 项，逐条把实现改回去再跑）

| 变异 | 期望 | 结果 |
|---|---|---|
| 开卡 L2 → L1 | `LandAsApprovalRequests` 变红 | 红 |
| 开卡 L2 → L1 | 审计用例（走审批路径那条）变红 | 红 |
| 关停 L2 → L1 | `LandAsApprovalRequests` 变红 | 红 |
| 提现 L3 → L1 | `LandAsApprovalRequests` 变红 | 红 |
| 提现 L3 → L1 | `RequiresASecondPerson` 变红 | 红 |
| **提现 L3 → L2** | **`RequiresASecondPerson` 变红** | **红** |
| 提现 L3 → L2 | `LandAsApprovalRequests` 仍绿（L2 照样落单） | 绿（符合预期） |
| reveal L1 → L2 | `RevealStillExecutes` 变红 | 红 |
| 买号 L2 → L1 | `HumanPurchase…` 变红 | 红 |
| 租号/买邮箱 L2 → L1 | `HumanPurchase…` 变红 | 红 |
| **要号 L1 → L2** | **`MachineNumberRequestStillExecutes` 变红** | **红** |
| 路由规则 L1 → L2 | `RoutingRuleStillExecutes` 变红 | 红 |

两条加粗的最要紧：

- **提现 L3 → L2 变红**，说明 `RequiresASecondPerson` 真的分得清 L3 与 L2，
  而不是「只要落单就绿」。这条是「提现为什么不是 L2」的全部证据。
- **要号 L1 → L2 变红**，说明那两条「仍然直接执行」的**缺席型断言不是恒真的**
  ——它们是本片「别改过头」的唯一防线，恒真的话等于没写。

没有一条新断言是钉字面等级的（`RiskLevel == "L2"` 这种写法一条都没有）。

### reason 那两条的变异验证（两个方向都做了）

| 变异 | 期望 | 结果 |
|---|---|---|
| 把 reason 闸改成恒不触发 | `RejectMissingReason` 与既有 `RequiresReason` 变红 | 红 |
| 同上 | `LowRiskLevelsDoNotRequireReason` **不受影响**仍绿 | 绿 |
| **把 reason 闸提到风险闸之外（对所有等级生效）** | **`LowRiskLevelsDoNotRequireReason` 变红** | **红** |

第三行是这条用例的全部价值——它证明「L0/L1 不要 reason」这个断言真的会在有人
把校验加到所有等级上时报警，不是恒真的。

**一处工具教训**：第一次做变异时我把整个 `if` 段删掉，结果 `strings` 成了未使用
的 import，**整个包编译不过**，于是所有用例都"红"——包括本不该受影响的那条，
差点被读成一个假信号。`kernel.go` 里 `strings.` 只出现这一次。
删代码做变异时先确认它不会顺带弄坏编译；改条件（`if false && …`）比删更安全。

**`action/kernel_reason_test.go`（新增 2 条，验收线要求后补）**
- `TestLowRiskLevelsDoNotRequireReason` —— **本文件存在的理由**：L0/L1 缺
  `reason` 仍然照常执行（Handler 被调用、返回值对、审计记成功），且**网关是
  接着的**，证明「不要 reason」不是因为没接审批中心，而是这个等级本来就不过
  风险闸。在这条之前，「把 reason 校验误加到所有等级上」这个改动是绿的，
  而平台上绝大多数写操作是 L0/L1、调用方从来不发 reason。
- `TestApprovalLevelsRejectMissingReason` —— L3/L4 与 L2 一样要 reason，
  且被拒要留痕、错误话里要点名 `reason`（缺的是一个**不在 Schema 里**的字段，
  笼统的「参数不符合 Schema」会让人去翻 Schema 而翻不到）。

  **一处更正**：我先前报「全仓库没有任何测试钉住 L2+ 缺 reason」，那是**错的**
  ——`TestApprovalRequiresReason` 一直都在，是我的 `grep` 模式没命中（它用循环
  变量，字面量是 `""` / `"   "` / `"\t\n"`）。所以「契约裸着才让前端漏发漏到
  今天」这个因果也不成立：后端有闸也有测试，漏的是跨前后端那一段。
  本文件因此只保留与既有用例**不重复**的部分，L2 空白串那几条没有再写一遍。

### 这些测试不依赖「前端能调通」（已核对）

本片全部 6 条新用例都**直接构造 `action.Request` 打内核**，不经过 HTTP 层、
更不经过前端：没有 `httptest`、没有 `NewRouter`、没有 `platform.ts` 那条路。
所以文首那两个前端洞**不会**让本片的测试假绿或假红。

但反过来也要说清楚——**这正是这些测试为什么抓不到那两个洞**：

- 用例里 `Reason` 是**显式填的**（`Reason: "把结余提回公司钱包"` 之类）。
  内核那道 `reason` 非空闸因此被满足，而真实前端一个字都不发。
  测试与界面的差别就在这一个字段上。
- 用例断言的是内核返回的 `CodeApprovalRequired` 与 `ApprovalRequestID`，
  不是 HTTP 202 的响应体形状——前端认不认 202 在这一层看不见。

换句话说：本片的测试证明了**后端行为是对的**，没有、也不打算证明**这条路
在界面上走得通**。后者是 XM-ACTION-REASON 的验收面。

## 门禁

- `XM_TEST_DATABASE_URL` 已设（`scripts/dev/worktree-testdb.sh --print-url`）；
  `go test -p 1 -count=1 ./...` **退出 0，全绿**（`-p 1` 没漏，共用测试库）。
- `go vet ./...` 退出 0。
- `bash scripts/check-governance.sh` 退出 0。
- 前端未跑：本片一个 `web/` 文件都没碰，且此刻有另一个 agent 正在同一个
  worktree 改前端（`workbench.ts` / `OverviewPage.tsx` / `router.test.tsx` 等
  5 个 `web/` 文件是**他的**未提交改动，不属于本片）。

  **提交时只取本片的 24 个文件**：改动 20 个（6 个 `contracts/actions/*.json`
  + 14 个 `internal/platform/**.go`）、新增 4 个
  （`cards/risk_levels_test.go`、`sms/risk_levels_test.go`、
  `action/kernel_reason_test.go`、本文件）。`web/` 下的一个都不要带。

## risks

- **最大的风险是权限而不是代码**：`fund-operator` 与 `sms-operator` 都不含
  `approval.decide`。这两个角色的持有人如果不同时是 admin，改完之后他们
  **提得起单但没人批**。见文首第 1 条。
- **提现在只有一个操作者时会做不成**（L3 不许自批）。见文首第 2 条。
- **已经批准但尚未执行的单不受影响**，因为 L1 从不落单，改之前不存在这几个
  Action 的存量单。但**日后再调等级要当心**：`ExecuteApproved` 会比对
  `claim.RiskLevel != def.RiskLevel` 并拒绝执行（「请重新提交审批」），
  所以调等级会让**队列里所有该 Action 的在途单作废**。下次改之前先看队列。
- 六个 Action 的调用方现在会拿到 **202 + 单号**而不是执行结果。后端
  `httpapi/response.go:43` 已有 `APPROVAL_REQUIRED → 202` 的映射。
  **前端两处都没接上（缺 `reason`、不认 202），已核实——见文首，
  这是本片最高一级的风险，也是它不能单独上线的原因。**

## follow_ups

- **XM-ACTION-REASON 必须与本片同批上线**（前端补 `reason` + 认 202）。
  见文首。这是发布上的硬依赖，不是「最好也做一下」。
- ~~补 reason 的后端用例~~ —— **已做**（`action/kernel_reason_test.go`，
  验收线 2026-09-07 指示）。两侧都盖、两个方向都做了变异验证。
- **产品负责人裁定**：提现 L3 还是 L4（连带 `approval.l4` 首批持有人）；
  买号那三个是否值得这道摩擦。
- 给 `fund-operator` / `sms-operator` 补 `approval.decide`，或明确「这两个角色
  的人同时是 admin」是长期假设。
- 前端逐页确认 202 + 单号这一支的落地（属 XM-0030b-ui 的面）。
- `cards` 的 topup / redeem 与 withdraw 的 limit.set / address.register 三处
  值得**下一次专门评估**：它们都动钱或动「钱能去哪儿」，今天保持 L1 只是
  因为仓库里没有写下任何依据，不是因为评估过风险不高。
