# XM-I18N-LABELS：管理后台机器标识的中文对照

- **status:** implemented，未上线。纯前端展示层改动，**后端一行未改**
  （`git status internal/ cmd/` 为空）。
- **branch:** `ai/claude/XM-I18N-LABELS`。基线 `8c446e5`（提交 `d24f9b5`），
  之后合入主线 `a2d02ae`（合并提交 `79be3fc`）并在合并后追加一轮。未推送。
- **来源：** 产品负责人原话——「这些报错也好、审批也好、告警也好，能不能有中文
  对照，在管理后端都显示中文。因为我的英语水平不好。」

## 一、普查结果

先按「界面上真的会露给人看的机器标识」把全站扫了一遍，分成三类。

### 1.1 此前**完全没有中文**的（本片新增）

| 类别 | 后端来源 | 项数 | 界面露出点 |
|---|---|---|---|
| Action 错误码 | `action/errors.go` 的 `Code` | 14 | `ActionErrorNote`（所有写弹窗）、`ApiStateView`（所有只读页错误态）、`alertBulkAck`、`LoginPage`、`TotpEnrollPage`、`InvoiceConsolePanel`、`MetricSparkline`、`AuditPage` 结果列、`ActionsPage` 执行记录 |
| 前端自造错误码 | `api/client.ts` | 4 | 同上（`UNKNOWN` / `NETWORK_UNAVAILABLE` / `BAD_RESPONSE` / `UNAUTHENTICATED`） |
| 风险等级 | `action/risk.go` 的 `RiskLevel` | 5 | `ActionsPage` 三处（目录列、执行记录列、等级表）、`ApprovalQueue` 两处（分组标题、详情） |
| 身份类别 | `principal/principal.go` 的 `Type` | 4 | `AuditPage` 主体列、`ActionsPage` 身份列与「允许身份类型」列、`ApprovalQueue` 提交人、`SettingsPage` 主体类型 |
| 上游账号登记状态 | `finance/account.go` 的 `Status` | 2 | `ChannelDetailPage` 登记状态 |
| HTTP 状态码 | 无后端枚举（`httpapi/response.go` 的 `StatusForCode` 是映射方向） | 12 | `RouteErrorBoundary`、`api/client.ts` 的兜底文案 |

### 1.2 **已有中文但表不全**（本片补齐，并加门禁）

| 类别 | 后端有 | 前端原有 | 缺了什么 |
|---|---|---|---|
| 告警规则键 | 8 | 5 | `upstream.version.changed`、`upstream.runway.low`、`approval.pending.too_long` |
| 后台任务种类 | 14 | 10 | `approval_expire`、`card_sync`、`sms_probe`、`assurance_probe` |

**这两处不是「英文没翻」那么轻。** 告警规则键那一份同时是**静默对话框的下拉
选项**：缺三条意味着运营根本选不到它们去静默——想压住「审批单挂太久」的刷屏
只能整个环境全局静默。任务种类那份缺的第四个（`assurance_probe`）尤其说明
问题：它的 kind 常量不在 `internal/platform/jobs/` 而在 `internal/platform/
assurance/job_args.go`，**任何按目录去找的人都会漏掉它**。

### 1.3 **已经有中文、只是散在各处**（本片收进门禁，实现不动）

告警严重度 3、告警状态 5、投递状态 3、静默状态 3（`lib/alerts.ts`）；
任务运行状态 7（`api/jobs.ts`）；审批状态 6（`lib/approvals.ts`）；
服务状态 3（`ui-admin/freshness.ts`）。这些实现是对的，只是没有任何东西
保证它们跟得上后端——现在有了（见第三节）。

### 1.4 顺带发现的一个 fail-silent

`ActionsPage` 的执行状态一直写成 `status === "succeeded" ? "成功" : "失败"`。
后端 `action.RunStatus` 今天确实只有两个值，但**加第三个值的那一天，这个三元式
会把它显示成「失败」**——那不是翻译不到位，是把一个新事实说成一个旧的假事实。
改成查表 + 认不出来原样显示，并纳入对账。

## 二、原码怎么保留的

三种形制，按位置选，**每一种都保证原码逐字仍在页面上**：

1. **行内错误条**：`{服务端原话}（{中文}，错误码 {原码}）`
   例：`数据库不可用（服务端内部错误，错误码 INTERNAL）`。
   中文与原码在**同一个文本节点**里——原码要能被人整段选中复制去 grep 日志，
   拆进子元素就选不全了。完整解释挂 `title`，不占正文。
2. **徽章／短标签**：`中文（原码）`，如 `人（HUMAN）`、`在采（active）`。
3. **等级与状态码**：原串在前，中文补在后——`L3 高风险`、`HTTP 404 未找到`。
   等级串是运维查 ADR-003、跟人开口时用的那个词，它该在最前面。

**认不出来的码走的是「什么都不加」那条路**：`errorCodeNote("QUOTA_EXHAUSTED")`
返回 `错误码 QUOTA_EXHAUSTED`——与本片之前的写法逐字相同，不加「未知错误」。
加了的话，一个有名有姓的问题会变成没名字的问题，而事实是平台知道得很清楚，
只是这套前端还没跟上后端的新码。

一个刻意的用词决定：**`UNKNOWN` 这个码的中文不叫「未知错误」**，叫「响应里
没有错误码」。它有确切含义（服务端或它前面的反代没按平台错误包格式作答），
叫「未知错误」会与「映射表不认识这个码」的兜底撞车，而那是完全不同的两件事。

## 三、映射模块与对账测试

### 3.1 模块

**`web/apps/admin-web/src/lib/labels.ts`（新增，390 行）** —— 机器标识中文对照的
**唯一一份**。含错误码、Action 执行状态、审批状态与表决、风险等级、身份类别、
上游账号登记状态、HTTP 状态码，以及三个形制函数 `withCode` / `fullLabel` /
`errorCodeNote`。每条对照分三层：`label`（中文名）、`hint`（为什么，进悬停）、
`nextStep`（**接下来做什么，可见的一行**，只有确实知道下一步时才填）。运行时零依赖（只有一个 `import type`），所以 `api/client.ts`
也能安全地用它。

告警与任务的中文仍留在 `lib/alerts.ts` / `api/jobs.ts`：它们已经正确、已有测试，
搬家只会制造风险；本片给它们补的是**门禁**，不是搬家。

**去掉的重复**（本片之前各有两份、且已经分叉）：

- 审批状态：`ApprovalQueue.tsx` 的筛选下拉自带一份（`已批准（待执行）`），
  `lib/approvals.ts` 另有一份（`已批准`）。现在限定语由对照表的 `note` 承载，
  徽章取短名、下拉取全名——**同一条记录的两种渲染**，不是两份表。
- 风险等级：`ActionsPage` 与 `ApprovalQueue` 各有一份语气表和一份「等级怎么写」
  的逻辑。合并成 **`components/RiskBadge.tsx`**（新增）。这个合并不是顺手做的
  ——它是被变异验证逼出来的，见 4.2。
- 风险等级表的事实：`ActionsPage` 的 `RISK_LEVELS` 里 `examples` /
  `directlyExecutable` 与 `labels.ts` 重复，改为派生；只留 ADR-003 独有的
  「基础控制」一栏（后端代码里没有这个事实，没有可对账的对象）。

### 3.2 对账测试（本片的核心交付物）

**`web/apps/admin-web/src/lib/labels.reconcile.test.ts`（新增，55 条）**
—— **直接读后端 Go 源码**，把每个枚举值逐个对着前端的表查差集。

> **这一节写于合并主线之前，当时只有下表这 13 组，而那远远不够。** 合并之后
> 发现新模块的枚举可以整个绕过这套断言，于是补了「枚举清点」那一层——见
> **第七点五节**。下表保留原样，它仍然是每一组差集断言的索引。

覆盖：

| 组 | 读的文件 | 抽取方式 |
|---|---|---|
| Action 错误码 | `action/errors.go` | `X Code = "…"` |
| Action 执行状态 | `action/kernel.go` | `X RunStatus = "…"` |
| 审批状态 / 表决 | `approval/approval.go` | `X Status = "…"` / `X Verdict = "…"` |
| 风险等级 | `action/risk.go` | `X RiskLevel = "…"` |
| 审批策略事实 | `approval/approval.go` 的 `DefaultPolicy()` | map 字面量 + `RequiresAdvancedControls` 函数体 |
| 身份类别 | `principal/principal.go` | `X Type = "…"` |
| 告警规则键 + 中文名 | `alerts/rules.go` | `RuleXxx = "…"` 与 `Key:/Title:` 配对 |
| 告警严重度 / 状态 / 投递状态 | `alerts/alert.go` | 三个类型 |
| 静默窗口状态 | `httpapi/alert_silences.go` | `silenceStateXxx = "…"` |
| 任务运行状态 | `jobs/query_store.go` | `X RunState = "…"` |
| 任务种类 | **递归扫 `internal/platform/`** | `XxxJobKind = "…"` |
| 上游账号启停 | `finance/account.go` | `X Status = "…"` |
| 服务状态 | `registry/service.go` | `X ServiceStatus = "…"` |

三处值得单独说：

- **风险等级不只对「有没有中文」，还对票数与有效期**。负责人的话是「光一个
  L3 说明不了什么，要能看出要几票、能不能自批、多久过期」。那些数字唯一的
  来源是 `approval.DefaultPolicy()`，测试把它的四个 map 抠出来逐条比对，
  并且**连给人看的那句 hint 里的数字也一起比**——事实字段对上了、文案却还停在
  旧数字，等于没对上。产品负责人日后改策略（设计稿 §7 那四个待拍板项），
  这一条会红。
- **告警规则的中文名逐字取自后端 `Title`**，不是前端另起一套说法。两边各说各的
  时，运营在告警页看到的名字和运维在规则文档里查到的名字对不上号。
- **任务种类递归扫整个 `internal/platform`**，因为 `assurance_probe` 的常量不在
  jobs 包里。只盯一个目录会正好留下那个洞。

**防恒真的两道**（每一组都有）：

1. 先断言抽到的数量下界 + 关键值存在，再断言差集为空。抽取器失灵（文件被挪、
   常量写法改了、正则少个空格）会让清单变空数组，「每一项都有中文」于是自动
   成立——这是这类测试最典型的假绿。
2. 「已翻译」的判据（中文名 ≠ 原值）自身配反向断言：喂后端不存在的值进去，
   确认判据说它「没翻译」；再配一条正向对照，确认判据不是恒假。

## 四、变异验证

### 4.1 对账测试：真的改后端 Go 源码，跑完还原

不是只用合成字符串走一遍——**真的改了 `internal/` 下的文件，确认变红，再逐个
`md5sum` 核对还原**（`git status internal/ cmd/` 现在为空）。

| 变异（改后端源码） | 期望 | 结果 |
|---|---|---|
| `errors.go` 加 `QUOTA_EXHAUSTED` | 「每个错误码都有中文」红，且差集恰为 `["QUOTA_EXHAUSTED"]` | 红，差集逐字相符 |
| `heartbeat.go` 加 `NightlyDanceJobKind = "nightly_dance"` | 「每个任务种类都有中文」红 | 红（同时证明递归扫描有效） |
| `rules.go` 把 `Title` 改成「指标同步炸了」 | 「中文名逐字取自后端 Title」红 | 红，指名 `metric.sync.failed` |
| `kernel.go` 加 `RunStatus = "timed_out"` | 「每个执行状态都有中文」红 | 红 |
| `finance/account.go` 加 `Status = "paused"` | 「每个上游账号启停状态都有中文」红 | 红 |
| `registry/service.go` 加 `ServiceStatus = "draining"` | 「每个服务状态都有中文」红 | 红 |

| 在**第三个模块**（`notify/notify.go`，既非 jobs 也非 assurance）种一个 `DigestJobKind = "notify_digest"` | 「每个任务种类都有中文」红 | 红，差集恰为 `["notify_digest"]` |

对照组：每次变异只红目标那几条，其余 30+ 条保持绿。

**最后一条是应验收线要求补做的**：任务种类那一组扫的根是
`internal/platform/`（`readdirSync(..., { recursive: true })`），**不是**
`internal/platform/jobs/`。读代码能确认，但读代码确认不了「它真的会因此变红」
——所以把常量种在一个与任务系统毫无关系的模块里跑了一次。红了。
下次有人把 kind 常量放进第三个地方，门禁还是会拦住。

测试文件里另外**常驻**一段合成源码的变异（「往枚举里加一个码：差集里恰好多出
它」「改掉常量写法让抽取器抓空时，数量下界那条拦得住」），让后来的人不必重做
上面那套手工验证就能看见这条流水线在工作。

### 4.2 「认不出来的码原样显示」：缺席型断言，逐条验过

| 变异（改前端条件，不删代码） | 期望 | 结果 |
|---|---|---|
| `errorCodeNote` 未知码兜底成「未知错误，错误码 X」 | `labels.test.ts` 两条 + `ActionErrorNote.test.tsx` 一条红 | 红 3 条；控制组 18 条全绿 |
| `riskLevelText` 未知等级兜底成 `RISK_LEVELS.L0` | `labels.test.ts` 一条 + `ApprovalQueue.test.tsx` 缺席断言红 | **第一次只红了 1 条** —— 见下 |
| `fullLabel` 吞掉限定语 | 下拉「已批准（待执行）」找不到 + 徽章短名断言红 | 红 2 条 |
| `principalTypeText` 丢掉原码 | 「人（HUMAN）」找不到 | 红 2 条 |
| `errorCodeNextStep` 一律返回空串（＝把下一步藏进悬停） | 「可见地说清下一步」红 | 红 1 条 |
| 改渲染条件为 `nextStep !== undefined`（＝没有下一步也渲染空段落） | 「不留一个空段落」红 | 红 1 条，报 `expected 2 to be 1` |

**第二项抓出了一个真问题，也是本片唯一一次「测试先说了实话」**：
`ApprovalQueue` 里我自己写的 `RiskBadge` 复制了一份 `meaning ? \`${level}
${meaning.label}\` : level` 的逻辑，没有调 `riskLevelText`——于是变异改到
`riskLevelText` 头上时，那条组件级缺席断言**恒真地绿着**。修法不是改断言，
是把两页各自的 `RiskBadge` 合并成 `components/RiskBadge.tsx`（4.1 表里的
`ActionsPage` 那份本来就是调 `riskLevelText` 的，所以合并方向是明确的）。
合并后重跑同一个变异：两条都红，且**红在目标断言那一行**（`not.toMatch(/…风险/)`），
不是红在锚点上。

缺席断言一律先 `await`／取到正向锚点，再同步断言缺席，没有把 `waitFor` 套在
缺席断言外面。

## 五、撞红并改对的既有断言（10 条，逐条改对，没有放宽）

改错误码文案形制必然撞既有的逐字断言。全部按新的**正确**文本改，没有一条改成
正则模糊匹配：

- `lib/alertBulkAck.test.ts` 3 条 —— `（错误码 X）` → `（中文，错误码 X）`
- `components/SubscriptionLifecycleDialog.test.tsx` 3 条（403/400/409）
- `pages/AlertsPage.test.tsx` 1 条
- `components/ConnectorConfigPanel.test.tsx` 1 条（INTERNAL）
- `components/CredentialManagementPanel.test.tsx` 1 条（ACTION_NOT_REGISTERED）
- `api/alerts.test.ts` 1 条 —— 原本是 `expect(ALERT_RULES).toHaveLength(5)`。
  **这一条改的是断言的性质**：条数该有几条由后端 `rules.go` 说了算，写死在这里
  只会在后端加规则时红在一个说不清原因的地方；改成「非空 + 键的形态」，条数交给
  `labels.reconcile.test.ts` 的差集断言。

`router.test.tsx:1651` 用的是 `/错误码 PERMISSION_DENIED/`，新文案仍然包含这个
子串，未受影响——不是我放宽的，它本来就这么写。

## 六、门禁（合并主线后复跑，下表为最终结果）

| 门禁 | 结果 |
|---|---|
| `go test -p 1 -count=1 ./...`（八个代理变量全 unset） | 退出 0，**62** 个包 ok，0 FAIL |
| `go vet ./...` | 退出 0 |
| `bash scripts/check-governance.sh` | 退出 0 |
| `pnpm -r run typecheck` | 5 个包全 Done |
| `pnpm -r run test` | **2115** + 262 + 16 + 10 全绿。本片新增 **52** 条：合并前 32（1955→1987），合并后 20（对账文件 35→55 条） |
| `pnpm -r run build` | 退出 0 |

**build 的坑按要求单独核过**：不只看退出码。`ui-storybook build: ✓ 163 modules
transformed` 与 `Storybook build completed successfully` 之后，
**`admin-web build: ✓ 375 modules transformed`** 与 `dist/assets/index-*.js`
的产物行都在（先 `rm -rf dist/assets dist/index.html` 再构建，确认是这次生成
的），`build: Done` 出现 2 次，全文 grep `assertion|libuv|abort` 命中 0 次。
这一次没有复现那个 libuv 拆卸崩溃。

**跑门禁前的环境准备**：本 worktree 没有 `node_modules`。按既定做法建了
junction（根 `node_modules` → 主检出；五个工作区包的 `@xingmang/*` → 本
worktree 自己的 `web/packages/*`），配合 `--config.verify-deps-before-run=false`
跑真门禁命令。**没有跑 `pnpm install`**。

**一处操作教训**：中途在同一条复合命令里连着跑了两次 `go test ./...`，第一次
末尾出现 `FAIL`。两次共用同一个 worktree 测试库。之后每次单独跑都是 58 ok /
0 FAIL / 退出 0。仓库那条「共用测试库会互撞」的纪律是实的，别在一条命令里
串两次全量。

## 七、没做的、以及为什么

### 7.1 有意留成原样显示的（**不是漏了**）

- **连接器新鲜度的 `last_error_code`**（`OpsPage`、`ConnectorProbeHistoryCard`、
  `CPAAssurancePanel`、`ui-admin/FreshnessBadge`）。它是**开放命名空间**：值由
  各连接器自己填（`upstream_timeout` 之类），后端没有闭合枚举，没有可对账的
  对象。给它建一份猜出来的表，等于把「认不出来就原样显示」这条原则自己破掉。
- **上游系统自己的状态串**：NewAPI 渠道类型（`PlatformOverviewPanel` 的
  「类型 X」）、Sub2API 号码状态（`SMSPanel`，已有本地映射 + `（未知状态）`
  兜底且保留原值）。它们不是平台的枚举。
- **地址类标识一个字都没动**：Action ID（`cards.card.issue@1`）、规则键本体、
  `request_id`、`run_id`、`params_hash`、UUID、scope 名、指标键。中文只加在
  旁边。
- **`registry.ConnectionStatus`（enabled/disabled/killed）**：全站 grep 没有
  找到今天会渲染它的地方（连接管理页尚未落地），按「不把不渲染的常量算进去」
  的要求没有收进对照表。**后端一旦有页面渲染它，要记得同时补表和补对账。**

### 7.2 三条术语——**已由产品负责人裁定**（2026-09-08）

1. **`ACTION_NOT_REGISTERED` → 「动作未注册或资源不存在」，维持。**
   这个码今天身兼两职：写路径上是「这个 Action 没注册」，读路径上被当作通用的
   「资源不存在」用（后端注释写明了这件事和它为什么没被改名，全仓 13 处读路径）。
   裁定意见：**在前端选一边等于替后端做了个它没做的决定，且必然在另一半场景里
   说错话；笨拙但正确 > 顺口但一半时候是假的。** 等后端那条 follow_up
   （`docs/handoffs/slices/XM-ERRCODE-NOTFOUND.md`）解决之后再来收窄。

2. **`ADVANCED_CONTROLS_REQUIRED` → 「需要高级管控」，且必须另起一行说清下一步。**
   否决了「审批中心未接入」那个备选：它把原因写成了症状，更要紧的是**审批中心
   现在已经接入**——那个译法会让人以为「我们还没做审批中心」，而事实是这套后台
   连的后端版本旧了。

   **本轮据此补了代码，不只是定了个词**：`Meaning` 新增可选的 `nextStep`，
   `ActionErrorNote` 与 `ApiStateView` 把它渲染成**可见的一行**（不是悬停——
   藏起来等于没说）。措辞与 `api/approvals.ts` 的
   `APPROVALS_NOT_MOUNTED_DESCRIPTION` 同一口径，指向「确认 platform-api 已滚到
   含该变更的版本，而不是等排期」。

   为什么非补不可：后端那句原话是
   `action cards.card.issue 风险等级 L3 需要 Action Advanced Controls（Foundation-B / XM-0030）`
   ——半句英文加两个内部代号，对着它没人知道该干什么。

   目前**只有这一个码填了 `nextStep`**。机制是通用的，但没有下一步的码一律留空，
   也不渲染那一行——编一句「请联系管理员」比空着更糟。

3. **runway → 「可用天数」，维持。** 判据是「跟界面上已经在用的走，而不是跟代码
   里的英文走」。

   **顺带更正我上一轮的一句错话**：我说「仓库里『续航』有时『可用天数』有时」，
   并把统一术语列进了 follow_up。**这是错的**——全仓 grep 过，「续航」唯一的
   出处就是我自己在 `labels.ts` 里写的那个括号注解。前端从 `api/finance.ts` 到
   告警规则「上游可用天数不足」一直是统一的。已经把那个括号去掉，
   **follow_up 撤回**。

### 7.3 发现但没动的既有不一致

- **`api/config.ts` 的 `PrincipalType = "HUMAN" | "MACHINE"`**。后端
  `principal.ParseType` 只收 `HUMAN/SERVICE/AI/SERVER_AGENT`，**`MACHINE` 会被
  dev-header 解析器当场拒掉**（403「身份类型非法」）。这不是 i18n 问题，本片
  没改它；`SettingsPage` 现在会把 `MACHINE` 原样显示出来——按原则二，这恰好让
  这个不一致**看得见**，而不是被一个编出来的中文名盖住。
  → **follow_up：要么改前端类型，要么后端加这个值，二选一。**
- **`ChannelProfitView.tsx` 的 `statusText`** 把 `finance.Status`（active/
  disabled）与 registry 的 `retired` 混在一个 switch 里翻译。它今天是对的
  （两个命名空间的值恰好不冲突），但那是巧合。没并进 `labels.ts`，因为并进去
  就得先把这两个命名空间在调用点分开——那是另一片的工作量。
  → **follow_up：分开这两个枚举，再各自纳入对账。**

## 七点五、合并主线之后（2026-09-08 下午）——门禁自己被抓到有洞

主线上来了三个新模块（extapp / integration / publishing）与资源目录的三条只读
Query。合并之后**对账测试一条都没红**。

**那不是通过，那是漏。** 三个新模块一次带来十个新枚举，而我那些分组测试只认
我当初逐个列出的那几个文件——「我列了 13 组」与「后端一共有多少组」之间
**没有任何东西在对账**。我上一轮在本文档里写的「后端加了新码而前端没跟，这里
变红」这句话，**范围说大了**：它只对我枚举过的那 13 组成立。

### 补的东西

**`ENUM_INVENTORY` + `discoverGoStringEnums()`（labels.reconcile.test.ts）** ——
反过来问：把 `internal/platform` 下所有字符串枚举（`type X string` 且同文件里
≥2 个该类型常量）**发现**出来，共 **68 个**，每一个都必须在清点表里有一行。
新增枚举 → 红 → 有人必须写一行说清它要不要中文。

三档，含义写在测试的注释里，**不许被读成「已验证的覆盖」**：

| 档 | 条数 | 含义 |
|---|---|---|
| `reconciled` | 23 | 上面有一组自己的差集断言盯着 |
| `labelled-elsewhere` | 23 | 界面上会露出、也已经有中文，但**还没纳入对账**——后端加取值时不会有任何东西变红 |
| `backend-only` | 22 | 判断它不露到界面上，附一句依据 |

**后两档是待办清单，不是完成度。** 机器只保证「每个枚举都被分过类」这一件事；
分类本身是**我写下的判断**，值得验收线扫一眼——尤其 `backend-only` 那 22 条。
分错的代价与今天相同（某处中文没跟上），不会造成安全上的假绿。

### 新纳入对账的 10 个枚举

`integration.{ClientStatus,RuleStatus,TriggerKind}`、
`extapp.{AppStatus,AuthMode,ReleaseKind}`、
`publishing.{DraftStatus,ChannelStatus,Platform,AssetKind}`、
`registry.ConnectionStatus`。

三个新模块**本来就都写了中文**（页面作者都翻了），问题不是没翻，是**又多了三份
私有映射、且都不在门禁里**。所以这一轮没有搬家，只是把它们各自钉住——与我对
alerts / jobs 的做法一致。

### 顺带修的三处

1. **`TRIGGER_KIND_LABELS.manual` 是错的**：写成「手动或定时触发」，而 schedule
   另有一项叫「定时」。下拉里同时出现这两项，选的人无从分辨；后端
   `TriggerManual` / `TriggerSchedule` 是两个独立取值。改成「手动」。
   （出处大概是 `blueprints/ext.ts` 里那份**节点类型**清单——那里「手动或定时触发」
   是一个节点，说法没问题，被抄成逐值标签才出错。蓝图那份没动。）
   新增断言：四个中文名两两不同，且除 schedule 外都不含「定时」。
2. **`CLIENT_STATUS_LABELS.disabled` 会误导**：裸一个「已停用」，而
   `integration/doc.go` 第 1 条写得很清楚——**停用一行不会让任何请求被拒绝**，
   登记簿不是授权面。改成「登记已停用」，并加 `CLIENT_STATUS_HINTS` 说明
   「该身份的请求照样会被放行，要真正断供得去改角色」。新增断言钉住这两点。
3. **`ConnectionStatusBadge` 是三元链**：`enabled ? … : killed ? … : "已停用"`
   —— 与我这一片开头修的那个 `succeeded/failed` 是同一类 fail-silent，后端加
   第四个取值那天会把它显示成「已停用」。改成查表，认不出来原样显示；
   `killed` 保持「已拉闸」而不是「已停用」（Kill Switch 是显式紧急动作）。

**规则状态与渠道状态按你的提醒逐条核过**：`RULE_STATUS_LABELS` 是
草稿 / 已登记 / 已作废，**不含**「启用 / 生效 / 运行」——已加一条断言机械地守住
这一点（`registered` 不许被翻成「已启用」，因为登记不会让任何 Action 跑起来）。
publishing 的三张表里**没有任何一处回显凭据**，也加了断言（不许出现
`secret://` / credential / token / api_key），并另加一条：草稿状态不许凭空多出
「已发布」——后端 `DraftStatus` 刻意没有 PUBLISHED。

### 变异验证（合并后这一轮）

| 变异 | 期望 | 结果 |
|---|---|---|
| 在 `notify/notify.go` 里新增 `type Flavour string` + 两个常量（模拟「新模块带来新枚举」） | 「没有哪个后端枚举是清点表不知道的」红 | 红，差集恰为 `["notify.Flavour"]` |
| 往清点表里塞一条后端不存在的 `ghost.Removed` | 「清点表里也没有后端已经删掉的枚举」红 | 红，差集恰为 `["ghost.Removed"]` |

第一条正是今天早上静悄悄溜过去的那个场景。现在它会红。

## 八、risks

- **对账测试读的是 Go 源码文本，不是 Go 类型**。后端把某个枚举改成 `iota`、
  或者拆到多个文件、或者用 `var` 而不是 `const`，抽取器会抓空——那时挡住这种
  假绿的是每组开头那条数量下界断言（已变异验证过它会红）。红了之后**修抽取器，
  不要把下界调低**。
- **前端测试依赖仓库布局**（`../../../../../internal/...`）。admin-web 若被搬走
  或单独发包，这一组会读不到文件而整组红——红得很响，不会静默。
- **`silenceStateAll` 是按常量名排除的**（它是 `state` 查询参数的特殊值，不是
  响应里的状态）。排除处配了 `expect(names).toContain("silenceStateAll")`，
  它一旦改名，测试会指着这条排除说话，而不是在别处莫名其妙地红。
- **文案是我定的，不是负责人定的**。7.2 那三条尤其。中文名改起来很便宜
  （只改 `labels.ts` 一处），但改了要跑一遍前端测试——有几条逐字断言钉着文案。

## 九、follow_ups

- ~~7.2 的三条术语请负责人裁定~~ —— 已裁定（见 7.2），第 2 条已落成代码。
- 7.3 的两条既有不一致（`PrincipalType` 的 `MACHINE`、`ChannelProfitView` 的
  混合 switch）。
- 连接管理页落地时，`registry.ConnectionStatus` 要同时补表和补对账。
- **`docs/modules/notify/CATALOG.md`（运营看的告警目录）已核对：八条规则一条不
  少。** 也就是说，同一个事实的三处落点里，**只有前端那一份掉队了**——文档跟上了，
  代码没跟上，因为文档有人读、那份数组没人对。这正好说明为什么门禁要钉在代码
  这一侧。那份文档目前没有门禁，值得单独一条（它与 `alerts/rules.go` 同样可以
  对账）。
