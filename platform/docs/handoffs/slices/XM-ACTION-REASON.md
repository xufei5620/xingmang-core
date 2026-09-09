# XM-ACTION-REASON：管理端的 L2+ 执行通道（reason + 202）

- **status:** implemented，未提交（按指示不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `197d0bb`
  （`feat(action): 六个高危动作恢复设计风险等级，不再被迫停在 L1`）。
- **只动了 `web/` 下的文件。** `internal/` 与 `cmd/` 一行未改，全程只读。

> **本片必须与 XM-RISK-RESTORE（`197d0bb`）一起上线。**
> 两者互为对方的前提：只上 RISK-RESTORE，六个按钮点了就报
> `INVALID_PARAMS`（前端不发 `reason`）；只上本片，六个动作还是 L1、
> 走的仍是同步执行分支，改动不产生任何可见效果但也不会坏。
> **一起上线之外的任何顺序都会让管理端出现坏按钮或假回执。**

## 一、先核准的事实（都是自己读的，不是照抄 brief 的行号）

### 1. 内核对 L2+ 的 reason 要求

`internal/platform/action/kernel.go`：`Request.Reason` 字段注释写明
「L0/L1 可空；**L2 及以上必填**」。风险闸里：

```go
if strings.TrimSpace(req.Reason) == "" {
    return fail(CodeInvalidParams,
        fmt.Sprintf("action %s 风险等级 %s 需要审批，必须提供 reason", def.ID, def.RiskLevel), nil, p)
}
```

顺序上它排在环境/权限/Schema 之后、`approvals.Submit` 之前——也就是说**权限
不足的人拿到的是 `PERMISSION_DENIED` 而不是「理由没填」**，前端不必替它排序。

### 2. 202 的响应体（逐字，`internal/platform/httpapi/actions.go`）

```go
type approvalRequiredResponse struct {
    ApprovalRequestID string `json:"approval_request_id"`
    Status            string `json:"status"`
    Message           string `json:"message"`
}
```

写出的位置：`errors.As(err, &ae) && ae.Code == action.CodeApprovalRequired` 时
`WriteJSON(w, http.StatusAccepted, …)`，其中
`Status: string(action.CodeApprovalRequired)`。

`action.CodeApprovalRequired` 的字面量在
`internal/platform/action/errors.go:38`：**`"APPROVAL_REQUIRED"`**。

请求体是 `httpapi.executeActionBody`：`{"params": …, "reason": …}`，用
`DisallowUnknownFields` 解析——**只有这两个字段，多一个就是 400**。

200 的响应体是 `executeActionResponse`：`action_run_id` + `result,omitempty`。
两个结构体的字段互不相交，这是前端能靠 body 形状分辨的依据（`ApiClient.post`
只交出 body，状态码留在它里面）。

### 3. 后端已经分两种措辞（本片要跟上的那处）

`httpapi.ListActionsHandler(reg, approvalsWired)`：
`approvalsWired` 为真时 `blocked_reason = "需要审批：提交后落一张审批单，批准后由人触发执行"`，
否则才是 `"需要 Action Advanced Controls（Foundation-B）"`。

## 二、完整调用点清单

`grep -rn executeAction web/` 命中 ~80 处，但**受影响的只有 `197d0bb` 恢复成
L2+ 的那 6 个 Action**。逐个确认（契约 + Go 注册面 + 前端包装 + 界面入口）：

| Action | 等级 | api 包装 | 界面入口（按钮） |
| --- | --- | --- | --- |
| `cards.card.issue@1` | L2 | `api/cards.ts` `issueCard` | `components/CardsShared.tsx` `IssueCardDialog`「确认开卡」 |
| `cards.card.delete@1` | L2 | `api/cards.ts` `deleteCard` | `components/CardWorkbench.tsx` `CardActions`「关停」两步 |
| `cards.withdraw.execute@1` | **L3** | `api/withdraw.ts` `executeWithdraw` | `components/WithdrawPanel.tsx` `WithdrawForm`「提现」两步 |
| `sms.number.purchase@1` | L2 | `api/sms.ts` `purchaseSMSNumbers` | `components/SMSPanel.tsx` `PurchaseDialog`「买号」两步 |
| `sms.rent.purchase@1` | L2 | `api/sms.ts` `rentSMSNumber` | `components/SMSExtrasPanels.tsx` `HeroRentPanel`「下一步」两步 |
| `sms.email.purchase@1` | L2 | `api/sms.ts` `purchaseSMSEmails` | `components/SMSExtrasPanels.tsx` `EmailPurchaseDialog`「下一步」两步 |

**清单是穷尽的**，判据是：以上 6 个 action id 在 `web/` 下
（`grep -rn 'cards.card.issue|cards.card.delete|cards.withdraw.execute|sms.number.purchase|sms.rent.purchase|sms.email.purchase' web/`）
只出现在这 6 个包装函数里，每个包装函数各自只有一个界面调用点。

**改前 / 改后**（六处一致）：

- **改前**：请求体只有 `{params}`，内核回 `INVALID_PARAMS`；即便补上 reason，
  202 的 body 里没有 `action_run_id`，`executeAction` 返回 `{runId: ""}`，
  界面显示成绿色的「已提交 X 请求 · 这次响应里没有 action_run_id」。
- **改后**：界面上多一格**必填的「理由（提交审批用）」**；提交走
  `submitAction`，落单时回执是橙色的「X 已提交审批，<那件事>还没有发生 ·
  审批单号 ap-… · 去「操作与审批 · 待审批」」，且回执里不出现 run_id。

`assuranceProbes / alerts / connectors / credentials / finance / platformChannels /
savedViews / server / staff / totp` 以及 cards、sms、withdraw 里其余的包装
**一律不动**：它们仍是 L0/L1，请求形状与本片之前逐字一致。

## 三、改了什么

### 执行通道 `web/apps/admin-web/src/api/platform.ts`

新增 `submitAction()`，返回**可辨识联合**：

```ts
export type ActionOutcome =
  | { kind: "executed"; runId: string; result: unknown }
  | { kind: "approval_pending"; approvalRequestId: string; message: string };
```

- 请求体 `{params, reason}`；**没给 reason 时不发这个字段**（L0/L1 的请求形状
  不变）。
- 202 分支的判据是 `approval_request_id !== undefined || status === "APPROVAL_REQUIRED"`
  ——认单号**或**状态字面量：单号万一是空的，`status` 仍然说得清这不是一次执行。
- 导出 `APPROVAL_REQUIRED_STATUS = "APPROVAL_REQUIRED"`（照抄 `errors.go`）。

`executeAction()` 保留原签名（`Promise<ActionRun>`），改为 `submitAction` 的
薄封装，**拿到审批受理时抛 `ApprovalRequiredError`**。

> **为什么不把 80 个调用点全改成联合类型**：那会让本片的差异铺到 ~40 个包装
> 函数和它们的页面调用方，与另外两个 agent 正在改的文件大面积重叠。折中是
> 「窄接口 + 全局 fail loud」：需要处理审批的 6 处显式用 `submitAction`，
> 其余 74 处继续用 `executeAction`，而**一旦哪个 L0/L1 动作以后被提级，它
> 会当场抛错，不会再悄悄变成 `runId: ""` 的假成功**——那个哨兵在全仓已经
> 不存在第二个产生点了。

### 回执 `components/ActionResultNote.tsx`

`ActionResult` 从 `{title, runId}` 改成两个类型的联合
（`ActionExecutedResult` / `ActionApprovalPendingResult`），靠
`"approvalRequestId" in result` 收窄。~20 处既有生产者是
`{title, runId}` 的对象字面量，仍然匹配第一支，**一处都没动**。

审批那一支用 warning 而不是 success 配色（颜色也是文案的一部分），给单号、
给去 `/actions?sub=pending` 的链接、给「批准之后要在那一页由人点执行」，
并原样附上服务端那句 `message`。

新增 `actionResultOf(outcome, {executed, approvalPending})`：两个标题**必须由
调用点分别写**，这一层不按模板拼——让一个不知道业务的函数措辞，就是把
「事情有没有发生」交给它决定。

### 理由输入 `components/ApprovalReasonField.tsx`（新增）

六处共用。硬规矩写在文件头：**不预填、不给候选、不自动生成**——一句拼出来的
套话会让审批退化成盖章。空/过短时前端拦住并说明原因（`MIN_APPROVAL_REASON_LENGTH = 3`，
是下限不是标准），但注明前端拦截不是安全控制，服务端仍会判。

### 顺带修的三处假话

1. `pages/ActionsPage.tsx`：`foundationAReady` → `directlyExecutable`，
   「（全部待 Foundation-B）」→「（全部先落审批单）」，脚注去掉
   「L2/L3/L4 在 Foundation-A 阶段恒为不可执行」，改成「不能被直接执行 → 先落
   审批单 → 批准后在待审批由人触发」，并指出**逐条的真实原因由后端给
   （`blocked_reason`），本页不猜**。
2. `WithdrawPanel`：按钮 title「再点一次将真的发起转账」→「提交后先落审批单，
   批准并由人执行之后才会真的转账」；按钮文案「确认提现 X」→「提交提现审批 X」。
3. `CardWorkbench`：「再点一次将真的关停这张卡」→ 同上口径；
   「确认关停」→「提交关停审批」。

其余四处按钮文案同样从「确认买 N 个号」改成「提交买 N 个号的审批」等。

## 四、变异验证

每一条都是「把实现改回去 → 跑 → 确认真的红 → 改回来」，不是推断：

| # | 变异 | 结果 |
| --- | --- | --- |
| 1 | `submitAction` 删掉 202 分支（回到 `runId` 空串哨兵） | **9 条红**（整链 3 条回执 + 4 条 api 断言 + 2 条形状断言） |
| 2 | 请求体改回 `{params}`（不发 reason） | **7 条红** |
| 3 | `ActionResultNote` 的审批分支恒不进入 | **3 条红**（三条渲染回执的整链测试） |
| 4 | `isApprovalReasonUsable` 恒真（等于没有前端拦截） | **4 条红**（三条「一个请求都不发」+ 关停那条） |
| 5 | `ActionsPage` 措辞改回「（全部待 Foundation-B）」+ 旧脚注 | **3 条红**（含那条缺席型断言） |
| 6 | `executeAction` 回到 `return {runId: "", result: undefined}`（不再抛） | **3 条红**（api 层 1 条 + 两种错误处理形态各 1 条） |
| 7 | 给 L1 的「登记提现地址」对话框硬塞一个理由框 | **1 条红**（证明 `within(dialog).queryByLabelText("理由")` 不是恒真） |

变异 5 专门验证了缺席型断言：`ActionsPage.test.tsx` 里
「页面上不再出现 Foundation-B / Foundation-A 阶段那两句旧话」在旧措辞下确实
变红，而不是因为页面空着而恒真——它前面先 `await` 了一条正向断言的元素。

**旧实现下会不会照样绿**：整链那三条断言的是
`{kind, approvalRequestId, title: "…已提交审批，X 还没有…"}` 与
「审批单号 ap-…」这段渲染文本，旧实现（`{runId: ""}` + 只有 run_id 那一支的
回执）一个都产出不了；变异 1 的实测结果就是这条判断的证据。

## 五、测试

**`components/ApprovalSubmitFlow.test.tsx`（新增，12 条）** —— 本片的主力。
**一个 api 模块都不 mock**，替身只放在 `globalThis.fetch`，链条是
`按钮 → 组件 → api 包装 → submitAction → apiClient → fetch`。
六个按钮**每一个**都从界面打进去走完整条链，断言：请求 URL 与 action id、
请求体里 `reason` 与 `params` 平级（`params` 里没有 reason）、202 之后的回执
文案与单号、以及「回执里不出现 run_id」。另有 3 条「理由为空时一个请求都不发」。

> 这一档存在的理由是仓库那条纪律：规则与调用它的地方是两段代码时，只在规则
> 那一侧造假响应会让判定恒真。所以这里没有「在 platform.ts 单测里造一个 202」
> 就收工。

同一个文件里另有 **2 条**钉住「L0/L1 调用点意外收到 202」的行为——那是
`executeAction` 改成抛错这个设计的**全部价值所在，所以必须有测试守着**
（见下一节的调用点普查）。两条分别覆盖管理端仅有的两种写错误处理形态：

- 形态 B（`AcknowledgeAlertButton`，`alerts.alert.acknowledge@1`）：断言
  服务端原话被逐字显示在 `role="alert"` 里、成功回调**没有**被调用、
  按钮既不 `disabled` 也不带 `aria-busy`（**两个都查**——只查 disabled 的话，
  一个「能点但转圈不停」的界面照样绿）。
- 形态 A（`WithdrawPanel` 的「登记地址」，`cards.withdraw.address.register@1`）：
  同一屏上 L3 有理由框、L1 没有（缺席断言用 `within(dialog)` 限定，因为
  L3 表单的理由框就在同一棵树上）；收到 202 时报错，且**两种回执一条都不出**
  ——审批回执只该长在准备好了的调用点上。

**`api/platform.test.ts`（+6 条）** —— `submitAction` 的两种结局、空单号但
`status` 说了话时仍算受理、reason 发与不发的请求体形状、以及
`executeAction` 拿到受理时抛 `ApprovalRequiredError`。

**`pages/ActionsPage.test.tsx`（新增，3 条）** —— 风险与启用条件那一格的新
措辞、旧措辞的缺席、脚注两句。

**改了的既有测试（3 个文件，5 条）** —— `WithdrawPanel` / `SMSPanel` /
`SMSExtrasPanels` 里断言旧按钮名的那几条：改按钮名 + 补填理由，并顺手加上
「理由是第二个实参、不在 params 里」的断言。

## 五之二、`executeAction` 改成抛错的影响面普查

剩下那约 74 个调用点原先永远走不到 202 分支，现在会拿到一个自己没预料到的
异常类型。**逐处核过**，结论是：全部会显示出来，没有一处被吞成泛型报错、
也没有一处会停在 loading。依据如下。

**写路径的错误只有两种形态，且都汇进 `ActionErrorNote`。** 扫遍
`components/` 与 `pages/` 的 61 个 `useMutation(` 块：

- 29 块写了 `onError`（多为 `onError: setError`），错误存进本地 state，
  由 `<ActionErrorNote error={error} />` 渲染；
- 32 块没写 `onError`，直接 `<ActionErrorNote error={mutation.error} />`。
  逐个核过这 32 处**都真的渲染了**（`ActionErrorNote error={…}` 各 1~5 处）；
  唯一例外 `RunwayThresholdRulePanel` 的 `previewMutation` 把错误交给
  `<PreviewSection … error={previewMutation.error} />`，同样显示得出来。

`ActionErrorNote` 的非 `ApiError` 分支是
`error instanceof Error ? error.message : "未知错误"`，挂 `role="alert"`。
`ApprovalRequiredError extends Error`，所以人看到的是**内核那句原话**
（「action X 风险等级 L3 需要审批，已受理为审批单 Y」），不是一句「未知错误」。
只读路径的 `ApiStateView` 同样有这个分支（`PageState kind="error"` + message）。

**批量路径**：`lib/alertBulkAck.ts` 的 `describeAckFailure` 显式兜底
`if (error instanceof Error && error.message) return error.message;`——
逐行回执里也拿得到那句话。

**唯一一处「非 ApiError 就换成一句套话」的形状**在
`pages/TotpEnrollPage.tsx:191`（`… : "生成密钥失败，请重试。"`）。它**不在
Action 写路径上**：`api/totp.ts` 里只有 `resetStaffAccountTotp` 走
`executeAction`，而 `enrollTotp` / `confirmTotp` 是直接 `client.post` 到
`/api/v1/auth/totp/*`——那两个端点不经内核，回不出 202。
（`resetStaffAccountTotp` 的错误由 `StaffAccountsPanel` 的 `ActionErrorNote` 显示。）

**不会停在 loading**：忙态一律取 `mutation.isPending`，react-query 在
`mutationFn` reject 时把它置回 false，与错误类型无关；没有任何一处用
`instanceof ApiError` 去决定「要不要结束 loading」。`main.tsx` 的重试判据
`error instanceof ApiError && error.retryable` 只作用于 **query**，且对
`ApprovalRequiredError` 求值为 false——不会进重试循环。

## 六、门禁（都在这个 worktree 跑通）

```
pnpm --config.verify-deps-before-run=false -r run typecheck   → 5/5 Done
pnpm --config.verify-deps-before-run=false -r run test        → 全绿
    design-tokens 10 / ui-primitives 16 / ui-admin 261 /
    admin-web 133 文件 1888 条（含并发 agent 同期新增的用例）
pnpm --config.verify-deps-before-run=false -r run build       → Done（含 tsc --noEmit）
```

上面那次三项全绿是在 `197d0bb` 之上、并发编辑的间隙里跑到的。**之后再跑，
typecheck 与 build 又红了两次，两次都不是本片**：

1. `components/ChannelBindingHistory.tsx:151` 有个 `?? … || …` 未加括号的
   **解析**错误（另一个 agent 刚落地的新文件）。它让 6 个 channel 相关测试
   文件**整个装载失败**——`Tests 1592 passed | 0 failed`，但
   `Test Files 6 failed`。对方几分钟后自行修掉。
   **看到「Test Files 失败但 Tests 零失败」时先去看装载错误，别去翻断言。**
2. 同一位 agent 随后新增的 `components/ChannelBindingHistory.test.tsx:92`
   有个 `TS2493`（对 `[]` 取 `[0]`），继续挡住 `admin-web` 的
   `tsc --noEmit`（`build` 脚本是 `tsc --noEmit && vite build`，所以 build 也红）。

**本片自身的证据**（在那两处仍红时取的）：用一份只排除
`ChannelBindingHistory.test.tsx`、其余整个 `admin-web/src` 照收的临时
tsconfig 跑 `tsc --noEmit` → **exit 0**；`vite build` 单独跑 → **exit 0**；
`pnpm -r test` → **全绿 1876 条**（测试装载不受那个类型错误影响）。
**没有改对方任何一个文件。**

**另一处 Windows 侧的假红**：`ui-storybook` 的 `storybook build` 有一次在
打完包、写完 `storybook-static/` 并打出「Storybook build completed
successfully」之后，于进程退出阶段崩在
`Assertion failed: !(handle->flags & UV_HANDLE_CLOSING), file src\win\async.c`
（退出码 3221226505 = 0xC0000409）。**产物是好的**，重跑一次 exit 0。
这是 libuv 在 Windows 上的收尾崩溃，不是构建失败——别照着它去翻 stories。

## risks

- **前端靠 body 形状而不是 HTTP 状态码分辨 202**：`api/client.ts` 的 `post`
  只把解析后的 body 交出来，拿不到 `response.status`。当前判据是
  「有 `approval_request_id` 或 `status === "APPROVAL_REQUIRED"`」，两个字段
  都逐字照抄自后端。**后端改这两个字段名时前端会静默退回 executed 分支**
  ——那正是本片修掉的那个 bug 的形状。要更硬的话，下一步是给 `post` 加一个
  暴露状态码的变体，但那会改动一个被所有读写路径共用的签名。
- **`executeAction` 的 74 个调用点没有承接审批的界面**：以后再有 L0/L1 动作
  被提级，它们会抛 `ApprovalRequiredError`（错误提示是后端那句人话），
  而不是坏成假成功。这是刻意的「响亮地坏」，已按 §五之二 逐处核过影响面，
  并有两条组件级测试守着（变异 6 验证过），但**提级时仍然要来改这里**。
- **理由的长度下限是 3 个字**，挡不住「测试」「先这样」。真正的把关只能在
  审批人那里；把数字调大只会让人学会凑字数。
- 回执里那句「去「操作与审批 · 待审批」」硬编码 `/actions?sub=pending`
  （常量 `APPROVAL_QUEUE_PATH`）。子页签 id 来自 `navigation.ts`，改路由要一起改。

## follow_ups

- **上线顺序**：本片与 `197d0bb` 同一批出。分开出的两种后果写在文首。
- 审批单落下之后，**提交人自己看不到「我提的那张单现在怎么样了」**——回执给的
  是单号和队列入口，队列页没有「我提交的」这个筛选。真跑起来之后这会是第一个
  被抱怨的地方。
- 六处的「理由」目前是单行 `Input`（仓库没有 Textarea 原语，`WithdrawPanel`
  的备注也是单行）。L3/L4 的理由多半要写几句话，值得单独加一个 Textarea 原语。
- `cards.card.topup` / `cards.card.redeem` / `freeze` / `unfreeze` 仍是 L1。
  它们也花钱（充值/赎回），下一轮风险评级如果把它们提上去，改动形状与本片
  完全一样：包装函数换 `submitAction` + 加 `reason` 参数、界面加 `ApprovalReasonField`、
  回执换 `actionResultOf`。
