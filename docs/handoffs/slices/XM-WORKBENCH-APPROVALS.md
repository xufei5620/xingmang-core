# XM-WORKBENCH-APPROVALS：工作台「待审批」接上审批中心

- **status:** implemented，**未提交**（按 brief 要求不 commit、不 push，改完即停）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `e673aef`。
- **commit:** 无——改动留在工作区。**同一 worktree 里另有一个 agent 在改
  `internal/` 与 `contracts/`**，提交时只取下面 files_changed 里的五个文件。
- **来源：** 运营工作台「我的待处理」六个分类里，「待审批」一直没有数据源。
  审批中心 XM-0030 已实装并启用（`GET /api/v1/approvals?status=PENDING` 是通的，
  「操作与审批」页的待审批子页签已经在用它），本片把工作台这一格接上。

## 改了什么

**`web/apps/admin-web/src/lib/workbench.ts`**

- 新增纯函数 `workItemsFromApprovals(items, now)`，与 `workItemsFromAlerts` /
  `workItemsFromJobRuns` 并列。
- `WORK_CATEGORIES` 里 `approvals` 的 `blockedBy` 撤掉，换成 `source`。
  （两者必须**恰好有一个**，`workbench.test.ts` 有一条 XOR 断言钉着。）
- `ageText` 拆成 `durationText` + `ageText` + 新增 `untilText`（还有多久，
  解析不出返回 `null`）。行为对既有调用方逐字不变。
- 新增 `WORK_APPROVALS_LIMIT = 20`；`truncationNote` 加第三个判据
  `approvalsTruncated`（**必填**，不做成可选：可选会让调用方漏传而无声）。

**`web/apps/admin-web/src/pages/OverviewPage.tsx`**

- 新增 `approvalsQuery`（`status=PENDING`、`limit=WORK_APPROVALS_LIMIT`、
  `retry: false`），接进 `refreshAll`。
- `WorkList` 收三类事项；二选一的 `jobsOnly ? … : …` 换成**按分类查表**
  （`sourceByCategory`），不是嵌套三元。
- 空态文案改成按分类查表（`EMPTY_DESCRIPTIONS` + `DEFAULT_EMPTY_DESCRIPTION`），
  「全部」那句里把「审批」从「还没接」的名单中划掉。
- 卡片顶部那段说明改成「故障 / 待审批 / 失败任务」三类。

**`web/apps/admin-web/src/router.test.tsx`（只改一条断言）** ——
「筛选进 `?work=`，选到没有数据源的分类时说清楚被什么挡着」原来用
`?work=approvals`，接上数据源后那一类不再有 `blockedBy`，改用 `?work=expiring`
并断言它自己的说明（凭据模型里没有到期时间 / `CredentialRef`）。

## 三个刻意的决定

### 1. 只收「此刻真的还等着人投票」的单，过期的不算

两道过滤：`status === "PENDING"`，且 `!isEffectivelyExpired(item, now)`。

第二道不是多余的。`ExpirePending` 是定时任务，库里的 status 会滞后，而服务端
在**执行那一刻**才按 `expires_at` 判过期（`components/ApprovalQueue.tsx` 的第 2
条纪律就是这个）。照抄 status 会让首屏混进一批谁也批不动的单——「我的待处理」
是"我必须动手的事"，摆一件动不了的事进来，人翻两次就不再信这张清单。这与
「重试中的任务不列进来」是同一条理由。

取数那一层已经带了 `status=PENDING`，函数里仍然再滤一遍：它是纯函数，调用方
换个取法时不该悄悄把已驳回、已执行的单列成待办。

### 2. 排序复用审批队列页的 `groupByRisk`，不另写一套

L4 → L3 → L2，组内先到期的在前。同一批单在工作台和队列页排成两个样子，人会
以为看的是两份数据。徽章上放的是**风险等级**而不是「待审批」：这一屏唯一要当场
判断的是「先看哪一张」，而 L4 与 L2 的差别正是答案。

`meta` 直接用 `voteProgress(item, now)`——「票数够了但还缺一张特权票」那种措辞
只有一份，不在这里另写。

### 3. 未挂载走 `FeatureNotMountedError`，与「操作与审批」页同一条路径

`api/approvals.ts` 已经把**裸 404**（chi 对未挂载路由回纯文本，解析不出
`error.code`）翻成 `FeatureNotMountedError`；`ApiStateView` 认识这个类型，渲染
`PageState kind="unavailable"`：标题「未接入」、正文是 `error.description`
（「这套后台连上的 platform-api 还没有审批中心……请确认 platform-api 已滚到含
该变更的版本，而不是等待排期」）、**不给重试按钮**。

工作台什么也没有自己发明——只是把 `approvalsQuery.error` 送进 `ApiStateView`。
说错方向的说明比没有说明更糟：它会把人支去等一个不会再来的排期。

**代价（见 risks）**：这只在选中「待审批」那一格时看得见。

## 测试

**`lib/workbench.test.ts`（+9 条，共 38 条全绿）**

- 一条待办的等级 / 提交人 / 还差几票 / 到期时间 / 落点**逐字相等**
  （`"staff_bob 提交 · 还差 1 票（已 1/2）"`、`"3 小时后到期"`、
  `"/actions?sub=pending"`）——只断言「有值」等于没测。
- L4 排在 L3 前、同档先到期的在前；不认识的等级显示成中性徽章而不是丢掉。
- 已过期的 PENDING 不进待办；非 PENDING 一律不进。
- 票数满但缺特权票时 `voteProgress` 那句原样落在行上。
- `expires_at` 解析不出时说「到期时间未知」，不拼出「—后到期」。
- 分类表：有源的是 `["incidents","approvals","jobs"]`；`approvals` 不再挂
  `blockedBy`。
- `truncationNote`：待审批那一条；三边都截断；**按格子只说一条改成整句相等**
  （见下）。

**`pages/OverviewPage.test.tsx`（+8 条，共 10 条全绿）** —— 页面级接线：
列出真实待审批单并直达 `/actions?sub=pending`；整组 404 说「未接入」+ 逐字两句
说明 + 无重试按钮；**503 时照常「加载失败」+ 重试按钮**（与上一条互为对照）；
空态说清空的是什么；取满上限时提示；「全部」里审批排在告警之后；审批端点挂了
不把「全部」整块变红。

### 变异验证（缺席型断言，逐条删实现跑一次）

| 变异 | 期望 | 结果 |
|---|---|---|
| 去掉 `!isEffectivelyExpired(item, now)` | 「已过期的不进待办」变红 | 红 |
| 去掉 `item.status === "PENDING"` | 「非 PENDING 不进待办」变红 | 红 |
| `approvals` 分类改回 `blockedBy` | 两条分类用例变红 | 红（2 条） |
| `truncationNote` 的 `showApprovals` 恒为 `true` | 「只说当前这一格」变红 | 红 |

**顺带改掉一条恒真的断言**：起初在「待审批空态」那条里写了
`queryByText(/注意：审批、财务异常/)).toBeNull()`——可那个位置根本不渲染
「全部」的默认文案，删不删实现它都绿。换成单独一条用例，对「全部」空态的
**整句**做相等断言，既钉住「审批已从『还没接』名单里划掉」，也钉住「其余三类
仍被逐个点名」。`truncationNote` 的按格子断言同样从 `not.toContain` 换成整句
相等，理由一样：断言"某句不在"太容易随便改个字就恒真。

## 门禁（全部自己跑通）

```
pnpm --config.verify-deps-before-run=false -r run typecheck   # 5 个包全 Done
pnpm --config.verify-deps-before-run=false -r run test        # 前端合计 2080 条全绿
pnpm --config.verify-deps-before-run=false -r run build       # admin-web + storybook 均成功
```

未跑：`go test ./...` / `go vet`（本片一行 Go 都没动，且同一 worktree 里另一个
agent 正在改 `internal/`，跑出来的红绿不归本片）。

## files_changed

- `web/apps/admin-web/src/lib/workbench.ts`
- `web/apps/admin-web/src/lib/workbench.test.ts`
- `web/apps/admin-web/src/pages/OverviewPage.tsx`
- `web/apps/admin-web/src/pages/OverviewPage.test.tsx`
- `web/apps/admin-web/src/router.test.tsx`（一条断言）

未动：任何 `internal/` 下的 Go 文件、`web/packages/ui-admin/src/navigation.ts`、
`web/apps/admin-web/src/router.tsx`、`docs/architecture/ADMIN-IA.md`、
`web/apps/admin-web/src/api/approvals.ts`（只读）。

## risks

- **「全部」视图里看不见「未接入」。** 加载态与错误态按分类查表，「全部」落到
  告警那一条——审批端点挂了只是少几行，不该把整块换成错误态（那正是最需要看见
  告警的时候）。这是沿用「失败任务」既有的取舍，代价是：后端版本旧时，「全部」
  里审批静默为空，要点进「待审批」才会说明原因。已有用例把这个行为钉住了，
  改口径时会看见它。
- **`APPROVAL_RISK_TONE` 与 `components/ApprovalQueue.tsx` 的 `RISK_TONE` 是同
  一张表的两份。** 那一份在组件内私有，而 brief 限定本片不改那个文件，所以照抄
  了一份（注释里写了「加档位时两处一起改」）。要消掉这个重复，该把它提到
  `lib/approvals.ts`——那是另一片的事。
- **`router.test.tsx` 的 `okHandler` 没有 `/api/v1/approvals` 分支**，工作台在
  那些用例里对着兜底的 404 取数。因为 brief 限定只改一条断言，没有补。160 条
  路由用例全绿（「全部」视图不看审批的错误态），但下一个动 `okHandler` 的人
  顺手补一条空列表会更干净。
- 「待审批」只取 20 条，服务端上界是 100。取满会提示「可能不是全部」，但工作台
  本来就是入口——真到需要翻页的量级，该处理的是积压。

## follow_ups

- **`APPROVED` 但还没执行的单没有进这一格。** 那种单也需要人动手（按「执行」），
  但分类名叫「待审批」，把待执行的塞进来是另一件事，需要先定 IA。
- 顶部四格的「今日到期」仍是「—/未接入」，说明里还写着「审批、重试与轮换到期；
  随 Foundation-B 与后台任务页上线」——审批这一半现在算得出来了（
  `expires_at` 在今天之内的 PENDING 单）。本片没动那一格：它同时还等着重试与
  轮换两条线，改一半会让那句说明更难读。
- 把 `RISK_TONE` 提到 `lib/approvals.ts`，两处共用（见 risks 第 2 条）。
