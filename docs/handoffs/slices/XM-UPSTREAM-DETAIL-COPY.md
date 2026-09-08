# XM-UPSTREAM-DETAIL-COPY：上游详情接真实数据 + 一批指错方向的文案

- **status:** implemented，**未提交、未推送**（按派工要求改完即停）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `8c446e5`。
- **范围：** 纯前端。后端 `internal/`、`cmd/`、`db/` **一行未动**（只读核对）。
- **来源：** 派工「上游详情页整片写着『读契约尚未接入』，而读契约早就有了；外加
  D1–D5 五处过期文案 + 全 `web/` 残留 grep」。

---

## 〇、先说与派工前提不符的两处（都不影响结论，但影响做法）

**一、行号漂了一处。** 派工说 `ListUpstreamAccountsHandler` 在
`internal/platform/httpapi/router.go` 约 584 行，实测在 **633 行**。
`finance.ts` 的 `UpstreamAccountItem`（449–480）、`UpstreamDetailPage.tsx` 的
「只读结构预览」（第 80 行）都与派工一致。

**二、「余额/消耗/利润接哪条」派工留了判断题，答案是两条都能对上，而且对得很死。**
派工要求「先自己确认这两条端点返回什么、能不能按上游 id 对上」。核对结果：

`/finance/upstreams/summary` 与 `/finance/channels/summary` **遍历的是同一个
`finance.UpstreamSummary` 列表**，两个 DTO 的 `ID` 字段都取
`s.Account.ID.String()`（`internal/platform/httpapi/finance_summary.go`
`channelToItem` / `upstreamToItem`）。也就是说三条端点的行 id **就是同一个**
`finance.upstream_account.id`，按 id 定位不是凑合，是精确匹配。

两条不是重复：上游投影多给 `supplier_key` 与 `runway`（余额/日均/可用天数），
渠道投影多给 `gross_margin`、`platform_id`、`metered`、`business_day_tz`。所以
两条都接了，各取各的（第二节逐格列出）。

---

## 一、上游详情页：逐格接了什么 / 接不了什么

文件：`web/apps/admin-web/src/pages/UpstreamDetailPage.tsx`（整页重写）。

### 1.1 顶部五格

| 格 | 数据源 | 说明 |
|---|---|---|
| 接入平台 | 路由平台段 | 原样保留，本来就是真的 |
| 接入渠道 | `/upstreams/summary` 的 `supplier_key` 归并计数 | **键只从汇总行里取，前端不拼一遍**。今天多半是 1，那是 `db/migrations/000008_finance_registry.up.sql:131` 的唯一索引 `(environment, system_type, base_url) WHERE base_url IS NOT NULL` 使然，不是漏做聚合——后端注释里写着同一句话 |
| 上游余额 | `summary.runway.balance` | 给不出时显示 `runwayReasonText(runway)` 那五句里的一句，不给统一的「暂无数据」 |
| 本期总消耗 | `summary.usageRevenue` | 副行带窗口 `from ~ to`（后端不给 from/to 时只取今天） |
| 本期总利润 | `summary.grossProfit` | 同上 |

### 1.2 四张明细区（原来 30 多格全「未接入」，现在按字段逐格判）

- **上游资料**：`upstream_name` / `base_url` / `upstream_contact` / `status` /
  `environment` / `upstream_group` / `updated_at` 直接来自登记簿；
  「账号密码状态」走 `describeCredential(credential_ref)`（已配置 / 未配置）；
  新增「接入方式」（`describeAccessMethod`）。
  **仍标未接入的只有「上游账号」**——登记簿没有账号名字段，账号身份在凭据里
  （ADR-014），这里不拿网址或名称冒充账号。
- **充值比例与成本口径**：`recharge_ratio` / `recharge_cost_rate` / `group_rate` /
  `currency` / `business_day_tz` / `metered` 来自登记簿；
  `observed.costObservedAt` / `revenueObservedAt` / `source` 来自供给汇总；
  「毛利率」来自**渠道汇总**的 `gross_margin`。
  仍未接入两格，各带各的原因：「最近一笔比例」（登记簿只存当前值，没有比例
  历史表）、「比例更新时间」（只有整行的 `updated_at`，拿它冒充倍率时间会让
  「今天没改过倍率」看起来像改过）。
- **余额与预计补充**：`runway` 全套（余额 / 日均 / 天数 / 观测时刻 / 覆盖窗口）
  \+ `coverage`（完整度 + 收入/成本已知行数 + 币种混杂）。
  仍未接入两格：「订阅最近到期」（**刻意不合成**，每笔批次的起止就在下方
  「账号明细 · 订阅批次」里逐条列着）、「预计补充时间」（后端没有这个字段，
  由天数外推是前端造数）。
- **联系人与支持**：只有 `upstream_contact` 一个自由文本字段是真的；邮箱 /
  电话 / 沟通渠道 / 最近联系 / 支持等级全部标未接入，理由逐格写实
  （「不从自由文本里正则抠一个出来冒充结构化数据」）。

### 1.3 三张表

- **上游全部分组** → 供应商组内各账号的 `upstream_group` / `group_rate` /
  `status` / `system_type` / `token_mappings.length`。「可用模型」仍未接入。
  提示语改了：这张表列的是**平台登记簿记下的**分组，不是从上游系统拉回来的
  分组目录——上游侧的分组目录没有读契约。
- **关联渠道** → 真表。行 = 供应商组内各 id 在 `/channels/summary` 里的那一行，
  七列（平台 / 渠道 / 分组·Key / 供给成本 / 我方计费 / 毛利 / 状态）全部有值，
  渠道名链到渠道详情页。
- **上游凭据** → 账号级一行 + 每条令牌映射一行；类型 / 账号别名 /
  CredentialRef / 配置状态是真的。「最近轮换」「最近验证」仍未接入，理由精确到
  **在哪**：轮换时间在「设置 → 凭据管理」的凭据元数据（`updated_at`/`version`），
  本页不跨读那条端点；「最近验证」平台根本不采集——凭据管理只回答「此刻读不
  读得到」，那不是一个时刻（核对：`api/credentials.ts` 的 `CredentialMetadata`
  只有 `updated_at`/`fingerprint`/`version`/`available`/`revoked`，无到期字段）。

### 1.4 孤儿组件挂上了

`components/UpstreamAccountDetail.tsx` 自 `3c97057`（渠道管理合并成单表、删掉
`UpstreamAccountsPanel.tsx`）起**没有任何调用方**——全仓只有它自己、它的测试和
`InvoiceConsolePanel.tsx` 一句注释提到它。它带的四个退款/终止对话框
（`SubscriptionLifecycleDialog` × 4：批次 refund/terminate、代理 refund/terminate）
因此在整个后台里点不到。

本页作为它的新宿主挂载：`<UpstreamAccountDetail account={account} onDone={afterWrite} />`，
外加 `ActionResultNote` 回执条。**它一行没改**——重写等于把已有测试覆盖的那份
逻辑复制成两份。写完作废三个 query key（登记簿 + 两条汇总）。

### 1.5 找不到 id / 平台对不上怎么处理（派工点名要的）

页头与「返回上游管理」链接**始终在**，body 换成 `PageState kind="empty"`：

- **登记簿里没有这个 id**：标题「上游登记簿里没有这一条」，正文带上列表条数与
  查询的 id，并说明「登记簿没有按 ID 取单条的端点，这一页按 ID 在列表里定位」。
  与同族的 `ChannelDetailPage` 的「渠道目录里没有这一条」同一形态，不另立一套。
- **URL 平台段与 `system_type` 不一致**（如 `/platforms/newapi/suppliers/<一个
  sub2api 账号>`）：标题「这个上游不属于 NewAPI」，正文说出两边各是什么。
  **刻意不按 URL 的平台把它画出来**——那种情况下页面上每个数都是对的，唯独
  归属是错的，是最难被发现的一种错。

`queryKey` 用 `api/finance` 导出的 `UPSTREAM_ACCOUNTS_QUERY` /
`UPSTREAM_SUMMARY_QUERY` 常量，因为挂进来的 `UpstreamAccountDetail` 写完作废的
正是这两个 key。**遗留不一致见第五节 follow_up 1。**

---

## 二、五处过期文案的新措辞

### D1 `lib/workbench.ts` `focusRows()` 的「安全」行

旧：`凭据到期与权限异常随「人员与权限」页上线`
新：`凭据模型里还没有到期时间：CredentialRef 只登记 secret://<scope>/<name>，不记录签发与轮换到期。权限异常则还没有判定规则：员工账号与角色读得到，但没有任何一条规则说什么算异常。`

前半句**与 `WORK_CATEGORIES.expiring` 共用一个常量 `CREDENTIAL_EXPIRY_GAP`**。
这句话有过两份副本，上次订正只改了 `expiring` 那份——把它抽成一份，下次不可能
只改一半。后半句是自己核的：`StaffAccountsPanel` 读得到账号与角色
（`api/staff.ts`），但全仓没有任何一处算过「什么算权限异常」。

核对依据：`internal/platform/secrets/ref.go:17` 的 `CredentialRef` 只有
`scope`/`name` 两个字段；`api/credentials.ts` 的 `CredentialMetadata` 无到期字段。

### D2/A3 `pages/OverviewPage.tsx` 的「今日到期」格 —— **选了「拆格」**

**结论：收窄成「审批到期」，接上同页已有的 `approvalsQuery`（真数据）。**

派工给了两条路：接上审批+重试，或拆格。选后者，理由三条：

1. **重试不是一条截止线。** 后台任务的 `scheduled_at` 是「下次什么时候再试」，
   不是到期时刻；而工作台只查 `state=discarded`——那些**已经把重试用尽了**，
   根本没有未来时刻可数。把「下次尝试时间」和「审批到期时刻」加成一个数，
   得到的数没有含义。已放弃的任务在同一屏「失败任务」一类里逐条列着。
2. **轮换到期一个数都算不出来**（同 D1 的缺口）。三样合成一个数 = 恒偏低，
   而偏低的数与正确的数长得一模一样。
3. 收窄之后这一格对它声称的那件事是**完整**的，另外两样各自在同屏的
   「失败任务」与「即将到期」两类里说清楚——没有信息被丢掉。

口径落在新的纯函数 `approvalsDueSoonCount(items, now)`（`lib/workbench.ts`）：

- 窗口 `APPROVAL_DUE_WINDOW_HOURS = 24`，**滚动 24 小时而不是「今天」**——
  「今天」要先定按哪个时区切日（宪法 14 条），这一格没有业务日语义可依，硬挑
  一个时区会让同一批单在两台机器上数出不同结果。同「最近恢复」那格的口径。
- **已过期的不算**，复用 `lib/approvals` 的 `isEffectivelyExpired`（与
  `workItemsFromApprovals` 同一判据）：库里的 status 滞后于 `expires_at`，
  一张过期的单谁也批不动，算进「快到期了，去看一眼」只会让人白跑。
- `expires_at` 解析不出来的不算——我们断言不了它什么时候到期。

降级：审批端点没挂载 / 没权限时显示「—」+「读不到」徽章 + 一句说明，**不显示 0**
（0 会被读成「没有快到期的单」）；取满上限时副行追加「可能少算」。新增
「查看审批队列 →」入口。

### D3 `pages/FinancePage.tsx` `WRITE_FEATURE_LOCKS`（补单、对账纠正两处逐字重复）

旧：`等 Action Advanced Controls：按 ADR-003 属 L3/L4，Foundation-A 阶段内核不放行`
新（补单）：`等一个补单 Action 被注册：同退款，客户支付侧的写入口一个都没有注册`
新（对账纠正）：`等一个对账纠正 Action 被注册：同上，客户支付侧的写入口一个都没有注册`

依据（自己核的，没照抄派工）：`cmd/platform-api/main.go:237-246` 无条件构造
`approvalService` 并 `action.WithApprovalGateway(approvalService)`；
`internal/platform/action/kernel.go:212-240` 在 `k.approvals != nil` 时对 L2+
落审批单返回 `APPROVAL_REQUIRED`（HTTP 202），只有 `k.approvals == nil` 才回
`ADVANCED_CONTROLS_REQUIRED`。已注册的 finance Action 只有
`finance.upstream_account.set` / `finance.recharge_ratio.set` /
`finance.token_map.set` / `.remove`，全在上游成本侧。

### D4 `pages/FinancePage.tsx` 退款那条

旧：`等 Action Advanced Controls：按 ADR-003 退款属 L4，内核在没接审批中心时对 L2 及以上一律拒绝执行（ADVANCED_CONTROLS_REQUIRED）`
新：`等一个退款 Action 被注册：平台上已注册的 finance Action 全在上游成本侧，客户支付侧一个都没有。审批中心（XM-0030）已启用，L4 现在会落成审批单而不是被拒绝执行——所以内核不再是这件事的阻塞点`

派工说「从句字面仍成立但摆在解锁条件清单里会被误读」，同意；改法是把「内核
不是阻塞点」这件事**明说出来**，而不是把从句删掉了事。

### D5 `pages/FinancePage.tsx` `PENDING_TAB_COPY.exceptions`（`?sub=exceptions` 页顶）

旧：`…需 Action Advanced Controls（Foundation-B / XM-0030）接入后内核才放行。`
新：`…而平台已注册的 finance Action 全在上游成本侧，客户支付侧一个都没有。审批中心（XM-0030）已启用，L3/L4 现在会落成审批单——等的不是它，是那几个 Action 本身。`

### 顺带（同一句话的第二、三份副本，不在派工清单里）

- **`pages/FinancePage.tsx` 约 525 行**（「财务待办」卡落款）：
  旧 `审批链随 Foundation-B` → 新「审批中心已启用但这张卡还没读它，而且
  `/api/v1/approvals` 不支持按 `action_id` 前缀筛选，『只要财务相关的那些单』
  今天得整条队列拉回来自己过滤」。
  筛选能力是核过的：`ListApprovalsHandler`（`internal/platform/httpapi/approvals.go:113`）
  只解析 `status` 与 `limit`。
- **`lib/financeGlobal.ts` 约 260 行**：上面那句话的**第二份副本**，在
  `runwayAttentionItems` 的文档注释里。同步改成同一说法。
- **`pages/SettingsPage.tsx` 约 68 行**：旧「写入仍需 Foundation-B / C3c 的
  审批链」→ 新「阈值写入还没有注册对应的 Action——审批中心（XM-0030）已启用，
  L2 现在会落成审批单，缺的是那条写路径本身」。
  核对：全仓 `internal/` 没有任何写 R5 阈值的 Action（`grep runway` 只命中告警
  规则键 `upstream.runway.low`）。

---

## 三、grep 全 `web/` 的残留：处理了哪些、留了哪些

`grep -rn "Foundation-B\|Foundation-A\|Advanced Controls"` + `随…上线` 全量过了一遍。

### 已处理（本片）

`lib/workbench.ts`、`pages/OverviewPage.tsx`、`pages/FinancePage.tsx`（4 处）、
`lib/financeGlobal.ts`、`pages/SettingsPage.tsx`。

### 未处理，**需要下一片接手**（都核过、都还在误导人）

1. **`components/RunwayThresholdRulePanel.tsx:274` —— 用户可见的标题
   「Foundation-B / C3c 尚未开放」。** 这句话现在是错的：审批中心已启用，L2 会
   落审批单。真正的阻塞是**全仓没有注册任何写 R5 阈值的 Action**。
   **没动的原因：** 这个面板渲染在 `/alerts?sub=rules`，而本会话有另一个 agent
   （`alerts-gaps`）在告警线上；改它要连带改三处断言——
   `components/RunwayThresholdRulePanel.test.tsx:38`、
   `pages/AlertRulesPage.test.tsx:17`、`router.test.tsx:2743`——其中
   `AlertRulesPage.test.tsx` 大概率是对方的文件。同文件并发编辑在共享工作树里
   会互相覆盖，所以列进交接而不是硬改。
   `components/RunwayThresholdRulePanel.tsx:96` 的注释同一问题。
2. **`blueprints/governance.ts` 共 11 处 `随 Foundation-B 上线`。**
   - **229、260 行渲染在 FinancePage 上**（异常协同 / 金额语义与写入功能），
     属本片的页面，措辞与 D3–D5 同一类错误；
   - **424 / 440 / 447 / 463 / 470 / 475 / 482 / 496 / 518 行渲染在 ChangesPage 上**。
   **没动的原因：** `pages/ChangesPage.tsx` 在派工的「绝对不要碰」清单里，而
   `governance.ts` 正是那一页的数据文件——`ChangesPage.tsx:175-176` 的注释逐字
   写着「blueprints/governance.ts 里那五句今天都写着『随 Foundation-B（XM-0030）
   上线』」，说明对方正打算改它。`git status` 显示 `ChangesPage.tsx` 已被对方
   修改而 `governance.ts` 尚未——两人同时写同一文件会互相覆盖。
   **建议：** 由改 ChangesPage 的那位一并处理，229/260 两行别漏（它们不在
   ChangesPage 上，容易被当成不相干）。
3. **`pages/PlaceholderPage.tsx:21`（`/actions`）与 `:26`（`/changes`）** 仍写着
   「随 Foundation-B 上线」。**是死文案**：`PlaceholderPage` 的路由由
   `NAV_GROUPS` 里 `built: false` 的项生成（`router.tsx:347-349`），而
   `navigation.ts` 里只有四个 `/ext/*` 是 `built: false`——这两条永远渲染不出来。
   没动的原因：本会话有 `ext-app` / `ext-integration` / `ext-publishing` 三个
   agent，`PLACEHOLDER_COPY` 的 `/ext/*` 条目大概率在他们手上。零用户影响，
   可以随手清，但不值得冒并发覆盖的风险。

### 核过之后判定**不用改**的

- `api/alerts.ts:251`、`components/ObserveServiceDialog.tsx:24`、
  `lib/serviceForm.ts:173`：把 Foundation-A 当阶段名用，描述的事今天仍然成立
  （告警规则清单端点确实没有；手填水位确实还是过渡手段）。
- `components/BulkAcknowledgeAlerts.tsx:24`、`pages/ActionsPage.tsx:606`、
  `lib/workbench.ts:93`：注释在讲「这句话以前是错的、已经改了」，是订正记录本身。
- `router.test.tsx:680`：`blocked_reason: "需要 Action Advanced Controls（Foundation-B）"`
  是**测试夹具**，模拟服务端下发的 `blocked_reason`。ActionsPage 照实渲染服务端
  给的话，不是前端文案。真串在 `internal/platform/action/kernel.go:215-216`，
  而那一支（`k.approvals == nil`）对没接网关的嵌入者/测试**仍然是准确的**。
  后端不在本片范围。

---

## 四、测试与变异验证

### 4.1 新增/改写的用例

**`pages/SupplyDetailPages.test.tsx`**（「上游详情」整块 describe 重写，6 例）

- 登记簿事实与汇总金额都是真数据，且页面不再说「读契约尚未接入」
- 孤儿组件挂上了：订阅批次表与「记退款」/「终止」入口在这一页点得到
- 计量型账号不画订阅批次表，而是说清它的成本是怎么算的
- 登记簿列表里没有这个 ID 时说清楚，不给一张字段全空的详情
- URL 平台段与 `system_type` 对不上时，不按 URL 的平台把它画出来
- `suppliers/new` 与未知平台的 not-found 兜底（原有，保留）

夹具补了 `supplier_key`、`usage_revenue`/`supply_cost`/`gross_profit`、
`/finance/channels/summary`、`/finance/subscription-batches`。

**`lib/workbench.test.ts`**（+6 例）：`approvalsDueSoonCount` 的窗口边界 /
已过期 / 非 PENDING / `expires_at` 解析失败四条；「安全」行说的是真缺口；
「安全」行与「即将到期」共用同一段文案。

**`router.test.tsx`**：四格改名断言；「阻塞」那格的未接入断言（原「没有数据源的
两格」拆开）；新增「审批到期」真数据用例（4 张单只数 2 张）与端点读不到时的降级
用例；新增 SettingsPage 阈值写入措辞用例。

**`pages/FinancePage.test.tsx`**：异常与冻结、写入功能锁定两例改成断言新归因 +
缺席断言。

### 4.2 「旧实现下会不会照样绿」

把 `UpstreamDetailPage.tsx` 换回 `HEAD` 的版本跑新用例：**5 例全红**，
每一例红在自己的正向锚点上（`Relay 甲` / 订阅批次表 / 「这是计量型账号」 /
「上游登记簿里没有这一条」 / 「这个上游不属于 NewAPI」）。已还原。

### 4.3 变异验证明细（每一条都记了红在哪一行 + 对照组）

| # | 变异 | 目标断言 | 结果 | 对照组 |
|---|---|---|---|---|
| M1 | 把「上游详情读契约尚未接入」加回页面 | `SupplyDetailPages.test.tsx:599` 缺席断言 | 红，**且正好红在 599 行** | 其余 22 例全绿 |
| M2 | 在「找不到 id」分支里注入「充值比例与成本口径」区块（锚点保持可见） | `:647` 缺席断言 | 红在 647 | 其余 22 例全绿 |
| M2b | **改条件**：`item.id === upstreamId` → `() => true` | 找不到 id 用例 | 红在锚点 644（证明 id 匹配是承重的） | 其余 22 例全绿 |
| M3 | 在「平台不匹配」分支里注入同一区块 | `:662` 缺席断言 | 红在 662 | 其余 22 例全绿 |
| M3b | **改条件**：`account.system_type !== platform` → `false` | 平台不匹配用例 | 红在锚点 658 | 其余 22 例全绿 |
| M4 | **改条件**：`approvalsDueSoonCount` 去掉 `isEffectivelyExpired` 过滤 | 计数用例 | 红 2 例：`workbench.test.ts:153`（过期不算）+ `router.test.tsx:952`（读数 2→3） | 其余 204 例全绿 |
| M5 | **改条件**：`APPROVAL_DUE_WINDOW_HOURS` 24 → 48 | 窗口用例 | 红 2 例：`workbench.test.ts:142` + `router.test.tsx:952` | 其余 204 例全绿 |
| M8 | 把旧「安全」行文案整句写回 | 「安全」两例 | 红 2 例，但**红在锚点 506**，缺席断言没跑到 → 这次不算数，补 M8b | — |
| M8b | 保留新文案、**只在末尾追加**「随「人员与权限」页上线。」 | `workbench.test.ts:512` 缺席断言 | 红，正好红在 512 | 其余 43 例全绿 |
| M6 | 把「需 Action Advanced Controls」追加回 exceptions 文案 | `FinancePage.test.tsx:364` 缺席断言 | 红在 364 | 其余 18 例全绿 |
| M7 | 把「Foundation-A 阶段内核不放行」追加回补单解锁条件 | `FinancePage.test.tsx:421` 缺席断言 | 红在 421 | 其余 18 例全绿 |
| M9 | 把「写入仍需 Foundation-B / C3c 的审批链」追加回 SettingsPage | `router.test.tsx:2727` 缺席断言 | 红在 2727 | 其余 162 例全绿 |

M8 是一次**失败的变异**，如实记下来：整句替换让正向锚点先炸，缺席断言根本没
执行，那次变异什么都没证明。M8b 换成「保留新文案 + 追加旧那句」，锚点保持绿、
只有缺席断言红——这才对得上。M2/M3 用同样的手法（注入而非删除），M2b/M3b 另外
补了「改条件」型变异来证明判定本身承重。

---

## 五、门禁（全部本地跑通）

```
pnpm --config.verify-deps-before-run=false -r run typecheck   ✅ 5/5 项目 Done
pnpm --config.verify-deps-before-run=false -r run test        ✅ design-tokens 10 / ui-primitives 16 /
                                                                 ui-admin 261 / admin-web 1983，全绿
pnpm --config.verify-deps-before-run=false -r run build       ✅ storybook build completed；
                                                                 admin-web「✓ 365 modules transformed」
                                                                 + dist/ 三个产物行（已按要求 grep 确认
                                                                 admin-web 真的跑到了，不是被 storybook 中断）
```

`go test` **没跑**（后端一行没动，且别的 agent 在用同一个测试库）。

---

## 六、files_changed（10 个，全部在 `web/apps/admin-web/src/`）

```
pages/UpstreamDetailPage.tsx      整页重写（接三条读契约 + 挂载 UpstreamAccountDetail）
pages/SupplyDetailPages.test.tsx  上游详情 describe 重写 + 夹具扩充
lib/workbench.ts                  CREDENTIAL_EXPIRY_GAP 抽常量、D1 订正、
                                  APPROVAL_DUE_WINDOW_HOURS + approvalsDueSoonCount
lib/workbench.test.ts             +6 例
pages/OverviewPage.tsx            「今日到期」→「审批到期」接真数据 + 降级
pages/FinancePage.tsx             D3/D4/D5 + 525 行落款
pages/FinancePage.test.tsx        两例改断言 + 缺席断言
pages/SettingsPage.tsx            阈值写入阻塞措辞
lib/financeGlobal.ts              runwayAttentionItems 文档注释（525 行那句的第二份副本）
router.test.tsx                   四格改名 + 审批到期两例 + SettingsPage 一例
```

**未触碰**（派工点名 + `git status` 显示别人在改）：任何 `internal/`、`cmd/`、
`db/` 下的 Go/SQL 文件，`K:/发票/` 与其它 `wt-*`，`pages/RegistryPage.tsx`、
`pages/ChangesPage.tsx`、`components/Sub2ApiFinanceOverview.tsx`、
`components/PaymentStatusRollupTable.tsx`。

---

## 七、risks

1. **`接入渠道` 今天恒为 1。** 这是登记簿唯一索引的直接后果，不是 bug；副行
   写明了归并键。索引哪天放宽，这个数会自己变——因为归并键是后端给的，前端
   没有第二份实现。
2. **`supplier_key` 为空时降级成「未接入」而不是「1」。** 真实后端逐行都会给
   （`SupplierKeyOf` 没有返回空串的路径），所以这一支只在夹具/异常响应下出现。
   刻意分开：「归并不出来」和「就它自己一个」在屏幕上必须长得不一样。
3. **「审批到期」的窗口是滚动 24 小时，不是业务日。** 与格子标题一致
   （已从「今日到期」改名），副行也写了。若将来产品要求按业务日切，需要先定
   时区来源，那是另一个决定。
4. **上游汇总在仓库里有两个互不作废的 queryKey，写完只有一半界面会刷新。**
   这不是「多发一次请求」那种量级的问题，详见 follow_up 1。本页选了常量那一侧
   （因为挂进来的 `UpstreamAccountDetail` 作废的就是它），**这是既有分叉的一侧，
   不是本片新造的**——但本页确实成了分叉 A 侧的第二个读者。

---

## 八、follow_ups

1. **上游汇总的 queryKey 分叉成两个互不作废的值 —— 写完只有一半界面会刷新，
   而没刷新的那一半看起来完全正常。**

   `/finance/upstreams/summary` 这一条读契约，全仓用两种 key 缓存：

   | | key 值 | 读它的地方 | 写完作废它的地方 |
   |---|---|---|---|
   | **A 侧** | `[UPSTREAM_SUMMARY_QUERY]` = `["finance-upstream-summary"]` | `pages/UpstreamDetailPage.tsx:110` | `components/UpstreamAccountDetail.tsx:188`、`pages/UpstreamDetailPage.tsx:129` |
   | **B 侧** | `["finance","upstreams","summary"]` | `components/ChannelTable.tsx:77`、`components/FinanceSummaryCards.tsx:149`、`components/ManagedChannelTable.tsx:93`、`pages/ChannelDetailPage.tsx:121`、`pages/FinancePage.tsx:460` | `components/ChannelTable.tsx:98`、`components/ManagedChannelTable.tsx:113` |

   react-query 的作废是**前缀匹配**，而 `"finance-upstream-summary"` 不是
   `["finance","upstreams","summary"]` 的前缀（反之亦然）——**两边谁都作废不了对方。**

   **后果是双向的，而且都不会报错：**

   - **A 写 → B 五处不刷。** 在上游详情页登记订阅批次、记退款、终止、改令牌映射
     （`UpstreamAccountDetail` 的四个对话框 + `TokenMappingEditor`），成本摊销当场
     就变了，但**平台概览的资金卡、渠道管理表、跨平台财务页、渠道详情页**读的是
     B 侧缓存，原样保留旧的余额 / 可用天数 / 成本 / 毛利。
   - **B 写 → A 不刷。** 渠道管理表上改绑解绑（`ManagedChannelTable.tsx:113`）
     或改充值倍率（`ChannelTable.tsx:98`）之后，上游详情页那五格照旧。

   **为什么这个 bug 特别难发现（这是它比「少发一次请求」严重的地方）：**

   1. **没刷新的那一半看起来完全正常**——数字有值、格式对、金额可解释、
      覆盖率徽章也照常显示。它不是空白、不是「加载失败」、不是「未接入」，
      **它是一个陈旧但形状完美的读数**，与正确读数唯一的区别是数值不同。
   2. **同一屏内不会自相矛盾。** 每一侧内部是自洽的：B 侧五处共用一份缓存，
      要刷一起刷、要陈旧一起陈旧。所以永远不会出现「同一页上两个数打架」
      这种显眼症状——只有**跨页切换**时才有机会撞见，而人跨页时本来就预期
      看到不一样的数字（不同的口径、不同的窗口），第一反应不会是「缓存没刷」。
   3. **单测抓不到。** 每个组件各自的测试都只装配自己那一侧，断言「写完调用了
      `invalidateQueries`」——两侧的断言都通过，因为各自作废的确实是自己读的
      那个 key。要抓它得把两个组件放进**同一个 QueryClient** 里跑一次。

   **对照：登记簿那条 key 没有这个问题**（说明分叉的只有上游汇总这一条）。
   `UPSTREAM_ACCOUNTS_QUERY` 的值就是 `"finance-upstream-accounts"`，
   而 `ManagedChannelTable.tsx:89/112`、`ChannelDetailPage.tsx:117`、
   `ChannelTable.tsx:97` 写的字面量**与常量逐字同值**，
   `PlatformOverviewPanel.tsx:722`、`UpstreamAccountDetail.tsx:187`、
   `UpstreamDetailPage.tsx:106/128` 用常量——同值，所以互相作废得到。
   `["finance","channels","summary"]` 同理，全仓只有一种写法。

   **讽刺的地方，值得写给接手的人看：** `api/finance.ts:580-587` 那段文档注释
   逐字预言了这个 bug——「抽成常量而不是在各处写字面量……两处各写一遍字符串，
   改动一处就会变成『写成功了但表没刷新』，而这种 bug 只在真机上看得见」。
   而它正上方紧跟着的 `UPSTREAM_SUMMARY_QUERY`（593 行）就是被绕过去的那条。

   **本片为什么没顺手统一：** 两种改法都要动别的 agent 正在改的文件。
   把 A 侧改成字面量要动 `api/finance.ts` + `UpstreamAccountDetail.tsx`
   （后者有两处测试断言 `[UPSTREAM_SUMMARY_QUERY]`：
   `UpstreamAccountDetail.test.tsx:235/389`）；把 B 侧五处改成常量要动
   `ChannelTable.tsx`、`ManagedChannelTable.tsx`、`FinanceSummaryCards.tsx`、
   `ChannelDetailPage.tsx`、`FinancePage.tsx`，其中前四个不是我的文件。

   **⚠ 统一时必须整体做，不能零敲碎打。** 我本来可以顺手把自己文件里的
   `FinancePage.tsx:460` 改成常量——**那样反而更糟**：`FinancePage` 与它同屏
   渲染的 `FinanceSummaryCards`（`FinancePage.tsx:23` 引入）当前共用 B 侧那份
   缓存，同屏只发一次请求（`FinancePage.tsx:432` 的注释写着这条意图）。单独
   改一处会让同一屏发两次请求，并把分叉从「两侧」变成「三处不一致」。

   **建议统一到 A 侧（常量 `[UPSTREAM_SUMMARY_QUERY]`）**：它是
   `api/finance.ts` 已导出、有文档注释、且与另外三条 key（登记簿 / 订阅批次 /
   代理资产）同一套命名的那个；B 侧五处是散写的字面量，没有单一定义点。
   改完建议补一条**跨组件**回归：同一个 `QueryClient` 下渲染
   `UpstreamAccountDetail` 与 `FinanceSummaryCards`，在前者触发一次写，
   断言后者的 query 被标记为 stale——否则下次分叉照样没人拦得住。
2. `blueprints/governance.ts` 的 11 处、`RunwayThresholdRulePanel` 的 2 处、
   `PlaceholderPage` 的 2 处死文案 —— 见第三节，各自写了没动的原因与建议归属。
3. 「关联渠道」表今天每次只有一行（同 risk 1）。若将来一个供应商挂多个账号，
   这张表和「接入渠道」计数会同时变，不需要改代码——但值得在那时候确认排序
   （现在按 `/channels/summary` 的返回序，即接入方式 → 系统 → 地址）。
4. 「可用模型」「最近轮换」「最近验证」「预计补充时间」是本页剩下的四类未接入，
   各自的缺口已经写在 hint 里；前两类分别要上游分组目录读契约和跨读凭据管理
   端点，都够一个独立切片。
