# XM-SUB2API-PROFIT：Sub2API「利润核算」接线（与 NewAPI 共用一张表）

- **status:** implemented，**未提交**（按派工要求不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`（同一 worktree 内另有三个 agent
  在改别的文件，本片只动了下面 5 个）。
- **commit:** —（工作区改动）
- **来源：** 派工 XM-SUB2API-PROFIT。缺口是
  `PlatformFinancePanel.tsx` 的 `case "profit"` 是纯占位，而
  NewAPI 的同名子页早就用同一个端点做出来了。

## summary

把 `NewApiFinanceOverview.tsx` 里的 `ProfitView` 抽成平台参数化的
`ChannelProfitView`，两个平台共用。**没有新做一张表**，也没有接新端点——
Sub2API 这一格用的是 NewAPI 侧已经验证可用的那条路
（`GET /api/v1/finance/channels/summary`，无条件挂载，一次返回两个平台的行，
按 `system_type` 区分）。

## files_changed

| 文件 | 改动 |
|---|---|
| `web/apps/admin-web/src/components/ChannelProfitView.tsx` | **新增**（401 行）。平台参数化的利润核算表 |
| `web/apps/admin-web/src/components/ChannelProfitView.test.tsx` | **新增**。10 条 |
| `web/apps/admin-web/src/components/financeShared.tsx` | **新增**（119 行）。两个子页签共用的四样东西 |
| `web/apps/admin-web/src/components/NewApiFinanceOverview.tsx` | 搬走 301 + 91 行；改用新组件与共享模块 |
| `web/apps/admin-web/src/components/PlatformFinancePanel.tsx` | `case "profit"` 换成 `<ChannelProfitView platform="sub2api" />` |
| `web/apps/admin-web/src/components/PlatformFinancePanel.test.tsx` | +1 条接线用例 |

**`NewApiFinanceOverview.test.tsx` 一条未改**（`git diff --stat` 对它为空），
全部 13 条原样全绿。这是本片的安全网，没有被动过。

## 搬了什么、留了什么

先逐个 grep 确认「是不是也被 `OrdersView` 用着」，再决定搬还是留。

**搬进 `ChannelProfitView.tsx`（只服务于 `ProfitView`）**——`PROFIT_COLUMNS`、
`formatMargin`、`amountCell`、`formatScaled`、`hostOf`、`accessMethodText`、
`statusText`，以及派工没点名但同样只有它在用的表壳一整套：
`InteractiveLedgerTable`、`LedgerColumn`、`LedgerFilter`、`rowKeyFor`、
`filterValuesForRow`。

> 表壳必须一起搬，否则新组件要反过来从 `NewApiFinanceOverview` 里 import
> 一个通用表格，依赖方向就彻底拧了。

**搬进 `financeShared.tsx`（两个子页签**确实**共用）**——
`useFinancePeriod`（+ `FinancePeriodState`）、`DemoBanner`、`RefreshErrorNotice`，
外加 `useFinancePeriod` 自己依赖、而 `NewApiFinanceOverview` 也要用的
`validDateOnly` 与 `businessTodayDateOnly`。

> 第一版是按「被共用的就留在原处并 export」做的，结果是
> `NewApiFinanceOverview` ⇄ `ChannelProfitView` 一个模块环。验收线 2026-09-07
> 拍板断环——理由见下面「三个要说明的决定」第 2 条。这不违背「别盲搬」：
> 那条针对的是「只服务单一调用方的东西别搬」，而这四样恰恰是两边共用的。

**同名但不是同一个东西，一个没碰**：`hostOf` / `statusText` 在
`ChannelTableColumns.tsx`、`ManagedChannelTable.tsx` 里各有一份局部定义，
`amountCell` 在 `platformOrdersColumns.tsx`、`PlatformUsersPanel.tsx` 里也各有
一份。它们与本片搬的那几个只是重名。

**顺带发现（未处理）**：`NewApiFinanceOverview.tsx` 的 `formatCount` 与
`toIntegerValue` 在我改之前就已经是未被引用的 import（`noUnusedLocals` 没开，
所以不报错）。不是本片造成的，没动。

## 平台差异只有一处

```ts
const rows = (summaryQuery.data?.items ?? []).filter((item) => item.systemType === platform);
```

文案走一张映射表而不是一串三元：

```ts
const PLATFORM_NAME: Record<ChannelProfitPlatform, string> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};
```

平台名出现在**六处**（说明段落、刷新失败标签、表 `aria-label`、空态标题、
空态说明、演示横幅）。散开写的话，下次加平台漏掉其中一处不会报错，
只会让某一行文案说着另一个平台的名字。

**表标题 `title` 仍是「利润核算明细」，两个平台相同**——NewAPI 原文里它本来
就不含平台名，而 NewAPI 侧要求逐字不变（既有用例断言 toolbar 的
`aria-label` 是「利润核算明细筛选与搜索」）。平台名落在 `tableLabel`
（表格自己的 `aria-label`）上，那里两边是对称的。

## 三个要说明的决定

### 1. `DemoBanner` 加了一个必填的 `platformName`（派工没要求）

那句横幅原文写死着「请在「连接与凭据」配置真实 **NewAPI** 实例」。它是
`OrdersView` 与利润表共用的组件，照搬到 Sub2API 视图会把人指到另一个平台的
凭据页去。所以按派工留在原处 + export，但签名加了 `platformName`。

**NewAPI 侧渲染出来的字节完全没变**：两个调用点传 `platformName="NewAPI"`，
源码里那行从 JSX 文本节点改成了模板字面量（合并成单个文本节点，
`getByText` 更稳），渲染结果逐字相同。

做成必填而不是默认 `"NewAPI"`：默认值会让下一个平台忘了传时静默说错话。

### 2. 共用的四样进 `financeShared.tsx`，两个页签谁也不 import 谁

第一版按派工「被共用的就留在原处并 export」做，结果是
`NewApiFinanceOverview` → `ChannelProfitView`（渲染它）
→ `NewApiFinanceOverview`（拿共用辅助）这样一个模块环。

那一版**能跑**（vitest + rolldown 生产构建都过、无环告警），但它能跑靠的是
「被引用的都是提升的函数声明、且只在 render 期调用」——**实现细节在兜底，
不是设计成立**。谁哪天把 `DemoBanner` 改成 `const DemoBanner = () => …`，
或者打包器换个求值顺序，它就在运行时炸，而且炸在一个跟改动看起来毫不相干
的地方。验收线 2026-09-07 拍板断环，采用现在这个形状：

```
NewApiFinanceOverview ──┐
                        ├──> financeShared   （financeShared 谁也不 import）
ChannelProfitView ──────┘
NewApiFinanceOverview ─────> ChannelProfitView  （单向，只为渲染 profit 子页签）
```

**验证**（不只看直接反向边，跑了整个 `src/` 的传递闭包环检测，201 个源文件）：
涉及本片三个文件的环 **0 个**。顺带查出仓库里另有 3 个既有的环，都不是本片
碰的：`api/client.ts ⇄ auth/session.ts`（两条路径）与
`api/connectors.ts ⇄ lib/connectorForm.ts`。

`financeShared.tsx` 刻意**只收确实被两边共用的东西**——只服务单一调用方的
辅助不要往里放，那会把它变成又一个什么都能装的 `utils`。文件头注释写了
这条纪律和断环的理由。

### 3. `initialDate` 不再重算一次「今天」

`ChannelProfitView` 的 `initialDate` 默认空串，直接交给 `useFinancePeriod`——
它内部本来就有「不合法就回落到当前业务日」这条。省略 / 不合法 / 合法三种
情况的结果与 `NewApiFinanceOverview` 原来先归一化一遍完全相同。
`PlatformFinancePanel` 因此不必自己找一个日期来源（它也没有）。

## tests_run

新增 11 条（`ChannelProfitView.test.tsx` 10 + `PlatformFinancePanel.test.tsx` 1）。

**混合数据用例是主用例**：同一份夹具喂进 2 条 sub2api + 2 条 newapi 行，
两个平台各渲染一次，断言互为镜像——本平台的渠道名与金额都在、另一平台的
渠道名与金额都不在、**行数也钉住**。夹具里两边的金额两两不同
（¥200/¥150/¥50/25.00%/¥900 对 ¥100/¥70/¥30/30.00%/¥400），
同一个数字出现在两边的话过滤器坏掉也可能照样绿。

### 变异验证（四项，全部按预期变红；断环重构后重跑了一遍，结论不变）

| 变异 | 期望 | 结果 |
|---|---|---|
| 删掉 `.filter((item) => item.systemType === platform)` | 混合数据 2 条 + 空态 2 条 + 筛选 1 条 + 接线 1 条变红，且 NewAPI 既有那条也变红 | **红 7 条** |
| `case "profit"` 恢复成旧的 `pending(...)` 占位 | 新加的「Sub2API 利润表显示了行」变红 | **红**（`Unable to find role="table" and name "Sub2API 渠道利润核算"`） |
| `PLATFORM_NAME.sub2api` 写死成 `"NewAPI"` | 所有带平台名的逐字断言变红 | **红 6 条** |
| `DemoBanner` 里的 `${platformName}` 写死回 `NewAPI` | 演示横幅那条变红 | **红 1 条** |

第二项是派工点名要的那条检查：**这条正向断言在旧实现下不会照样绿**。

第三项顺带证明了文案断言不是摆设——包括那条容易恒真的
`queryByText(/配置真实 NewAPI 实例/)` 缺席断言（演示横幅用例里有一个
「非演示来源不挂横幅」的对照组，确认横幅不是无条件不出现）。

其余覆盖：空态两句文案逐字（含「暂无可展示记录；空态不代表金额为 0。」）、
八个列头、筛选在搜索之前、上游下拉的选项**只来自本平台的行**、
金额缺席显示「—」且一个 0 都不长出来、首次读取失败的文案**带错误码**
（`暂时不可用（错误码 UPSTREAM_UNAVAILABLE）`）且重试后恢复本平台空态。

## 门禁

**断环前**（2026-09-07 上午）三条全绿：typecheck 5 个包全 Done；
test admin-web 127 文件 1834 条 + ui-admin 261 + ui-primitives 16 +
design-tokens 10 全绿；build admin-web + ui-storybook 均成功。

**断环后重跑**时，同一 worktree 里另外两个 agent 的在写文件让共享门禁变红了，
**两处都与本片无关**，我没有碰它们：

| 挡住的地方 | 谁的 | 与本片的关系 |
|---|---|---|
| `src/components/ChannelBindingHistory.test.tsx:92` `TS2493` | 新增未跟踪文件，只 import `./ChannelBindingHistory` | 无 |
| `src/pages/IdentityPage.test.tsx` 5 条失败 | 新增未跟踪文件 + `IdentityPage.tsx` 改动 | 无（`grep` 确认不引用本片任一模块） |

用排除法把本片的结论单独取出来：

```
# typecheck：只排除那一个别人的文件，其余全项目
npx tsc -p <临时 config，exclude ChannelBindingHistory.test.tsx> --noEmit   # exit 0
# test：只排除别人那一个测试文件
npx vitest run --exclude "src/pages/IdentityPage.test.tsx"                  # 132 文件 1876 条全绿
# build：tsc 部分同上已验，单跑打包
npx vite build                                                             # 358 modules，✓ built，无环告警
# 另：ui-admin 261 / ui-primitives 16 / design-tokens 10 全绿
```

**给合入的人**：等那两个 agent 的文件落定后，`-r run typecheck/test/build`
三条要原样再跑一遍确认。

## `finance/profit-daily` 的结论：**新端点没人接，不是旧端点被废弃**

占位文案里那句「数据来自 XM-0037 成本台账（finance.profit-daily 端点已有）」
指的端点确实存在，且**全前端零调用**（`grep` 整个 `web/` 只在那句占位文案本身
和几处无关的 `source: "finance.profit_daily"` 字符串里出现）。查下来：

- **没有废弃标记**。`internal/platform/httpapi/profit_daily.go` 里没有任何
  Deprecated；`router.go:595` 无条件挂载；`docs/modules/finance/README.md`
  §426 仍把它写成一条现行 Query；`dbroles/policy.go` 仍在维护它的列权限；
  `XM-ALERTS-LIST-TRUNCATED` 那一片还动过它的 `Limit` 上限。
- **它比这一页需要的粒度细一档**。台账一行是
  `(upstream_account_id, business_day, token_id)`；
  `channels/summary` 读的是**同一张表按渠道聚合后的投影**
  （见 `docs/superpowers/plans/2026-08-28-xm-0037-cost-accounting-design.md`
  第 440 行的字段映射）。两者不是替代关系。
- **两者的默认窗口刻意不同**。README §1000 原话：
  「默认窗口是**今天**（不是 profit-daily 的 7 天）：这两个端点喂的是
  『今日毛利』那几张卡，默认给 7 天合计会让卡上的数字是标题的七倍」。
- 时间线也支持这个读法：profit-daily 是 XM-0037b（`1b779ba`）落的，
  只有那一个 commit；`channels/summary` 是后来的 XM-0037d（`62399e3`）
  为「看板供数」补的。

**所以：profit-daily 是一条健在的逐行台账下钻端点，只是至今没有哪个页面需要
逐行下钻，因而没有前端消费方。**本片按派工没有去接它，占位里那句指错方向的
供数说明已经随占位一起删掉，`PlatformFinancePanel.tsx` 的注释里留了一行说明
本片走的是 `channels/summary`。

## risks

- **`businessTodayDateOnly` 在仓库里现在有两份同样实现的导出**：
  `financeShared.tsx`（本片）与既有的 `Sub2ApiOrdersPanel.tsx`
  （`Sub2ApiRefundsPanel` 与 `pages/FinancePage` 从那边取）。合并会牵动
  `components/` 以外的文件，超出本片范围；`financeShared.tsx` 的文件头
  已经把这条标出来了。
- **两个平台共用同一个 react-query key**
  （`["finance","channels","summary",from,to]`）。这是刻意的——端点本来就
  一次返回两个平台，共用缓存避免同一天请求两遍；代价是两边的过滤发生在
  组件内而不是请求里。
- **端点本身没有平台参数**。Sub2API 若某天需要按平台在服务端过滤，
  要改的是端点而不是这个组件。
- 演示横幅的判定读 `observed.source`；`finance-collect-staging` 在
  `DEFAULT_DEMO_SOURCES` 里，Sub2API 的采集来源若将来换名字，
  要回 `lib/demoData.ts` 加一行（那个文件的注释已经写了这条纪律）。

## not_run

- 后端 `go test ./...` / `go vet`：本片一行 Go 都没动，没跑。
- `scripts/check-governance.sh`：同上，且本 worktree 里另有三个 agent 的
  未提交改动混在一起，全仓治理检查的结果归不到本片头上。
- 浏览器里的真实渲染：只跑了 jsdom 测试。

## follow_ups

- **`ChannelProfitView` 现在支持的两个平台是硬编码的联合类型**。CPA 与
  服务器没有上游账号，给它们挂一张恒为空的利润表等于把「这里本来就没有这个
  概念」显示成缺口（README §1000 同一条理由）——这是刻意不做，不是漏做。
- 若要给这一格加「逐行下钻」（点开一个渠道看它按天 / 按 token 的台账行），
  那才是 `finance/profit-daily` 的第一个真实消费方。验收线已把它登记成后续
  切片；**别按「与 channels/summary 重复的废弃端点」处理**，那会误删一条有用
  的路。
- 仓库里另有 3 个既有模块环（`api/client.ts ⇄ auth/session.ts` 两条路径、
  `api/connectors.ts ⇄ lib/connectorForm.ts`），本片没碰。本片用的环检测脚本
  是一次性的（`src/` 下相对 import 的传递闭包 + DFS），值得的话可以固化成
  一条门禁。
