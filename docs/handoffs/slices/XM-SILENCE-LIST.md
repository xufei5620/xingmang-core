# XM-SILENCE-LIST：静默窗口的只读列表

- **status:** implemented，未提交（按交办要求不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-0030c 之后）。
- **来源：** 缺口清单「暂停告警只能建、不能看」。
- **上线影响：** 纯新增一条只读端点 + 一个此前是占位的子页签。既有端点、
  既有 Action、既有权限表一律未改，生产行为在没人点开那个子页签时不变。

## 一、复核结果（行号是复核当天实测的）

**三条都还在，缺口成立。**

| 清单上的说法 | 实测 | 结论 |
|---|---|---|
| `store.go` 有 `ListActiveSilences` 与 `ListSilences` | `internal/platform/alerts/store.go:494` 与 `:512` | 属实 |
| `router.go` 里 `Silence` 出现 0 次 | `grep -c Silence internal/platform/httpapi/router.go` → `0`；全 `httpapi/` 只有 `finance_runway_preview.go:171` 的 `alerts.StatusSilenced`，与静默窗口无关 | 属实，无任何列出端点 |
| 前端「暂停告警」落在通用占位 | `AlertsPage.tsx:82` 的注释原文就是「**静默记录也没有列表端点**」，且 `AlertsPage.test.tsx:319` 有测试钉着它是占位 | 属实 |

那句「必须保持占位」的理由是**没有端点**，不是「不该做」。端点补上之后
理由消失，所以这一片把「暂停告警」搬走、**把「故障事件」留在占位**
——它缺的不是端点而是 Incident 对象本身。

### 两处与交办口径不同，先说清楚

**1. 没有「已取消」这个状态。** 交办里写的是「区分『生效中』与
『已过期/已取消』」。实测：`Silence` 结构体（`alert.go:151`）只有
`ID / RuleKey / Environment / Reason / StartsAt / EndsAt / CreatedBy / CreatedAt`
八个字段，没有任何取消标记；`alerts/actions.go` 只注册了
`alerts.silence.create`；全仓 `grep DeleteSilence|CancelSilence|silence.cancel|silence.delete|RevokeSilence`
零命中，SQL 里也没有对应查询。**平台今天没有撤销静默的能力，窗口只会自己
到期。** 所以做成三态：`active` / `scheduled`（未开始）/ `expired`。
编一个「已取消」出来会让运营以为那件事已经做得到，界面上还会去找那个按钮。

**2. 两个方法的差别不只是「是否过滤过期」。** 逐字对照 `db/queries/alerts.sql`：

```sql
-- ListActiveSilences（:136）
WHERE environment = $1 AND starts_at <= $2 AND ends_at > $2
ORDER BY ends_at DESC;              -- 没有 LIMIT

-- ListSilencesByEnvironment（:144）
WHERE environment = $1
ORDER BY starts_at DESC, id
LIMIT $2;
```

差别有三处：前者**还排除了未开始的窗口**（不只是过期的）、**没有 LIMIT**、
排序键也不同。这直接决定了下面两个设计。

## 二、端点契约（逐字）

```
GET /api/v1/alerts/silences
```

**Scope：`alerts.ScopeRead`**，其值为 `"ops.read"`（`alerts/permissions.go:13`）
——与 `GET /api/v1/alerts` 是同一个，路由上并排。**没有新增任何 scope**，
因此 `web/apps/admin-web/src/api/staff.ts` 的 `STAFF_ROLE_CATALOG` 无需改动
（全量前端测试已验证它没红）。

依据：窗口正文是「规则键 + 理由 + 谁按的 + 起止时间」，泄漏面比告警正文
（含余额、收入数字）**更小**；而 `ops.read` 已经能读到后者。多一个 scope
只是多一个要维护、要授予、会漏授的授权面。写路径不动：建窗口仍是
`alerts.silence.create@1`（L1，`alerts.silence.manage`），读写刻意分开授权。

### 查询参数

| 参数 | 取值 | 缺省 | 错误 |
|---|---|---|---|
| `environment` | 必须与调用者身份一致 | 调用者自己的环境 | 不一致 → `403 PERMISSION_DENIED`「不允许跨环境读取：调用者身份属于 …」；非法环境名 → `400 INVALID_PARAMS` |
| `state` | `active` \| `all`（大小写不敏感） | `active` | 其余 → `400 INVALID_PARAMS`，文案逐字：`state 必须是 active（默认，只看此刻生效的）或 all（含未开始与已过期）` |
| `limit` | 正整数，上界 `alerts.MaxListLimit`（500） | `defaultSilenceLimit` = 200 | 非正整数/非数字 → `400 INVALID_PARAMS`，文案逐字：`limit 必须是正整数`（与 `/alerts` 同一个实现，见下） |

`state` 只有这两个取值，**不提供 `state=expired`**：过滤要么在 SQL 里，
要么就没有。「先按 limit 截断再在 Go 里挑出已过期的」会让一页全是生效中的
窗口时返回空列表，而调用方读到的是「没有已过期的静默」——那是假话。

### 响应（200）

```json
{
  "items": [
    {
      "id": "0e8c…",
      "rule_key": "metric.sync.failed",
      "environment": "development",
      "reason": "上游在维护，先压两小时",
      "starts_at": "2026-09-08T11:30:00Z",
      "ends_at": "2026-09-08T13:30:00Z",
      "created_by": "staff_alice",
      "created_at": "2026-09-07T12:00:00Z",
      "state": "active"
    }
  ],
  "limit": 200,
  "truncated": false,
  "as_of": "2026-09-08T12:00:00Z"
}
```

- `rule_key` **空串 = 全局窗口**（该环境下所有规则）。保持空串，不换哨兵值
  ——`"*"` 之类会与一条真叫这个名字的规则撞车。翻译成中文是前端的事。
- `state` ∈ `active` / `scheduled` / `expired`，由服务端算，**前端不自行比时间**：
  一台快五分钟的机器会把刚过期的窗口显示成「生效中」，而运营据此以为告警还压着。
- `as_of` 是判定所用的时刻。分类只在那一刻成立，而页面在浏览器里会挂很久；
  不说清算的是哪一刻，「生效中」就是一句没有时效的断言。
- `limit` / `truncated` 与 `/alerts` **同一套语义、同一条理由**：只有服务端
  知道生效的 limit（调用方传的会被钳），也只能说「可能」截断。
- 空列表返回 `[]` 而不是 `null`。
- 排序沿用各自 SQL 的顺序（`active` 按 `ends_at DESC`；`all` 按
  `starts_at DESC, id`），未改 SQL。展示顺序由前端决定。

## 三、四个刻意的决定

### 1. 边界规则抽成 `alerts.Silence.Active`，只写一处

`Matches` 答的是「**这条规则**现在被压着吗」，列表要问的是「**这个窗口**
现在生效吗」——两个问题。直接用 `Matches` 不行：它对 `ruleKey == ""` 恒返回
false，会把全局窗口整片判成不生效。

所以把左闭右开 `[starts_at, ends_at)` 那一行提到新方法 `Silence.Active(at)`
（`alert.go`），`Matches` 改为调用它。**HTTP 层不复刻这段判断**：两份实现
迟早分叉，而分叉的后果是列表说「已过期」、投递侧仍在静默（或反过来），
那比没有列表更糟。`TestSilenceMatches` 未改动且仍绿，证明重构没改语义。

### 2. `active` 与 `all` 走各自的 SQL，不是「全取再筛」

见第一节的三处差别。若统一走 `ListSilences` 再在 Go 里筛出生效的，一批
「未开始」的窗口（`starts_at` 最大，倒序排最前）就能把生效中的挤出这一页，
页面于是显示「当前没有静默生效」——而告警正被压着。那是这一页唯一不能出
的错。

代价是 `ListActiveSilences` 的 SQL 没有 LIMIT，所以 `limit` 在那条路径上由
HTTP 层兑现（取回后截断）。`truncated` 两条路径同一公式
（`len(out) >= effectiveLimit`），语义与 `/alerts` 一致。

### 3. 管理端发两次请求

同样的理由再走一遍：含历史那次可能被条数上限截断，而「现在压着几条」这个
计数**不能**跟着被截断。所以「生效中」横幅用单独一次 `state` 缺省的请求，
表格用 `state=all` 那次。多一次请求换掉「静默生效时页面说没有静默」这个
失败形态，划算。截断提示里也写明了计数不受影响。

### 4. `parseAlertLimit` 抽成 `parseListLimit(raw, def)`

两个端点的 `limit` 错误文案必须逐字一致（同一个参数在相邻端点上给出两种
说法，只会让调用方以为自己看错了），但**默认页大小各留一个常量**
（`defaultAlertLimit` / `defaultSilenceLimit`，同为 200）：共用一个常量的话，
将来调整告警页大小会连带改掉静默页，而改的人不会知道自己动了第二个页面。
`parseAlertLimit` 保留为一行转发，`/alerts` 的行为逐字未变。

### 附带：`Deps.SilenceNow`（仅测试用）

三态的全部内容就是「拿此刻去比起止时间」。跟着墙上时钟走的话，边界用例会
间歇性变红——那种红比不红更难查。所以路由把时刻做成可注入的：`nil` 时用
`time.Now`，**生产从不设置它**（`main.go` 没有传）。

## 四、变异验证明细

**方法**：改条件（不是删代码），每个变异同时跑一组「应该变红」和一组
「不该变红」的对照。脚本跑完自动还原并复跑全套确认无残留。

### 后端（`go test`）

| # | 变异（改的条件） | 应变红 | 实际 | 对照组（不该红） | 实际 |
|---|---|---|---|---|---|
| M1 | 默认分支条件 `state == silenceStateAll` → `state != ""`（等于去掉「只看生效中」的过滤） | `TestSilencesEndpointAgainstRealStore`、`TestListSilencesDefaultsToActiveQuery` | **RED** | `TestSilencesEndpointStateAllAgainstRealStore`、`TestListSilencesStateAllUsesHistoryQuery` | GREEN |
| M2 | `Silence.Active` 右端 `at.Before(EndsAt)` → `!at.After(EndsAt)`（右开改右闭） | `TestListSilencesBoundaryIsLeftClosedRightOpen` | **RED** | `TestListSilencesClassifiesThreeStates`、`TestSilencesEndpointAgainstRealStore` | GREEN |
| M3 | `silenceStateAt` 里 `at.Before(StartsAt)` → `at.After(StartsAt)`（未开始/已过期判据反转） | `TestListSilencesClassifiesThreeStates`、`TestSilencesEndpointStateAllAgainstRealStore` | **RED** | `TestSilencesEndpointAgainstRealStore` | GREEN |
| M4 | `parseSilenceState` 里 `EqualFold(raw, active)` → `!EqualFold(raw, all)`（拼错的值不再拒绝） | `TestListSilencesRejectsUnknownState` | **RED** | `TestListSilencesStateAllUsesHistoryQuery`、`TestListSilencesDefaultsToActiveQuery` | GREEN |

### 前端（`vitest`）

| # | 变异（改的条件） | 应变红 | 实际 | 对照组（不该红） | 实际 |
|---|---|---|---|---|---|
| FM1 | 「生效中」那次请求也带 `state=all`（取消单独查生效窗口） | `横幅只数生效中的那些`、`发两次请求` | **RED** | `生效中与已过期分得开` | GREEN |
| FM2 | `sortSilencesForDisplay` 的 rank 反转（已过期排到生效中前面） | `排序：生效中置顶` | **RED** | `生效中与已过期分得开`、`剩余时间按服务端的两个时刻算` | GREEN |
| FM3 | `describeSilenceState("expired")` 的 label 改成「生效中」 | `生效中与已过期分得开`、`未开始与已过期各有自己的说法` | **RED** | `一条都不生效时说清楚` | GREEN |
| FM4 | `AlertsPage.tsx` 里 `value === "silences"` → `value === "__never__"`（退回通用占位） | `暂停告警不再是占位` | **RED** | `故障事件仍是占位` | GREEN |

脚本另有一道保险：`-t` 没匹配到任何用例时视为失败（否则 vitest 会以
「0 passed」退 0，等于没测）。四个变异还原后全套复跑 **GREEN**。

### 针对本仓踩过的坑，逐条对照

- **「旧实现下会不会照样绿」**：M1/FM1/FM4 就是「旧实现」——端点不过滤、
  页面还是占位。三个都变红，说明这批用例不是在旧实现下也成立的空断言。
- **缺席型断言**：只有三处。`生效中与已过期分得开` 里
  `within(row).queryByText("生效中")` 由 FM3 变红；`暂停告警不再是占位`
  里 `queryByText("「暂停告警」尚未接入")` 由 FM4 变红；
  `横幅只数生效中的那些` 里 `queryByText(/现在有 3 个/)` 由 FM1 变红。
- **`waitFor` 套缺席断言**：没有这种写法。两处都是先 `await` 一个正向锚点
  （`findByRole("heading")` / `findByText`），再**同步**断言缺席。
- **只断错误码不够**：`TestListSilencesRejectsUnknownState` 与
  `TestListSilencesRejectsBadLimit` 都同时断言 `code` 与**逐字文案**。
- **「规则」与「调用它的地方」穿一条线**：判据在
  `alerts.Silence.Active`（Go），过滤在 `ListActiveSilences`（SQL），是两段
  代码。`alert_silences_integration_test.go` 的三条用例**从 HTTP 端点打进
  真实库**：`TestSilencesEndpointAgainstRealStore` 喂一条生效中 + 一条未开始
  + 一条已过期，断言默认查询**恰好返回一条且 id 对得上**（正向、精确，
  不是「某某不存在」）；`…StateAllAgainstRealStore` 是它的对照组——同一批
  数据只换参数就返回 3 条，于是「只返回 1 条」不可能是因为库里本来就一条。

## 五、门禁结果（全部自己跑通）

| 门禁 | 结果 |
|---|---|
| `go test -p 1 -count=1 ./...`（带 `XM_TEST_DATABASE_URL`，`-p 1` 未漏） | **PASS**（exit 0，无 FAIL 行） |
| `go vet ./...` | **PASS**（exit 0） |
| `bash scripts/check-governance.sh` | **PASS**（exit 0） |
| `gofmt -l ./cmd ./internal` | 无输出 |
| `pnpm -r run typecheck` | **PASS**（5 个包全 Done，exit 0） |
| `pnpm -r run test` | **PASS**（exit 0；admin-web 135 文件 / 1922 用例，ui-admin 261，ui-primitives 16，design-tokens 10） |
| `pnpm -r run build` | **PASS**（exit 0） |

**build 那项是怎么确认 admin-web 真构建过的**（不只看退出码和末尾几行）：
输出重定向到文件后三个独立证据一起对上——

1. 日志里有 `web/apps/admin-web build: ✓ 359 modules transformed.`
   （`grep "modules transformed"` 同时列出 storybook 的 163 与 admin-web 的
   359 两行，说明**两个包都跑到了**，`pnpm -r` 没有在 storybook 处中止）；
2. 同一段日志有 `web/apps/admin-web build: ✓ built in 482ms` 与
   `web/apps/admin-web build: Done`；
3. **落盘产物的 mtime**：`web/apps/admin-web/dist/index.html` 与
   `dist/assets/` 的时间戳是 `2026-09-08T07:40:07`，而检查时刻是
   `07:40:15`——8 秒前刚写的，不是上一次构建的残留。

本次 `ui-storybook` 没有触发那个 libuv 拆卸崩溃（exit 3221226505），
但上面第 1 条即使在它崩了的情况下也能立刻发现——admin-web 那行会不存在。

## 六、改了什么

**后端**

- `internal/platform/httpapi/alert_silences.go`（新增）——`SilenceLister`
  窄接口、`silenceItem`、三态判定、`ListSilencesHandler`、`parseSilenceState`。
- `internal/platform/alerts/alert.go`——新增 `Silence.Active(at)`；
  `Matches` 改为调用它（**语义未变**）。
- `internal/platform/httpapi/alerts.go`——`parseAlertLimit` 抽成
  `parseListLimit(raw, def)` 并转发（**行为未变**）。
- `internal/platform/httpapi/router.go`——`Deps.Silences`、`Deps.SilenceNow`；
  在 `/alerts` 旁注册 `GET /alerts/silences`，同一个 `RequireScope`。
- `cmd/platform-api/main.go`——`Silences: alertStore`（与 `Alerts` 同一个
  Store，不另开访问静默表的路径）。
- 测试：`alert_silences_test.go`（17 条，fake）、
  `alert_silences_integration_test.go`（3 条，真实库 + 真实路由）。

**前端**

- `web/apps/admin-web/src/api/alerts.ts`——`SilenceState` / `SilenceItem` /
  `SilencesPage` / `listSilencesPage` 与两个 state 常量。
- `web/apps/admin-web/src/lib/alerts.ts`——`describeSilenceState`、
  `sortSilencesForDisplay`、`formatSilenceRemaining`、
  `GLOBAL_SILENCE_RULE_LABEL`。
- `web/apps/admin-web/src/components/AlertSilences.tsx`（新增）——子页签本体。
- `web/apps/admin-web/src/pages/AlertsPage.tsx`——`silences` 接上
  `<AlertSilences />`；占位注释改写成只覆盖「故障事件」。
- 测试：`AlertSilences.test.tsx`（8 条，新增）、`lib/alerts.test.ts`（+7 条）、
  `AlertsPage.test.tsx`（占位用例拆成两条：故障事件仍占位 / 暂停告警不再占位，
  另加 `stubSilences`）。

**文档**

- `docs/modules/alerts/README.md`——权限表加一行；静默章节补「没有取消」
  与「列出窗口」两节。

**界面上「一眼看出哪些此刻在压着告警」是怎么做到的**：顶部一条红色横幅直接
给数字（「现在有 N 个静默窗口正在压着告警（截至 …）」，N=0 时改说「所有规则
都会正常投递」）；表格里状态列的「生效中」用 `danger` 色调——**不是绿色**，
一个正在生效的静默意味着告警此刻是哑的，那是需要被看见的状态，绿色会让人
一眼扫过去以为没事；排序把生效中置顶、同组内最快到期的在前；生效中的行还带
一行「还剩 X 分钟」，由服务端的 `ends_at` 与 `as_of` 算，不碰浏览器时钟。

## 七、风险与后续

- **`Deps.SilenceNow` 是测试用的注入点**，生产不设置。若将来有人在 `main.go`
  里给它赋值，静默三态会按那个时钟算——不该发生，但值得在评审时看一眼。
- **没有「取消静默」**（见第一节）。如果运营实际需要，那是另一片：要新增一个
  L1 Action、一列 `cancelled_at`/`cancelled_by`、以及本端点多一个状态取值。
  本片刻意没有替它预留字段——预留一个没有写入方的列，只会让人以为功能存在。
- **`state=all` 仍是 limit + truncated，没有游标**。这是照 `/alerts` 的邻居
  形状写的（本仓 Query 端点目前都是这个形状，没有游标分页的先例）。静默窗口
  数量级很小，短期够用；真要翻历史时再上游标，届时两个端点应该一起改。
- **工作树里有别人的在途改动，而且在我干活期间还在长**：交办说工作树是干净的，
  实际不是。收尾时 `git status` 里属于订阅/代理资产那条线的有六个文件——
  `web/apps/admin-web/src/api/finance.ts`、`components/UpstreamAccountDetail.tsx`
  与其 `.test.tsx`、`lib/subscriptionForms.ts`、
  `components/SubscriptionLifecycleDialog.tsx` 与其 `.test.tsx`。
  其中后两个 `.test.tsx` 是在我跑完全量前端门禁**之后**才出现的。
  **本片一个字都没碰它们**（也没碰 `K:/发票/`、`SubscriptionBatchDialog.tsx`、
  `ProxyAssetDialog.tsx`）。

  由此产生一个必须说清的边界：`pnpm -r run test` 那次全绿（07:39，1922 条）
  反映的是**那一刻**的工作树，包含对方当时的在途改动；此后对方又加了文件，
  我没有重跑全量。本片自己的文件在收尾时单独复跑过，仍全绿
  （Go：`httpapi` + `alerts` 两个包 exit 0；前端：4 个文件 57 条）。
  合入前建议在只含本片文件的树上再跑一次全量。

  **本片的文件共 16 个**（提交时按这份挑）：
  `internal/platform/httpapi/alert_silences.go`、`…_test.go`、
  `…_integration_test.go`、`internal/platform/alerts/alert.go`、
  `internal/platform/httpapi/alerts.go`、`internal/platform/httpapi/router.go`、
  `cmd/platform-api/main.go`、`web/apps/admin-web/src/api/alerts.ts`、
  `web/apps/admin-web/src/lib/alerts.ts`、`…/lib/alerts.test.ts`、
  `web/apps/admin-web/src/components/AlertSilences.tsx`、`…/AlertSilences.test.tsx`、
  `web/apps/admin-web/src/pages/AlertsPage.tsx`、`…/AlertsPage.test.tsx`、
  `docs/modules/alerts/README.md`、`docs/handoffs/slices/XM-SILENCE-LIST.md`。
