# XM-ALERTS-GAPS：批量确认接上执行，通知子页接上投递状态

- **status:** implemented，**未提交**（按任务要求不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `197d0bb`
  （`feat(action): 六个高危动作恢复设计风险等级，不再被迫停在 L1`）。
- **来源：** 两个实测核实的缺口，都在 `web/apps/admin-web/src/pages/AlertsPage.tsx`。
- **后端零改动。** 两个缺口用的都是**已经存在**的 Action 与响应字段。

## 动手前的重新核实（行号与字段名逐字）

这个 worktree 有并发编辑，任务书给的行号已经不准，两处都重新定位过。
两个缺口**今天确实都还在**。

| 事实 | 位置（重新定位后） | 逐字内容 |
|---|---|---|
| 确认动作是 **L0** | `internal/platform/alerts/actions.go:186` | `RiskLevel:  action.L0,` |
| 而且注释专门解释过为什么 | 同文件 `176–181` | 「L0 而不是 L1……任务书也明确要求 L0。静默才是真正会改变系统行为的那个，它是 L1。」 |
| 前端却写着等审批 | `AlertsPage.tsx:320–326`（任务书说 318–326） | 「批量确认是写操作，得走 Action 且 **L2 以上要审批**」/「批量确认随 Foundation-B（XM-0030）上线」 |
| 投递字段在响应里 | `internal/platform/httpapi/alerts.go:52–54` | ``NotifyStatus string `json:"notify_status"` `` / ``NotifyError  string `json:"notify_error"` `` / ``NotifiedAt   *string `json:"notified_at"` `` |
| 「通知」子页落到通用占位 | `AlertsPage.tsx:78–83` | 三元只认 `alerts` 与 `rules`，其余落 `"该子页尚未接入稳定的数据源。"` |
| 子页签的 id 是 `notifications`，不是 `notify` | `packages/ui-admin/src/navigation.ts:83` | `["notifications", "通知"]` |

**任务书里那句「`notify_status` 之类的字段」是准的**，三个字段名逐字照抄自
`alertItem` 结构体，没有凭记忆写。投递状态的取值同样照抄
（`alert.go:42/44/46`）：`pending` / `delivered` / `failed`。

## 缺口 1：批量确认

那句「随 Foundation-B 上线」从写下的那天起就是错的——挡它的理由（L2 以上要
审批）与它挡的那个动作（L0）对不上，而**单条确认一直可用**就是现成的反证。

- **执行**：循环调已有的 `alerts.alert.acknowledge@1`，一条一次。没有新端点、
  没有新 Action（宪法 2 条：批量不是一条新的写通道）。
- **串行不并发**：一次点击并发 N 个 POST 会把内核与审计写成一个尖峰；这一页的
  批量规模是几条到几十条，串行慢的那点没人察觉，顺序还让回执名单与表格对得上。
- **一条失败不中断后面的**：中途停下会留一个「谁确认了、谁没有」都说不清的
  半截状态。
- **明知会被拒的不发**：只有 OPEN / REOPENED 能确认（与后端 WHERE 子句同一条
  规则），其余**跳过并计入回执**——悄悄少确认两条和确认失败两条，对操作的人
  来说是同一种坏结果。
- **点之前就说会跳过几条**，不等回执：人是按「我选了 9 条」判断这一步做没做完的。

### 回执

不说「操作成功」。总账形如 `已确认 7 条，2 条失败；审计事件通常几秒内出现在审计页`，
一条都没成时明说 `0 条确认成功，2 条失败`（且**不提审计页**——没有记录可查）。
下面三段名单：

- **失败**：逐条 `<标题>：<后端原话>（错误码 X），需要权限 Y，request_id=Z`。
- **跳过**：逐条 `<标题>：当前状态「已静默」不可确认，只有未处理与复发可以`。
- **成功**：逐条 `<标题>：run_id=<action_run_id>`——规格 §5.8 要求界面显示
  action_run_id，N 次写就是 N 个，少一个就有一次写查不回审计事件。

回执挂在**页级**而不是选择条里：选择条会随「清除选择」一起消失，那一下会把
刚拿到的 run_id 名单一并带走（`DataTableV2` 的「清除选择」就在同一行）。

## 缺口 2：「通知」子页

数据源一直就在 `/api/v1/alerts` 的响应里——**同一页表格的「投递」列读的就是它**。
子页取 `status=all`：投递是件历史性的事，「昨天那条炸了的告警到底发出去没有」
和「现在这条」同样要问。

页面给三件事：按投递状态的计数、**「还没解决又没送达」的单独告警块**（本模块
最危险的组合，也是这一页存在的理由）、以及一张带投递时刻与失败原因的表。

**把数据源的三条局限写在页面上**，因为它最容易被当成通知流水来读，而平台今天
没有那张表：

1. 「已投递」只表示**至少一个渠道**收下了。这是 `MultiNotifier.Notify` 的取舍
   （`notify.go:222–231`：整体判失败会让主渠道变成垃圾消息源），代价是同一轮
   里另一个渠道失败**不会**进 `notify_error`，只落在服务端日志的
   `alert_notify_channel_failed` 里。
2. 一条告警只有一个最新投递状态，看不到重试次数与历史。
3. 与告警列表读同一批记录，同样受服务端条数上限影响（`truncated` 照样提示）。

**「故障事件」与「暂停告警」保持占位**，并加注释说明为什么：Incident 对象今天
不在平台里，静默记录也没有列表端点。通知能做出来只是因为它的数据一直在响应里。
有一条用例钉着这两格不被顺手接上别的数据。

## files_changed

改（1 个）：

- `web/apps/admin-web/src/pages/AlertsPage.tsx`

新增（6 个）：

- `web/apps/admin-web/src/lib/alertBulkAck.ts` —— 批量确认的纯函数（分流、
  失败措辞、汇总）
- `web/apps/admin-web/src/lib/alertNotify.ts` —— 投递状态归类与「未送达」筛选
- `web/apps/admin-web/src/components/BulkAcknowledgeAlerts.tsx` —— 按钮 + 回执
- `web/apps/admin-web/src/components/AlertNotifyDeliveries.tsx` —— 通知子页
- `web/apps/admin-web/src/lib/alertBulkAck.test.ts`
- `web/apps/admin-web/src/lib/alertNotify.test.ts`
- `web/apps/admin-web/src/pages/AlertsPage.test.tsx`（新建；此前告警页的用例
  都在 `router.test.tsx` 里）

**没有碰**：任何 `internal/` 或 `cmd/` 下的 Go 文件、`router.test.tsx`、
`src/api/alerts.ts`、`src/lib/alerts.ts`，以及任务书点名的那批并发文件。
`git diff -- AlertsPage.tsx` 只有本片的 8 处改动（35 增 9 删），交回报告前重跑过一次。

## tests_run

新增 **39 条**：`alertBulkAck.test.ts` 17 条、`alertNotify.test.ts` 7 条、
`AlertsPage.test.tsx` 15 条（批量确认 7 条 + 通知子页 8 条）。

部分失败路径**有专门用例**（任务书点名要求）：三条里第二条 403，断言总账逐字
是 `已确认 2 条，1 条失败；审计事件通常几秒内出现在审计页`、失败那条的原因逐字
可读、**第三条仍然发了也成了**（`ackedIds` 等于三个 id），且失败那条不出现在
run_id 名单里。另有全部失败、全部跳过、混合跳过三条。

### 变异验证（11 项，全红）

| 变异 | 转红的用例 |
|---|---|
| M1 总账无条件附带审计提示 | 「一条都没成时明说 0 条确认成功」+「全部失败时明说 0 条确认成功」 |
| M2 回执把失败的也列进 run_id 名单 | 「部分失败时逐条说清是哪一条、为什么」 |
| M3 把旧那句「随 Foundation-B 上线」放回去 | 「不再说批量确认随 Foundation-B 上线」 |
| M4 单条确认不再清掉上一张批量回执 | 「单条确认的回执与批量回执互斥」 |
| M5 通知子页回落到通用占位 | 通知子页 **6 条** 全红 |
| M6 「没解决又没送达」恒真 | 4 条（含下面那条修好之后的「全部送达时不报警」） |
| M7 认不出的投递状态并进「未投递」 | 2 条（lib 与页面各一） |
| M8 「未知状态」那一格无条件显示 | 「按投递状态分别计数」 |
| M9 故障事件/暂停告警也塞进通知子页 | 「故障事件与暂停告警仍是占位」 |
| M10 不再按 canAcknowledge 过滤，明知会被拒的也照发 | 4 条 |
| M11 遇到第一条失败就中断 | 2 条 |

**M6 第一次跑抓出了一条我自己写的恒真断言**，值得记下来：
「全部送达时不报警」原本写成

```ts
await waitFor(() => expect(screen.queryByText(/……没有送达任何人/)).toBeNull());
```

`waitFor` 的**第一次**回调就通过了——那时页面还停在「加载中…」，什么都没渲染，
于是这条断言与实现无关、恒绿。改成先 `await` 一个证明数据已渲染的正向断言
（`已投递` 那一格是 1），再同步断言那句话不在，M6 复验即转红。

**给下一个人：`waitFor` 包住的缺席断言几乎总是恒真**，因为「还没渲染」也满足
「不存在」。缺席断言要么前面先等一个正向锚点，要么根本不该用 `waitFor`。

### 正向断言的「旧实现下会不会照样绿」

- 批量那批：旧实现根本没有按钮，`getByRole("button", { name: "批量确认 N 条" })`
  直接失败。
- 通知那批有一处**真的有风险**并已处理：旧占位也渲染 `<PageHeader title="通知">`，
  所以 `findByRole("heading", { name: "通知", level: 2 })` 在旧实现下照样绿。
  M5 证明这 6 条**不是**靠它过的——每条都另有只有新实现才产生的断言
  （投递失败原因、计数格、来源说明、「从未投递成功」）。

## 门禁

三条都自己跑通了（`pnpm --config.verify-deps-before-run=false -r run …`）：

- `typecheck` —— 5 个 project 全 Done
- `test` —— **2149 条全绿**（admin-web 1862 / ui-admin 261 / ui-primitives 16 /
  design-tokens 10；ui-storybook 无单测）
- `build` —— 全 Done（admin-web 357 modules；chunk >500kB 的告警是既有的，
  与本片无关）

另跑了 `bash scripts/check-governance.sh` —— **退出 0，无输出**。

Go 侧未跑（`go test` / `go vet`）：本片一行 Go 都没改。

## risks

- **批量是 N 次独立的写，不是一个事务。** 中途关页面会留下「前几条确认了、
  后几条没有」的状态。这是可接受的——确认本身可重入（再点一次，已确认的会被
  跳过并如实报出来），但它不该被当成原子操作。
- **选中集不随 rows 一起清。** `DataTableV2` 没有那个 effect，所以确认完自动
  刷新一次之后，选中集里可能留着已经转走的行键。这些键会被报成「已不在当前
  列表里」的跳过项——**这是有意的**，比默默少确认几条好。
- **回执不入库。** 页面刷新就没了。真相在审计事件里（每条 run_id 都显示了），
  但「刚才那批到底哪几条失败了」在刷新后只能去审计页逐条查。
- **通知子页不是通知流水。** 页面已把三条局限写在最上面，但如果将来真的要做
  「按渠道的投递记录」，那需要后端新表，不是在这一页加列。

## follow_ups

- **`packages/ui-admin/src/DataTableV2.stories.tsx:276,283` 还写着同一句错话**
  （「写操作一律走 Action 且 **L2 以上要审批**」/「批量操作随 Foundation-B
  上线」）。那是 `bulkActions` 插槽的通用示例，不是告警页，且该文件在本片的
  改动范围之外（同一 worktree 有并发编辑），**没有动**。建议单独一片修掉——
  它是这句错误说法的最后一个副本，留着会被下一个人当成现行规则再抄一遍。
- **`BulkAckReceipt` 没有复用 `components/ActionResultNote.tsx`**，两者有概念
  重叠（都是写操作回执，都显示 run_id）。没复用有两个原因：`ActionResult` 是
  **单次**写的形状（一个 title + 一个 runId，或一张审批单），表达不了「N 条里
  哪几条成了」；而且该文件此刻正被另一个 agent 改。如果将来要统一，方向应该是
  给 `ActionResultNote` 加一个批量变体，而不是把三段名单塞进现在的单次形状。
- 批量确认后**不会自动清空表格选中集**：`DataTableV2` 没有暴露清空选择的
  回调，而该文件在本片范围之外。现在的兜底是把失效的键报成「已不在当前列表
  里」的跳过项。要真正修好，得给 `DataTableV2` 加一个 `onBulkActionDone`
  之类的出口——那是 ui-admin 的改动，应单独立片。
- 批量确认目前不带理由，与单条一致（L0 无需理由）。若将来产品要求批量写留痕
  说明，那是 Action 参数的变更，要先动契约。
- 「故障事件」与「暂停告警」两格仍缺数据源，各自需要一条新的只读 Query。
