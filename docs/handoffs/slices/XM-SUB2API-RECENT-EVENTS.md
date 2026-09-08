# XM-SUB2API-RECENT-EVENTS：Sub2API「资金概览 → 最近事件」四行接上真实数据

> ## ⚠ 两处与派工前提不符，都影响做法，先说
>
> **一、分桶逻辑早就抽出来了，不在 `PaymentStatusRollupTable` 里。**
> 派工写「如果 `PaymentStatusRollupTable` 里的分桶逻辑是内联的，把它抽成共享
> 纯函数」，并指向该文件约 7–15 行。那几行是**文档注释**；真正的分桶与求和
> 在 `web/apps/admin-web/src/lib/paymentStatusRollup.ts` 的
> `rollupPaymentStatuses`，`PaymentStatusRollupTable.tsx:25` 只是调用它。
> 所以本片**没有新抽函数**，而是复用既有的那一个，并在里面补上跨币种判据。
>
> **二、跨币种在这条通道上不是「客户端会不会相加」的问题，而是后端已经
> fail closed 了。**
> `stats_by_status` 的每一条金额都由后端打上**同一个**合约币种
> （`internal/platform/httpapi/payments.go:302-308`，`Currency: result.Currency`），
> 非合约币种的订单**金额被排除、笔数照计、整份汇总置 `IsPartial=true`**
> （`contracts/connectors/payments.read.v1.md` 第 214–223 行「跨币种处理」）。
> 也就是说派工设想的「一边 fail closed 一边偷偷相加」在今天的响应里走不到——
> 真正会误导人的是 `is_partial`。两条都实现了，见第四节。

- **status:** implemented，**未提交、未推送**（按派工要求改完即停）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `8c446e5`。
- **前置：** 无。后端 `internal/`、`cmd/` **一行未动**（只读核对）。
- **来源：** 派工「四行恒为「—」+「未接入」，理由写着支付事件端点尚未接入」。

---

## 一、复核结果（行号已按当前工作树重新核对）

| 派工说的 | 实测 | 结论 |
|---|---|---|
| 占位在 `Sub2ApiFinanceOverview.tsx` 约 80 行 | 就在**第 80 行**，四行写死 `—`/`—`/`<Badge>未接入</Badge>` | ✅ 缺口今天仍在 |
| 已实现的同款在 `PaymentStatusRollupTable.tsx` 约 7–15 行 | 7–15 行是注释；实现在 `lib/paymentStatusRollup.ts` | ⚠ 见开头 |
| 挂载点 `Sub2ApiOrdersPanel.tsx` 约 96 行 | **第 96 行** `<PaymentStatusRollupTable …>` | ✅ |
| 端点在 `router.go` 约 525–529 行**无条件挂载** | 在 **`router.go:540-547`**，且是 `if d.PlatformOrders != nil` **有条件**挂载 | ⚠ 见下 |
| `XM_PLATFORM_PAYMENTS_MODE` 空串按 fake | `cmd/platform-api/platformpayments.go:39-41` `case "": return paymentsModeFake` | ✅ |
| `launch.yaml` 约 271 行未设值 | **第 271 行** `XM_PLATFORM_PAYMENTS_MODE: ${XM_PLATFORM_PAYMENTS_MODE:-}` | ✅ |

关于挂载：路由包在 `if d.PlatformOrders != nil` 里，而 `d.PlatformOrders` 在
`mode=off` 时为 nil。生产用的 `launch.yaml` 不设值 → fake → **挂载**，所以
派工的结论（这条端点在生产上是通的）成立，但机制是 nil 判断而不是无条件。
这个区别有后果：**`mode=off` 的部署上这条端点会 404**，前端 `listPlatformOrders`
把它翻成 `FeatureNotMountedError`。本片据此给「最近事件」单独包了一层
`ApiStateView`，见第三节。

`router.go` 当前**有其它 agent 的未提交改动**（`git status` 里 `internal/`、
`cmd/`、`db/` 多个文件为 M/??）。上面的行号读自当前工作树；`if d.PlatformOrders != nil`
这段带 XM-PAY0 注释，是既有代码，不是别人这次加的。

---

## 二、分桶口径逐字（在哪确认的）

全部读自 **`web/apps/admin-web/src/components/platformOrdersColumns.tsx:41-56`**
的 `bucketLabel` / `STATUS_BUCKET_OPTIONS`，逐字照抄，未凭记忆：

```
upper = status.toUpperCase().trim()
  PAID | RECHARGING | COMPLETED | SUCCESS                                  → "成功到账"
  PENDING                                                                  → "待处理"
  EXPIRED | CANCELLED | FAILED                                             → "失败"
  REFUND_REQUESTED | REFUNDING | REFUND_PENDING | REFUND_FAILED
    | PARTIALLY_REFUNDED | REFUNDED                                        → "退款与冲正"
  其它                                                                      → "未知"
STATUS_BUCKET_OPTIONS = ["成功到账", "待处理", "失败", "退款与冲正"]（「未知」永远排最后）
```

**字段名与算法**（读自 `lib/paymentStatusRollup.ts` 与 `api/finance.ts:1006-1009`）：

- 输入是 `stats_by_status: Record<string, PlatformOrderStat>`，**键是上游原始
  状态字面量，不是归一化桶名**（`api/finance.ts:1014-1017` 的注释原话）。
- 笔数 = `stat.count` 直接相加，`number`。
- 金额 = `stat.amount.minor_units`（**字符串**）经 `BigInt()` 相加；
  `null` 或解析失败 → 这一条**不贡献金额**（不算 0）；整桶一条都没贡献 →
  合计是 `null` 而不是 `0n`。
- 币种字段是 `stat.amount.currency`。

四行的映射（`Sub2ApiFinanceOverview.tsx` 的 `RECENT_EVENT_ROWS`）：
成功充值→`成功到账`、待处理→`待处理`、失败→`失败`、退款→`退款与冲正`。
行名保留原型措辞，桶名放在该单元格的 `title` 上（见第三节为什么行名不改成桶名）。

---

## 三、共享函数抽在哪里、为什么

**没有新抽分桶函数**——`rollupPaymentStatuses` 已经是共享的，本片只是**加了
第二个调用方**。两边现在共用同一份分桶、求和、失败判据：

| 调用方 | 位置 |
|---|---|
| 「充值订单」页签「本区间汇总」 | `components/PaymentStatusRollupTable.tsx:25` |
| 「资金概览」页签「最近事件」 | `components/Sub2ApiFinanceOverview.tsx` 的 `RecentPaymentEventsTable` |

**新增到 lib 的东西**：`presentPaymentBucket(rollup, bucket, isPartial)` —— 把一个
桶摊成一行的取值判断（金额/笔数给不给、给不出时说什么）。放进 lib 而不是组件里，
是因为它是**口径**且值得脱开 DOM 单测；四挡见下表。

**刻意留在组件里没有搬进 lib 的**：行名（成功充值/待处理/失败/退款）、
「去向」文案、徽章措辞。它们只服务「资金概览」一个调用方，搬进 lib 只会让
lib 认识一个它不该认识的页面。

`presentPaymentBucket` 的四挡（与同一页上方 `PaymentSummaryCards` 的
`BucketCard` 同一条判据，不另起一套）：

| 情形 | coverage | 笔数 | 金额 |
|---|---|---|---|
| 桶缺席 + `is_partial=false` | `confirmed-zero` | `0` | `0`（**确认过的零**，不是「未接入」） |
| 桶缺席 + `is_partial=true` | `unknown` | `—` | `—` + 「无法确认它真的是零」 |
| 桶存在 + 完整 | `known` | 实数 | 实数 |
| 桶存在 + `is_partial=true` | `partial` | 实数 | 实数 + 「合计只会偏低」 |
| 桶存在 + 币种不一致 | `partial` | 实数 | `—` + 「币种不一致，合计给不出」 |
| 桶存在 + 整桶无金额 | `partial` | 实数 | `—` + 「上游没有给出…（不是 0）」 |

**查询复用**：直接用「充值订单」页签导出的 `useSub2ApiOrdersQuery(range)`，
**同一个 `queryKey`**（`["platform-orders","sub2api",from,to]`）。两个页签命中
同一份缓存，所以两处的数字在构造上不可能对不上——不是「我们记得让它们一致」，
是它们本来就是同一个响应。也不多打一次请求。

**单独包 `ApiStateView`**：`mode=off` 时这一块显示「未接入」，而整页的渠道汇总、
使用收入、经营利润桥（另外两条 Query）照常。有用例守着（见第五节）。

---

## 四、「充值不是收入」在界面上怎么体现

派工点名这是这一页最容易被读错的地方。页顶那条横幅（第 58 行）在「最近事件」
这个位置**已经滚出屏幕**，所以区分写在区块自己的标题正下方：

> 本统计区间内的**用户充值与支付事件**分桶，**不是当期收入**：收入是上方
> 「经营利润桥」的「使用收入」一行，两个数永远分开列，不相加。逐笔明细在
> 「充值订单」页签，那里的「本区间汇总」与这四行同一份口径、同一个查询。

配套的四件事：

1. **位置没动**——区块仍在「经营利润桥」**之外、之下**，不进利润桥的 `<dl>`。
2. **行名保留「成功充值」而不是改成桶名「成功到账」**：「充值」两个字本身就在
   说这不是收入，比桶名更挡得住误读。桶名放在该单元格 `title` 里，需要跟
   「充值订单」页签对账的人拿得到 join key。
3. **`<caption>`（屏幕阅读器）** 同样写明「这是充值口径，不是收入」。
4. **指路**：明说收入在哪一行，读者不必自己猜两个数该怎么合。

有用例逐字守着这四句（不是只断言渲染成功），且做过变异验证。

---

## 五、跨币种怎么处理

两条不同的危险，分开处理：

**(a) 同一个桶里出现两种币种字面量 → fail closed。**
`rollupPaymentStatuses` 原来会**直接相加、并保留先遇到的那个币种**——USD 和
CNY 加在一起，标上其中一个符号，看不出错。本片改成：币种不止一种就
`minorUnits = null`、`amountGap = "currency-mismatch"`，**笔数照给**（契约里
`Count` 本来就不区分币种），界面显示「—」+「币种不一致，合计给不出；不做
隐式换算」。与「资金概览」的 `aggregateChannelMoney`（`currency-mismatch` →
`money = null`）同一条纪律。

细节两点：
- **只统计真正贡献了金额的条目的币种**。一条金额缺席的记录自称 USD，不能证明
  这个桶里有 USD 的钱——否则既有的「部分条目给出金额」用例会被误伤。
- **币种空串（上游没说）算一个独立取值**。把「不知道是什么币」的钱和已知 CNY
  的钱加起来，等于替上游认定了币种。

如开头所说，**这一支今天的真实响应走不到**（后端只发一个合约币种）。它是防御
性闸门：口径不该依赖「上游现在恰好只发一种币」这个会变的前提。已在代码注释里
写明它是闸门不是常态分支。

**(b) 后端排除了非合约币种的金额（`is_partial=true`）→ 给数字 + 说清偏差方向。**
这是**今天真正会发生**的那一种。契约（第 216–223 行）：金额只对合约币种求和，
笔数不区分币种照计，命中非合约币种时整体 `IsPartial=true`。所以此时的金额
**不是一个没意义的数**，而是「合约币种子集的正确合计」，偏差方向确定（只会偏低）。
按 `BucketCard` 对同一份数据的既有做法给出数字，并在该行注明
「覆盖不全：上游有未计入金额的订单（非合约币种或未识别渠道），笔数仍完整，
合计只会偏低」。

**为什么 (a) 抹成「—」而 (b) 不抹**：(a) 的和是**没有意义的数**（USD+CNY），
(b) 的和是**有意义但不完整的数**。把后者也抹掉，等于对一个方向已知的偏差
放弃全部信息，反而更难用。这一条与派工「沿用同一条纪律」的字面要求有取舍，
理由写在这里，若判定要一律抹成「—」，改 `presentPaymentBucket` 一处即可。

**未归类状态**：`unknownStatuses` 非空时，区块顶部出警示条点名那些状态，并
说明「四行之和小于本区间共 N 笔」——否则四行会静静地少算。

---

## 五之二、两组「四个桶」的口径标注（产品负责人裁定：两组都留，按「全面」来）

这一页有**两组算同一批分桶、口径不同**的数字：

| | 上方「支付日快照」六卡 | 下方「最近事件」四行 |
|---|---|---|
| 来源 | `sub2api.payments.daily` **指标** | 逐笔订单端点 `stats_by_status` |
| 覆盖 | **单个业务日**，不随区间聚合 | **跟随所选统计区间** |
| 周/月区间下 | 全是「—」 | 有数 |

问题不在于有两组，而在于**没标清它们为什么不一样**——周/月时上面「—」下面
有数，读者第一反应是「有一个坏了」。三处标注：

1. **各自标明口径。** 六卡外新包一个 `<section>`（`PaymentDaySnapshot`），
   标题「支付日快照」+ 副标题写明来自 `sub2api.payments.daily`、口径是
   **单个业务日**、与下方**区间合计**是两条独立通道，**「不是同一个数的两个答案」**。
   「最近事件」副标题补一句对称说明：口径是**区间合计**、跟随所选区间，
   「两组算的是同一批分桶，但覆盖的时间范围不同」。
2. **非单日区间显示「—」时说清是口径不覆盖。** `range.from !== range.to` 时
   六卡上方出一条说明：「所选区间是 X 至 Y，**超出这个口径能回答的范围**，
   所以下面六张卡显示「—」。这是**口径不覆盖，不是数据缺失**，链路没有故障，
   不需要排查；要看这个区间的合计，见下方「最近事件」。」
   —— 这两件事的**下一步完全相反**（前者不用管，后者要查），所以必须分开说。
3. **单日区间不显示这段话。** 否则它变成永远在场的噪声，读者会连真正该看的
   那次也一并忽略。有缺席型断言守着，并做了变异验证（M7）。

4. **卡片徽章分成两种**（`PaymentSummaryCards.tsx`，**2026-09-08 team-lead
   放行后实施**）。此前非单日区间与真·未接入都挂「未接入」徽章，在屏幕上
   长得一模一样；现在非单日区间显示「**仅支持单日**」。只把区别写在下面那行
   小字里不够——徽章才是扫一眼就会读到的东西。

   改法：`UnavailableCard` 加可选 `badge?: string`，**默认仍是「未接入」**，
   因此不传的调用点行为逐字不变（有用例钉住，M11 验证）；只有
   `BucketCard` / `FeeCard` 的非单日分支经 `coverageBadgeFor(range)` 传新值。
   「净现金流入」两平台恒为未接入、与区间无关，**不跟着改**，它是同一次渲染
   里的对照组。

   **NewAPI「资金与订单」同时生效**——该组件两边共用，那边存在同样的混淆，
   一起修掉是这次改动的一部分价值（team-lead 原话）。`NewApiFinanceOverview.test.tsx`
   跑通未改一行。

## 六、变异验证明细

每次变异都是**改条件**（不是删代码），改完跑、看红在哪一行、再还原。
「对照组」= 同一次变异下**不该**变红、且确实没红的用例。

| # | 变异 | 预期红 | 实际红在 | 对照组（保持绿） |
|---|---|---|---|---|
| M1 | `paymentStatusRollup.ts:111` `currencies.length > 1` → `> 2` | 跨币种 fail closed 全组 | lib `:106` `expected 50000n to be null`；lib `:134`；lib `:207`；组件 `FinanceOverview.test.tsx:410`（`getByText("—")`）；组件 `:436`（`queryByText("¥500.00")` 缺席断言）；`Sub2ApiOrdersPanel.test.tsx:278` | 41–42 条全绿，含「币种一致时照常相加」「四行显示真实金额与笔数」，以及每条跨币种用例自己的**锚点**（`findByText("$99.00")` 未红） |
| M2 | `presentPaymentBucket` 缺席分支 `if (isPartial)` → `if (!isPartial)` | 缺席桶两挡互换 | lib `:161`（`'unknown'` ≠ `'confirmed-zero'`）；lib `:173`（反向）；组件 `:447`（`$0.00`）；组件 `:464`（`说不清`）；组件 `:487`（`queryByText("$0.00")` 缺席断言） | 42 条绿，含全部「桶存在」用例与四行正向用例 |
| M3 | `Sub2ApiFinanceOverview.tsx` 四行**整体还原成旧的写死实现**（`—`/`未接入`） | 新增/改动的用例全红 | **10 条全红**（`:303 :381 :408 :433 :445 :462 :485 :502 :516 :542`） | 18 条绿（周区间、日期清空、利润桥等与本片无关的用例） |
| M4 | `<strong>不是当期收入</strong>` → `不是本月收入` | 措辞用例 | `:521` `expected '…不是本月收入…' to contain '不是当期收入'` | `:516` 锚点绿、`:520`「用户充值与支付事件」绿 → 红精确落在收入那一句 |
| M5 | `rollup.unknownStatuses.length > 0` → `> 1` | 未归类告警用例 | `:505` `Unable to find … role "status"` | `:502` 锚点绿；其余 25 条绿 |
| M6 | `RECENT_EVENT_ROWS` 里「退款」的桶 `退款与冲正` → `失败` | 四行正向用例 | `:387` `Unable to find … $40.00`（退款行金额） | `:381` 锚点（`$4,180.00`）绿、成功充值/待处理/失败三行断言绿 |
| M7 | `PaymentDaySnapshot` 的 `range.from === range.to` → `!==` | 口径说明的出现/不出现两支 | `:557`（周区间该出现却没出现）、`:575`（单日区间不该出现却出现了，`expected <strong></strong> to be null`） | 29 条绿 |
| M8 | 「口径不覆盖，**不是数据缺失**」→「不是数据丢失」 | 非单日说明用例 | `:560` | 同一用例内 `:558`「所选区间是 2024-02-12 至 2024-02-18」绿 → 红精确落在「不是数据缺失」那半句 |
| M9 | 「最近事件」副标题「口径是**区间合计**」→「区间总计」 | 两组口径标注用例 | `:541`（`eventsText` 不含「区间合计」） | 同一用例内「支付日快照」三条断言全绿（`sub2api.payments.daily`／「单个业务日」／「不是同一个数的两个答案」）→ 证明两个区块是分别断言的，快照段里同样出现的「区间合计」没有把事件段的断言蒙混过去 |
| M10 | `coverageBadgeFor` 的 `range.from === range.to` → `!==` | 两种徽章互换 | `PaymentSummaryCards.test.tsx:155`（周区间没显示「仅支持单日」）、`:171`（单日区间没显示「未接入」）、**`:183`**（周区间不该有「未接入」，`expected <span…> to be null`）、**单日区间不该有「仅支持单日」**（同款缺席断言）；连带 `:68`、既有「未初始化的指标」用例 | 6 条红全部与徽章有关；其余用例（真实金额、NewAPI 不适用、月累计等）全绿 |
| M11 | `UnavailableCard` 的 `badge = "未接入"` 默认值 → `"暂缺"` | 「badge 缺省行为未变」用例 | `PaymentSummaryCards.test.tsx:68`、`:90`、`:171`，以及 `FinanceOverview.test.tsx:297` | 周区间的「仅支持单日」用例**保持绿**（它显式传了 badge，不走默认）→ 证明红确实来自默认值，不是任何改动都会红 |

**关于「红在对的行上」**：M1/M2/M4/M5/M6 的红都落在**要证明的那条断言**上，
各自的 `await` 锚点先通过。**M3 是例外且是刻意的**——旧实现一个数字都不渲染，
锚点本身就满足不了，所以它红在锚点上。M3 证明的是「这些用例在旧实现下不会
照样绿」（派工点名要问的那一条），**逐条断言的精确性由 M1/M2/M4/M5/M6 承担**。

**缺席型断言全部单独成条并变异验证**，理由：与正向断言写在同一条用例里时，
前面的 `getByText` 会先抛出，缺席断言根本跑不到，变异什么都证明不了。
为此拆出五条独立用例：
- 「跨币种时页面上不出现任何『加起来了』的金额」→ M1 红在 `FinanceOverview.test.tsx:436`；
- 「汇总被标记为不完整时，缺席的桶一个数字都不给」→ M2 红在 `:487`；
- 「单日区间不出现那段『口径不覆盖』的解释」→ M7 红在 `:575`；
- 「周/月区间的卡片上不再出现『未接入』」→ M10 红在 `PaymentSummaryCards.test.tsx:183`；
- 「单日区间的卡片上不出现『仅支持单日』」→ M10 同样变红。

**`waitFor` 没有套在任何缺席断言外面**：一律先 `await` 正向锚点
（`findByText("$99.00")` / `findByText("$4,180.00")`），再做同步 `queryByText`。

**一条未做代码变异的缺席断言**：「订单端点未挂载时」用例里的
`queryByRole("table", { name: "最近事件" })).toBeNull()`。它不恒真的证据是
**同一个定位器在其它 6 条用例里都取到了这张表**（`eventRow()` 就是靠它），
所以不是一个永远匹配不到的查询。要做代码变异得改 `ApiStateView`，超出本片范围。

---

## 七、门禁（全部自己跑通）

```
pnpm --config.verify-deps-before-run=false -r run typecheck   → 5/5 Done
pnpm --config.verify-deps-before-run=false -r run test        → 全绿
pnpm --config.verify-deps-before-run=false -r run build       → exit 0
```

`test` 明细（第三轮，含卡片徽章）：**全绿**——`admin-web` **136 files /
1982 tests passed**；`ui-admin` 17/261；`ui-primitives` 7/16；
`design-tokens` 1/10；`ui-storybook` 无单测。

第二轮时那 3 条红（`pages/RegistryPage.test.tsx` 2 条 + `pages/SupplyDetailPages.test.tsx`
1 条）是 `plat-readonly-queries` 的在制品，**本轮已由那一片自己修好**，
现已全部转绿。当时的隔离判据（零 import 路径 + 宿主源码是别人的 M/?? +
上一轮同一套文件全绿）事后得到印证。

> ### ⚠ 那 3 条红**不是本片造成的**，是同 worktree 里其它 agent 的在制品
>
> 失败集中在两个文件：`pages/RegistryPage.test.tsx`（**untracked，`??`，
> 别人刚新建**）与 `pages/SupplyDetailPages.test.tsx`（`M`）。断言内容是
> 「Relay 甲」「订阅批次：付款、摊销与有效期」「这是计量型账号」「上游登记簿里
> 没有这一条」「这个上游不属于 NewAPI」——全在**上游登记簿 / 订阅批次 / 成本
> 登记簿**这条线上，与支付、资金概览无关。
>
> 判据：`grep -nE "paymentStatusRollup|Sub2ApiFinanceOverview|PaymentStatusRollupTable"`
> 对这两个测试文件与 `RegistryPage.tsx` **零命中**，没有任何 import 路径通到
> 本片改的东西。它们的宿主源码（`RegistryPage.tsx`、`SupplyDetailPages`
> 相关、`ChangesPage.*`、`UpstreamDetailPage.tsx`）在 `git status` 里都是别人的
> M/??。本片第一轮门禁时全仓还是 135 files / 1946 全绿，这一轮变成 136 files
> （多出的正是那个新建文件）。
>
> 另外：单独跑这两个文件时失败**更多**（5 条），说明它们之间还有顺序/污染
> 问题。这同样是那条线自己的事，本片不动（也不该动——文件正被人编辑）。
>
> **本片自己的 3 个测试文件 59/59 全绿**
> （`FinanceOverview.test.tsx` 31、`paymentStatusRollup.test.ts` 19、
> `Sub2ApiOrdersPanel.test.tsx` 9）。

**build 的坑（派工点名）**：本次 `ui-storybook` **没有**在 libuv 拆卸时崩，
它打印了 `Storybook build completed successfully` → `Done` 后 `pnpm -r` 才继续。
**`admin-web build` 确实跑到了**，确认方式（日志存于 scratchpad `build.log`，
`grep` 逐条核对）：

```
web/apps/admin-web build$ tsc --noEmit && vite build
web/apps/admin-web build: ✓ 365 modules transformed.
web/apps/admin-web build: dist/assets/index-BXYS-3_O.css     42.10 kB │ gzip:   8.24 kB
web/apps/admin-web build: dist/assets/index-s4-Pyybz.js   1,381.71 kB │ gzip: 407.74 kB
web/apps/admin-web build: ✓ built in 297ms
web/apps/admin-web build: Done
=== EXIT: 0 ===
```

（第一轮是 359 modules，第二轮 365——增量来自其它 agent 新加的模块，不是本片：
本片的 `PaymentDaySnapshot` 是同文件内的函数，不新增模块。）

即：不只看 `Done`，而是核到**产物行 `✓ 359 modules transformed` + 两个 dist 资产
+ 进程退出码 0**。`grep -iE "libuv|assertion|abort|ELIFECYCLE|failed"` 零命中。

**没跑 `go test`**（后端一行未动，且另一个 agent 在用同一个测试库）。

---

## 八、files_changed

只动前端，6 个文件，全部在派工许可清单内：

| 文件 | 改动 |
|---|---|
| `web/apps/admin-web/src/lib/paymentStatusRollup.ts` | 桶增加 `currencies`/`amountGap`、跨币种 fail closed、汇总级 `currency`；新增 `presentPaymentBucket` 与 `PaymentBucketCell`/`PaymentBucketCoverage` |
| `web/apps/admin-web/src/components/Sub2ApiFinanceOverview.tsx` | 「最近事件」占位 → `RecentPaymentEvents`（含 `RECENT_EVENT_ROWS`、`RecentEventRow`、`COVERAGE_BADGE`、`eventAmountText`）；六卡外包 `PaymentDaySnapshot` 做口径标注 |
| `web/apps/admin-web/src/components/PaymentStatusRollupTable.tsx` | 金额列：「—」旁写出原因；**去掉 `row.currency \|\| "CNY"` 兜底**（见 risks） |
| `web/apps/admin-web/src/lib/paymentStatusRollup.test.ts` | +10 条（跨币种 5 条、`presentPaymentBucket` 6 条） |
| `web/apps/admin-web/src/components/FinanceOverview.test.tsx` | 新增 `rawOrdersPage`/`stubFinanceFetch`/`eventRow`；新增 12 条（9 条最近事件 + 3 条口径标注）；改 2 条既有用例 |
| `web/apps/admin-web/src/components/Sub2ApiOrdersPanel.test.tsx` | +1 条跨币种；既有「整桶没有金额」用例补一句原因文案断言 |
| `web/apps/admin-web/src/components/PaymentSummaryCards.tsx` | **team-lead 放行后**：`UnavailableCard` 加可选 `badge`（默认「未接入」）+ 新增 `coverageBadgeFor`；`BucketCard`/`FeeCard` 非单日分支传「仅支持单日」 |
| `web/apps/admin-web/src/components/PaymentSummaryCards.test.tsx` | +4 条（徽章正向 1、缺省行为 1、缺席断言 2）；一条既有用例改名（原名说「六卡都显示未接入」已不准确） |

**未触碰**：任何 `internal/` 或 `cmd/` 下的 Go 文件、`K:/发票/`、
`pages/RegistryPage.tsx`、`pages/ChangesPage.tsx`、`components/ChannelProfitView.tsx`、
`components/financeShared.tsx`、`components/NewApiFinanceOverview.tsx`。

⚠ `git status` 里另有 `cmd/platform-api/main.go`、`db/queries/registry.sql`、
`internal/platform/{finance,httpapi,registry}/…`、`db/embed.go`、
`internal/platform/lifecycle/` 等 M/?? 条目——**不是本片改的**，是同一 worktree
里其它 agent 的在制品。`git diff --stat -- web/` 可确认本片改动只有上表 6 个文件。

---

## 九、risks

1. **改了兄弟组件的行为（刻意，需知会）。** `PaymentStatusRollupTable` 现在对
   跨币种桶显示「—」+ 原因，而不是像以前那样给出一个加好的数并标上先遇到的
   币种。这是纠正一个**静默错误**，但它是「充值订单」页签的可见变化。
2. **顺手修了一处 `|| "CNY"`。** 原来币种为空时兜底成 CNY，会给一笔不知道币种
   的钱画上人民币符号——`formatMinorUnits(x, "")` 本来就会诚实地显示
   「（最小单位，金额单位未知）」。它就在我正在重写的那个单元格里，留着等于在
   自己刚改的行上留一句看不出来的假话，所以一并改了。**这一条严格说超出派工
   范围**，若不认可，回滚这一处不影响其余改动。

> **2026-09-08 team-lead 批准保留**，理由：两处都是在修静默错误的钱（跨币种
> 求和后贴先遇到的币种符号 / 币种未知时兜底成 CNY），变化方向是从错到对。
>
> 上面 1、2 两条是**提交时的原文，刻意不改**——审读的人该看到「当时的判断」
> 与「裁定」两件事，而不是一段被事后修饰过的文字。

3. **同一页现在有两组「四个桶」，来源不同。** 上方 `PaymentSummaryCards` 六卡读
   `sub2api.payments.daily` **指标**（**只支持单日**，周/月显示「未接入」）；
   下方「最近事件」读**逐笔订单端点**的 `stats_by_status`（**跟随所选区间**）。
   选周/月时上面「—」下面有数，是两条通道口径不同的正常结果，不是 bug；
   已在区块副标题标出来源与窗口（`来源 … · 窗口 … 至 … · 本区间共 N 笔`）。
   但这仍是**两个数摆在一页上**，读者可能问「哪个对」——建议产品侧确认要不要
   合并成一条通道（见 follow_ups）。

> **2026-09-08 产品负责人裁定：两组都留，按「全面」办**，不收敛成一条通道，
> 改为在界面上标清各自口径。已实现，见第五之二节。**残留** `PaymentSummaryCards`
> 的徽章一处待放行，见 follow_ups 第 1 条。上面第 3 条同样保留原文。
4. **区块会多加载最多 50 条订单行却不展示**（`useSub2ApiOrdersQuery` 是
   `useInfiniteQuery`，`limit=50`）。只为拿 `stats_by_status`。与「充值订单」
   页签共用缓存，用户两个页签都看时不产生额外请求；只看概览时是一次
   50 行的响应。可接受，但若要优化需要后端提供一个只回汇总的参数（后端只读，
   本片不动）。
5. **`freshness` 缺席会抛。** `listPlatformOrders` 里 `body.freshness as FreshnessContract`
   是无校验强转（既有代码）；响应缺 `freshness` 时本区块会崩。与
   `Sub2ApiOrdersPanel` 的既有暴露面相同，本片没有单独加防御（加了会掩盖契约
   违例，且与兄弟组件不一致）。测试替身按真实契约提供了完整 `freshness`。

---

## 十、follow_ups

1. ~~**待放行：`PaymentSummaryCards.tsx` 三处，把非单日区间的徽章从「未接入」
   改成「仅单日口径」。**~~ **2026-09-08 team-lead 放行，已实施**（徽章文案
   定为「仅支持单日」），见第五之二节第 4 点。下面的原始报备内容保留备查： 产品负责人已裁定「两组都留、按全面标注」，第五之二节
   在组别层做完了；**残留的是每张卡自己的徽章**——非单日区间时它和真·未接入
   长得一样，而这两件事的下一步相反。需要改的确切位置（该文件当前**没有**
   其它 agent 的改动）：

   | 行 | 现状 | 建议 |
   |---|---|---|
   | `PaymentSummaryCards.tsx:80-90` | `UnavailableCard` 里 `status={<Badge tone="neutral">未接入</Badge>}` 写死 | 加一个可选 `badge?: string`，默认仍是「未接入」——不传的调用点行为不变 |
   | `:143-154` | `BucketCard` 早返回，非单日分支已给出「周/月需要按天聚合…」的 note，但徽章仍是「未接入」 | 该分支传 `badge="仅单日口径"` |
   | `:201-212` | `FeeCard` 早返回，同上 | 同上 |

   **影响面**：该组件被 NewAPI「资金与订单」共用，改动会同时生效——那边存在
   同样的混淆，方向上是好事，但需要你知情后再放行。**未经放行我不动这个文件。**

2. **产品裁定已落地：六卡与「最近事件」不收敛，改为各自标注口径**（第五之二节）。
   若日后仍决定收敛成一条通道，本片的标注可整段删除，不留残迹。
2. **`stats_by_status` 的汇总粒度确认。** 契约说它覆盖整个查询区间、不随翻页
   变化；本片按此实现（并据此把「桶缺席 + 完整」判为确认过的零）。建议在真实
   实例上核一次周/月区间的返回，确认不是只覆盖首页。
3. **`is_partial` 的成因目前无法区分**。契约列了两种（Sub2API 非合约币种、
   NewAPI 未识别渠道），但响应里只有一个布尔。文案因此只能把两种都写出来。
   若后端将来给出成因字段，`PARTIAL_AMOUNT_NOTE` 可以说得更准。
4. **`listPlatformOrders` 的 `freshness` 强转**（见 risks 5）值得单开一片，
   连同 `Sub2ApiOrdersPanel` 一起处理，不宜在本片单侧加防御。
