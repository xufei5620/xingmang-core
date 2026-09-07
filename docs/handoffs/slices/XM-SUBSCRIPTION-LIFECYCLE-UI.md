# XM-SUBSCRIPTION-LIFECYCLE-UI：订阅批次与代理资产的退款 / 终止入口

> ## ⚠ 先说一件与派工前提不符的事：这四个入口今天**仍然点不到**
>
> 派工写的是「运营能登记订阅批次和代理资产，却无法退款、无法终止」。
> **前半句不成立**：登记入口也点不到，因为它们的宿主组件根本没被挂上路由。
>
> `web/apps/admin-web/src/components/UpstreamAccountDetail.tsx` 是
> `SubscriptionBatchDialog` 与 `ProxyAssetDialog` 的唯一宿主。而它自己在整个
> `web/` 里只有三处出现：**它自身、它自己的测试、以及
> `components/InvoiceConsolePanel.tsx:179` 的一句注释**。`pages/` 下零引用，
> `router.tsx` 里没有 lazy/动态 import（实测：全仓 grep 标识符 + grep 路径串，
> 两种口径都只有这三处）。
>
> 所以本片建的四个入口与既有的登记入口处境相同：**代码在、测试有、人点不到。**
>
> **本片刻意没有去挂它**。挂在哪一页、以什么形态展开（`UpstreamDetailPage.tsx`
> 今天只是个结构壳）是产品决定，而且要动 `pages/`——超出派工「只动订阅批次/
> 代理资产相关组件」的范围。已在开工时就把这件事告知派工方，见 follow_ups 第一条。

- **status:** implemented，**未提交、未上线**（按派工要求改完即停，不 commit/push）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `0c36ea3`（XM-ACTION-REASON）。
- **前置：** 无功能前置。后端四个 Action 一行未动（**只读**）。
- **来源：** 派工「四个生命周期 Action 已注册可用，前端零入口」。

## 一、四个 Action 的 Schema 逐字（在哪确认的）

全部读自 **`internal/platform/finance/subscription_actions.go`** 的四个
`Definition`，逐字照抄，未凭记忆：

| Action ID | 等级 | Permission | Schema 字段（全部必填） |
|---|---|---|---|
| `finance.subscription_batch.refund` | L1 | `finance.subscription.manage` | `subscription_batch_id`、`refunded_minor`、`refunded_on`、`reason` |
| `finance.subscription_batch.terminate` | L1 | 同上 | `subscription_batch_id`、`terminated_on`、`reason` |
| `finance.proxy_asset.refund` | L1 | 同上 | `proxy_asset_id`、`refunded_minor`、`refunded_on`、`reason` |
| `finance.proxy_asset.terminate` | L1 | 同上 | `proxy_asset_id`、`terminated_on`、`reason` |

函数名对应 `subscriptionBatchRefundDef` / `subscriptionBatchTerminateDef` /
`proxyAssetRefundDef` / `proxyAssetTerminateDef`；四个都是
`RiskLevel: action.L1`、`Permission: ScopeSubscriptionManage`
（`permissions.go` 里 = `finance.subscription.manage`）、`PrincipalTypes: humanOnly`。

**三条容易写错、已逐字核对的约定：**

1. **`reason` 是这四个 Action 自己 Schema 里的字段，走 `params`。**
   它**不是** `0c36ea3` 给请求体加的那个顶层 `reason`——后者是内核对 **L2 及
   以上**的审批理由（`action/kernel.go`）。这四个是 L1，内核不要求顶层 reason、
   也不会落审批单。放错地方的后果不是「多传一个无害字段」，而是 **Schema 校验
   直接 400**（params 缺 `reason`）。有一条用例把请求体整体 `toEqual`，
   顶层多一个 `reason` 就会红。
2. **`refunded_minor` 是「累计值」不是增量**，且是 **scale-6 纯整数字符串**
   （`$29.99` → `"29990000"`，后端正则 `^[0-9]{1,19}$`）。带小数点会被当成微单位，
   金额差十万倍且不报错（`parseMinorParam` 的注释原话）。
3. **Schema 是白名单**（`action/schema.go`：「未声明的字段一律拒绝」），
   所以包装函数逐个复制字段，不透传调用方对象。

**调用方式：**四个都用 `executeAction`（只接受「同步执行完」一种结局）而不是
`submitAction`。理由：L1 不会走 202；真拿到 202 说明等级被人改过，此时抛
`ApprovalRequiredError` 比悄悄显示成功正确——这一层没有承接审批单的界面。
（照 `api/platform.ts` 现在的约定读的，没有照抄别处旧写法。）

## 二、二次确认怎么做的

触发器是**有名字的按钮**（「记退款」/「终止」，终止用 `danger` 配色），
不是列表行里的图标，也没有折进「更多」菜单——把不可逆动作藏进二级菜单，
等于把「这一步撤不回来」这句话一起藏了。

点开之后，提交要过**三道**（任何一道不过，一个请求都不发）：

1. **后果说明写在脸上**：对话框顶部一个 warning 块，逐条列后果。每一条都能在
   后端指出出处（见组件里 `LIFECYCLE_COPY` 上方的注释），不是替业务想的。
   终止那三条是「自终止日起不再摊销 / 结转一笔损失进损失科目 / 只能做一次，
   平台没有『撤销终止』的 Action」。
2. **一个必须勾的确认框**，标签里带**这一笔的具体标识**
   （「我确认要终止批次 2026-08-01 → 2026-08-31，并知道它会结转一笔损失、
   且无法撤销。」）。不勾就提交 → 出一句 `role="alert"` 说明为什么，
   **不发请求**。刻意**不做成禁用按钮**：禁用的按钮不解释自己为什么禁用。
3. **理由必填**（Schema 要求），空白也拦在前端。

**刻意没有复用 `ApprovalReasonField`。** 它的标签是「理由（提交审批用）」、
提示语写着「会先落成一张审批单」——这四个是 L1，提交即执行，用它等于在界面上
说一句不会发生的事。这里的理由进的是**审计事件**（Handler 的
`action.RecordReason`），文案照这个事实写。

**日期不预填。** 浏览器时区与业务日切时区可能不是同一天，预填出来的那个日期
看起来完全正常却会把成本记到隔壁那天。那一格的说明里写了按账号的
`business_day_tz` 解释，并把有效期范围显示出来。

## 三、前端拦什么、不拦什么（判据是「服务端说不说得清」）

这一条是本片最要紧的设计判断，依据全部来自实际读过的后端代码：

`finance.domainError`（`finance/actions.go:630`）只把 `ErrNotFound` /
`ErrMissingField` / `ErrInvalidFormat` / `ErrInconsistent` / money 那两个映成
`INVALID_PARAMS`；**其余原样返回裸错误**，再被内核归一成 `EXECUTION_FAILED`
+ 一句「action … 执行失败」（`action/kernel.go:283`），HTTP 502。

于是分成两类：

**前端自己拦（服务端的原话到不了界面）：**

| 情况 | 后端行为 | 前端做法 |
|---|---|---|
| 累计退款额低于已登记值 | `ErrRefundNotDecreasing` → 502「执行失败」 | 表单校验拦住，并把已登记的数说出来 |
| 已经终止过 | `ErrAlreadyTerminated` → 502「执行失败」 | 终止入口自己收起来，原地说明「已于 X 终止；终止只能做一次」 |

**前端刻意不拦（服务端原话到得了界面，再写一份只会漂开）：**

- 终止日 / 退款生效日不在有效期内 → `ErrInvalidFormat` → `INVALID_PARAMS`
  400，带着「终止日 X 必须落在有效期 A..B 内」整句回来；
- 退款超过实付+附加 → `ErrInconsistent` → 400，带着「退款 X 超过实付+附加 Y：
  成本基础不得为负」；
- 跨环境 → `CodePermissionDenied` 403，带着「不允许跨环境操作成本登记簿：
  调用者身份属于 development」。

有一条用例专门钉住「到得了」这半边（用 400 那句完整的话），所以这个取舍不是
口头承诺。

另有一处**刻意不比较**：DTO 的 `refunded.scale` 与表单的 scale-6 不一致时
**不做金额比大小**，交给服务端判。跨标度比出来的「够大 / 不够大」都是错的。

## 四、回执

- **成功**：`onDone({ title, runId })` 交给既有的 `ActionResultNote`——
  它给 `run_id` 与「去审计记录页」的入口。标题用过去式且说清发生了什么：
  「订阅批次已终止，损失已结转」，不写「操作成功」。
- **失败**：`ActionErrorNote` 就地显示**后端原话 + 错误码 + request_id**，
  且**表单留在原地**（有用例断言 403 之后填过的终止日还在）。

**没有显示「结转了多少损失」**：终止 Handler 的返回值确实带 `amortization_loss`，
但 `finance.AmortizationLoss` 与 `SubscriptionBatch` **没有 JSON tag**，
序列化出来是 Go 字段名（`LossMinor`/`BookedOn`…），不是 read DTO 的形状，
也没有任何契约钉住它。照着猜等于编数据，所以本片不显示它，改为写成 follow_up。
成功之后四类 Query 一并失效，表上的金额从只读 DTO 重新读回来。

## 五、改了什么

**新增**
- `web/apps/admin-web/src/components/SubscriptionLifecycleDialog.tsx` ——
  四个入口共用的对话框，加 `subjectFromBatch` / `subjectFromProxy` 两个适配器
  （批次是 `starts_on`、代理是 `opened_on`，差异在这两个函数里一次性抹平）。
- `web/apps/admin-web/src/components/SubscriptionLifecycleDialog.test.tsx`（18 条）。

**改动**
- `web/apps/admin-web/src/api/finance.ts` —— 四个包装函数
  （`refundSubscriptionBatch` / `terminateSubscriptionBatch` /
  `refundProxyAsset` / `terminateProxyAsset`）与四个参数类型。
- `web/apps/admin-web/src/lib/subscriptionForms.ts` —— `validateLifecycleForm`、
  `lifecycleRefundFields`、`lifecycleTerminateFields`、`FINANCE_FORM_AMOUNT_SCALE`。
- `web/apps/admin-web/src/components/UpstreamAccountDetail.tsx` ——
  批次表加「操作」列（两个入口），代理行的「修改代理」旁边加两个入口。
- `web/apps/admin-web/src/components/UpstreamAccountDetail.test.tsx` —— +3 条。

**一个 `internal/` 或 `cmd/` 下的文件都没动**；`api/platform.ts`、
`pages/AlertsPage.tsx`、`K:/发票/` 一律没碰。

### ⚠ 提交时只取本片这 7 个文件

同一个 worktree 里此刻还有**另一个 agent 未提交的改动**（告警静默那一片：
`cmd/platform-api/main.go`、`internal/platform/alerts/`、
`internal/platform/httpapi/alert*`、`web/…/api/alerts.ts`、`lib/alerts*`、
`pages/AlertsPage*`、`components/AlertSilences*`、
`docs/handoffs/slices/XM-SILENCE-LIST.md`）。**那些不是本片的**，别一起带上。

本片的全部文件：

```
web/apps/admin-web/src/api/finance.ts                                  （改）
web/apps/admin-web/src/lib/subscriptionForms.ts                        （改）
web/apps/admin-web/src/components/UpstreamAccountDetail.tsx            （改）
web/apps/admin-web/src/components/UpstreamAccountDetail.test.tsx       （改）
web/apps/admin-web/src/components/SubscriptionLifecycleDialog.tsx      （新）
web/apps/admin-web/src/components/SubscriptionLifecycleDialog.test.tsx （新）
docs/handoffs/slices/XM-SUBSCRIPTION-LIFECYCLE-UI.md                   （新）
```

## 六、测试与变异验证

`SubscriptionLifecycleDialog.test.tsx` 18 条 + `UpstreamAccountDetail.test.tsx`
新增 3 条。后者补的是前者证明不了的那半句：**入口真的长在批次行与代理行上**
（组件自己会做对的事 ≠ 运营点得到它）。

### 变异验证（8 项，每项都配了「不该变红」的对照组）

| # | 变异（一律改条件，不删代码） | 期望 | 结果 | 对照组 |
|---|---|---|---|---|
| 1 | `validateLifecycleForm` 的 `if (!values.confirmed)` → `if (false && …)` | 两条二次确认用例变红 | 红 | 其余 16 条全绿 |
| 2 | **`submit()` 里的 `if (Object.keys(found).length > 0) return;` → `if (false && …)`**（保留报错、只拆掉拦截） | **四条缺席型断言在 `not.toHaveBeenCalled()` 那一行**变红 | **红** | 14 条绿 |
| 3 | 退款下限 `if (floor !== null && …)` → `if (false && …)` | 「低于已登记值」变红 | 红 | 「持平不拦」「跨标度不比」仍绿 |
| 4 | 标度闸 `if (guards.refundedScale !== …) return null` → `if (false && …)` | 「跨标度不比较」变红 | 红 | 其余 17 条绿 |
| 5 | `if (action === "terminate" && alreadyTerminated)` → `if (false && …)` | 「已终止不给终止入口」变红 | 红 | 「已终止仍可补记退款」仍绿 |
| 6 | 包装函数里 `refunded_on:` 改名成 `refund_on:` | 批次退款的 params 用例变红 | 红（连带「持平」那条） | 代理退款那条仍绿（另一个包装函数） |
| 7 | 批次行的 `action="terminate"` 改成 `"refund"` | 三条接线用例变红 | 红 | 该文件原有 14 条全绿 |
| 8 | 批次行的 `disabled={!canManage}` → `disabled={false}` | 权限锁定那条变红 | 红 | 其余 16 条绿 |

**第 2 项是这一组的关键，也纠正了我自己的一次误判。**
第 1 项（把 `confirmed` 校验整个拆掉）变红时，**红在 `findByText` 那个正向锚点
上**——错误文案没出现，所以先失败的是锚点，`not.toHaveBeenCalled()` 那一行
根本没跑到。**只有第 1 项的话，缺席型断言其实一次都没被验证过。**
第 2 项保留报错、只拆掉那个 `return`，四条用例才**恰好红在
`expect(fetchMock).not.toHaveBeenCalled()` 这一行**（报错信息：
`expected "vi.fn()" to not be called at all, but actually been called 1 times`）。
这才是「不确认就不发请求」成立的证据。

**另一处自我更正（已写进代码注释）：** 我原本在测试里写了
`letRequestsFly()` 并注释说「去掉它，闸拆掉也照样绿」。**实测是错的**——
把 flush 换成空函数、闸也拆掉，四条照样红：前面那句
`await findByText(...)` 已经顺带放行了足够回合。注释已改成实测结论；
flush 保留为余量（哪天有人把锚点换成同步的 `getByText`，缺了它断言会静悄悄
变成恒真），但**不再声称它是那根让断言成立的柱子**。

**还有一条用例的 fixture 是改过的**：「跨标度不比较」原先用 scale-2 的 `500`，
而那个数在两种读法下都拦不住 `$3`，**变异之后照样绿**——恒真。改成 scale-2 的
`500000000`（按 scale-6 误读成 `$500`），拿掉标度闸就会把 `$3` 误判成「低于
已登记」，这才有了第 4 项的红。

### 缺席型断言一览（每条都收窄了容器）

- 「不勾确认 / 理由留空 / 退款额过低 → 不发请求」：先 `await` 正向锚点（那句
  解释真的出现），再同步断言，`within(dialog)` 收窄；由变异 2 验证非恒真。
- 「已终止的批次没有终止入口」：**同屏渲染两笔**（已终止 + 未终止），各套一个
  `<section aria-label>`，用 `within(getByRole("region", …))` 收窄。
  对照组（未终止那笔按钮还在）挡住「按钮名字写错也会绿」这个假阴性。
- 「终止表单没有累计退款额那一格」`queryByLabelText` 收窄在 dialog 内。
- 「没有权限时点触发器开不出表单」+ `fetchMock` 未被调用。

## 七、门禁

三条全绿，均在本 worktree 实跑：

```
pnpm --config.verify-deps-before-run=false -r run typecheck   # 5 个 project 全 Done
pnpm --config.verify-deps-before-run=false -r run test        # 2212 条全绿
pnpm --config.verify-deps-before-run=false -r run build       # EXITCODE=0
```

测试分布：design-tokens 10、ui-primitives 16、ui-admin 261、
**admin-web 1925**（本片 +21：18 + 3）。

**build 那个坑这次没有咬人，但我没有只看退出码。** 把整个 `-r run build`
的输出落到文件再 grep，两个 app 都真的构建了：

```
web/apps/ui-storybook build: ✓ 163 modules transformed.   → Done
web/apps/admin-web  build: ✓ 359 modules transformed.     → Done（built in 282ms）
```

日志里搜 `3221226505` / `ELIFECYCLE` / `Failed` 均无命中，所以 `ui-storybook`
这次没有崩在 libuv 拆卸、也就没有中断 `pnpm -r`。

**`go test` 未跑**（后端一行没动，且另一个 agent 正在用同一个测试库）。

## 八、我对「这四个动作等级是否合适」的判断（**没有改，等裁定**）

先读了 `docs/handoffs/slices/XM-RISK-RESTORE.md` 的判据与待裁定清单。结论：

**`refund` 那两个我认为够得上更高等级，`terminate` 那两个不一定。** 依据：

- XM-RISK-RESTORE 自己写着「**ADR-003 的等级表把『退款』这类钱出去的动作放在
  L4**」——那句话是在论证提现该不该是 L4 时写的，而这两个 Action 的名字就叫
  refund。按字面读，它们正命中那一格。
- 但**这里的 refund 与那里的「退款」不是同一件事**，这是我不敢直接说「该改」
  的原因：`subscription_batch.refund` **不转移任何资金**，它登记的是「上游已经
  退给我们多少」，是一笔**记账**。钱的移动发生在上游，平台只是把它记下来。
  按爆炸半径看，它改的是毛利报表与成本基数，不是「钱出去了」。
- `terminate` 更明确地只是记账 + 结转损失，同样不动钱。

**真正让我觉得该重估的，是这四个的「不可逆」而不是「花钱」：**
退款额只增不减、终止只能做一次且没有撤销 Action。ADR-003 把「启停资源」这类
不可逆动作放在 L2（XM-RISK-RESTORE 就是据此把关停卡从 L1 抬到 L2 的，
`compensation_mode: NOT_POSSIBLE`）。**按同一条判据，这四个——尤其两个
terminate——像 L2 而不是 L1。**

**但仓库里没有为它们写下任何「本该更高」的依据**，这一点与 XM-RISK-RESTORE
改的那六个不同：那六个每一个都有一句「Foundation-B 落地后应升到 L2」之类的
原文。`subscription_actions.go` 的段注释反过来说「六个动作，全是 **L1 + 仅人类
身份**……**L1 是 §8.3 的硬性要求**」——它给的是**正面依据**，不是「因为内核跑
不动」。按 XM-RISK-RESTORE 定的纪律（「找不到依据的保持原样」「三处『全部 L1』
后面跟的是正面依据，不动」），**本片一个等级都没改**，也没碰契约。

裁定要点写进 follow_ups。**注意一个副作用**：这四个若抬到 L2+，本片的界面会
当场不对——它们会开始走 202 落审批单，而 `executeAction` 会抛
`ApprovalRequiredError`。届时要把这个对话框改成 `submitAction` + `actionResultOf`
两种结局，并且**顶层 `reason` 变成必填**（现在的 `reason` 在 params 里，
两个地方都要给）。这不是几行改动，是这个组件的一次重写。

## 九、risks

- **最大的风险不是代码，是这四个入口（连同既有的登记入口）今天没有页面挂载
  它们。** 见文首。不解决这一条，本片对运营的净效果是零。
- **「已终止就收起终止入口」依赖只读 DTO 的 `terminated_on`。** 列表是缓存的，
  两个人同时操作时后到的那个仍会撞上服务端的 `ErrAlreadyTerminated`——而那条
  拒绝在界面上只会显示成「执行失败」。前端这道闸是**减少**这种撞车，不是消除。
  真正的修法在后端（见 follow_ups）。
- **退款额「只增不减」的下限取自当前页面上的 DTO。** 同上，页面陈旧时下限也
  陈旧，服务端仍是最终裁决。跨标度那种情况前端**完全不判**。
- **代理表只列出与本账号批次关联的代理**（`linkedProxies`，既有行为）。
  一份没有任何批次关联的代理，在这一页上仍然退不了款也终止不了——
  这是既有的列表口径，本片没有改它。
- 「结转了多少损失」没有显示，只能到审计或报表里看。原因见第四节。

## 十、follow_ups

- **把 `UpstreamAccountDetail` 挂上路由**（谁挂、挂哪一页由产品负责人定）。
  这是四个入口 + 既有登记入口能不能被点到的唯一前置。本片刻意没做——
  它要动 `pages/`，超出派工范围，且是产品决定。
- **产品负责人裁定这四个 Action 的风险等级**（见第八节）。若抬到 L2+，
  本片的对话框要改写成 `submitAction` 两种结局 + 顶层 `reason`。
  **别只改等级不改前端**——那正是 XM-RISK-RESTORE 文首警告的那种断路。
- **后端把 `ErrRefundNotDecreasing` 与 `ErrAlreadyTerminated` 映进
  `domainError` 的 `CodeConflict`（→ 409）**。今天它们落到 default 分支，
  被归一成一句「执行失败」，操作员看不出真正原因——前端因此不得不把这两条
  规则抄一份到界面上，而那份抄件会随 DTO 陈旧而失准。这是**一个事实钉在两处**
  的开始，早修早省事。（本片是纯前端，没有动它。）
- **给终止 Action 的返回值定一个 DTO**（`AmortizationLoss` 现在没有 JSON tag）。
  有了它，回执才能直接说「本次结转损失 $X」——那是终止这个动作**唯一**要被
  记住的结果。
- 代理资产表补一个「已终止」状态列：现在只有挂载与凭据两个徽章，一份已终止的
  代理在表上看不出来（对话框里能看到「终止状态」，但要点开才行）。
