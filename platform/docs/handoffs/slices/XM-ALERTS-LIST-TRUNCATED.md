# XM-ALERTS-LIST-TRUNCATED：截断由服务端说，前端不自己算

- **status:** implemented，未上线。后端加一个**只增不改的响应字段**，前端两处
  改用它。
- **branch:** `ai/claude/XM-ALERTS-LIST-TRUNCATED`，**基于
  `ai/claude/XM-WORKBENCH-TRUNCATION`**。合入顺序：WORKBENCH-JOBS →
  WORKBENCH-TRUNCATION → 本片。
- **来源：** XM-WORKBENCH-TRUNCATION 自己留的 follow_up——「值得对
  `listXxx({ signal })` 这种不带 limit 的调用做一次全面清点」。清点做完了，
  结论见下。

## 清点结论：平台 API 只有一处会静默截断

逐条查了后端：`defaultAlertLimit = 200` 是**唯一**一个"调用方不传就悄悄套上"
的上限。其余带 `Limit` 的端点（actionruns / jobs / profit_daily / requests /
subscription / users / metrics_history）都是显式分页，调用方传参、响应带游标，
不存在"以为看到了全部"这回事。渠道汇总那类干脆没有 LIMIT、返回全量。

**所以这一片只动告警。** 其余的 `listXxx({ signal })` 没有问题，不是漏了。

## 前端自己数个数是错的

上一片（XM-WORKBENCH-TRUNCATION）用的是"返回条数 >= 我传的 limit"。这个判据
在两种情况下**永远判不出截断**：

- 不传 limit：生效上限是服务端的默认值，前端看不见那个数。
- 传得比服务端上界还大：会被钳到 `MaxListLimit`，前端拿自己传的 9999 去比，
  返回 500 条也判成"没截断"。

**只有服务端知道生效上限。** 所以：

- `alerts.ClampListLimit` 从私有 `clampLimit` 提升为导出（HTTP 层不许复刻这段
  钳制——`parseAlertLimit` 的注释早就点过"两处各写一个数、改一处忘另一处"）。
- `GET /api/v1/alerts` 的响应增加 `limit`（生效上限）与 `truncated`
  （`len(items) >= 生效上限`）。**只增字段**，老前端忽略即可。

后台任务那条不需要新字段：`/api/v1/jobs/runs` 本来就有 `next_before` 游标，
"还有下一页"比数个数可靠。上一片的启发式一并换掉。

## 改了什么

- 后端：`internal/platform/alerts/store.go`（导出 `ClampListLimit`）、
  `internal/platform/httpapi/alerts.go`（响应加两个字段）。
- 前端：`api/alerts.ts` 新增 `listAlertsPage`（带信封），`listAlerts` 改为
  基于它返回 `.items`——**五处既有调用点一行没动**。
- `pages/AlertsPage.tsx`：改用信封。**这一页的职责就是"看全部"**，而它原先
  不传 limit、被截断时一个字都不说。现在会说，并给出生效上限与收窄建议。
- `pages/OverviewPage.tsx` + `lib/workbench.ts`：`truncationNote` 的入参从
  两个计数换成两个布尔（服务端的 `truncated` 与游标的 `next_before`）。

## 测试

- 后端两条：`ClampListLimit` 的五种输入（不传/负数/超界/等于上界/界内）；
  截断判据本身——**特意钉住"传 9999 实际只能拿 500"这一格**，前端若拿自己
  传的数去比会在这里误判。
- `lib/workbench.test.ts` 五条随入参改写。
- `router.test.tsx` 四条：还有下一页时提示；**取满 20 条但没有下一页时不提示**
  （权威判据比数个数准的地方正在这里）；告警页在服务端说截断时提示并给出
  生效上限；服务端没给 `truncated` 时不提示。

**两次变异验证，第二次抓到了自己的恒真断言。** 把"缺字段时按未截断"改成
"缺字段时按可能截断"，`服务端没给 truncated 时不提示` 竟然**照样绿**——因为
它 `await` 的是页头，而页头在 `ApiStateView` 外面，断言跑在查询回来之前，
`queryByText` 自然是 null。改成先等表格里的告警标题落地之后，同一个变异如期
变红。（[[absence-assertions-must-be-mutation-checked]] 第二次应验。）

门禁：平台后端 `go test -p 1 -count=1 ./...` 全绿、`gofmt` 干净；
前端 `typecheck` / `test`（1554 条）/ `build` 全过；
`scripts/check-governance.sh` 退出 0、`gitleaks protect --staged` 无泄漏。

## follow_ups

- `components/PlatformOverviewPanel.tsx` 的 `WorkCard` 也用不带 limit 的
  `listAlerts`，同样吃 200 的默认值。它有自己的显示层截断（`limitWorkItems`
  会说"还有 N 条"），但那个 N 是在**已经被服务端截断过的集合**上算的。
  改起来只是换成 `listAlertsPage`，但那一格是"某个平台的待办"，200 条对单平台
  基本够；等真有平台堆到 200 条告警再说。
- `truncated` 只对 `/api/v1/alerts` 加了。将来若有别的端点引入默认上限，
  应当照这个形状加，而不是让前端再去数个数。
