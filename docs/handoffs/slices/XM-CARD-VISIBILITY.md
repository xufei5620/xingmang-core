# XM-CARD-VISIBILITY：让卡片同步说出上游到底为什么拒绝

- **status:** implemented + 复审补丁已合，未上线。后端 + 迁移 + 契约；
  `web/` 一个字没改（见「合并点」）。
- **branch:** `ai/claude/XM-CARD-VISIBILITY`，起点 `e4f7dcf`。
- **来源：** `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md` 的 card_sync 段与第三节第 4 条。
- **时间：** 第一轮 2026-09-08T16:18Z–17:20Z（约 1 小时）；
  复审补丁 2026-09-08T17:36Z–2026-09-09T00:05Z（约 6.5 小时）；
  门禁与 35 条变异复跑 2026-09-09T02:35Z–02:56Z（约 21 分钟）。

## 为什么

生产上 `card_sync` 连续 24 小时报错，日志、作业错误串与管理后台里能看到的
**只有 40 个字符**：

```
rejected: infini POST /v2/cards/status/batch
```

三天没人答得出「LINFENG 为什么被拒」。四件事叠在一起造成了这个结果：

1. 连接器把上游的业务码与 message 只放进 `Unwrap` 链，`Error()` 只回
   `kind + ": " + op`；而作业只记 `err.Error()`，链在这里被丢掉。
2. `rejected` 在全仓的定义就是「重试没有意义」，但**没有任何一处代码读过这个
   分类来决定重不重试**——每一轮照烧 3 次，24 小时里落了 288 条 discarded。
3. `RunOnce` 只返回一个 error：六步里砸一步，整轮就是失败。而「批量失败后退回
   逐张查」的兜底其实一直在正常工作，卡状态被刷新着，红的只是作业状态。
4. 运营没有任何办法把一个正在被上游拒绝的账号临时摘出同步循环。

## 改了什么

### 1. 错误可见（范围 1）

- `internal/platform/connector/types.go`：`Error` 新增 `Detail string`
  与 `NewErrorWithDetail(kind, op, detail, cause)`；`Error()` 在 Detail 非空时
  追加 `(detail)`。**`NewError` 的行为一字节未改**——其余六个连接器的对外
  错误文本完全不动，由 `TestNewErrorTextUnchangedForOtherConnectors` 钉住。
  另有 `DetailOf(err)` 供调用方显式取脱敏简述。
- `connectors/infini/redact.go`（新）：`redactUpstreamText`。像凭据的字段
  （token/secret/key/password/authorization/signature/digest/cvv…）**整个值
  去掉**；12~19 位数字串（允许组间空格/连字符）**打成 `****` + 末 4 位**。
  分隔符类含**全角冒号与全角引号**——本仓库上游文案按约定用全角标点，
  只认 ASCII 冒号的掩码在这个仓库里已经漏过一次。
- `connectors/infini/envelope.go`：四个错误分支全部改走 `NewErrorWithDetail`。
  `bodyPrefix` 现在**先脱敏再截断**（反过来的话，跨 300 字节边界的卡号会被
  切成两截而双双逃过 digitRun）；新增 `rawBodyPrefix` 只截断不脱敏，仅供
  `Unwrap` 链。
- `connectors/infini/endpoints.go`：`switchOp`（冻结/解冻）与 `DeleteCard`
  的 `data.success == false` 分支同样带上脱敏后的原话。
- `internal/platform/jobs/card_sync.go`：日志新增 `round`（按账号/步骤的
  完整结果）、`first_failure_account/step/kind/detail`。

**这是对既有写法的一次有意放宽，已写进契约。** ADR-004 的铁律原文是
「第三方错误不得原样透传给**用户**」；实现此前执行成「不透传给**调用方**」，
严过 ADR 一档。放宽的边界有两条硬约束：对外文本只带**码 + 脱敏后的 message**，
绝不带原始响应体；给最终用户看的那一层仍由 `cards/upstream_error.go` 翻成中文
指引，一个字的上游原文都不到那里。**ADR-004 本身没改**，改的是
`contracts/connectors/infini.card.v1.md`（那里此前把 ADR 复述严了）。

### 2. rejected 不重试 / 部分成功可见（范围 2）

- `internal/platform/cards/sync_result.go`（新）：`RoundResult` / `StepOutcome`
  / `stepTally`。`AnySucceeded()` / `AllFailed()` / `Retryable()` / `Summary()`。
- `RunOnce(ctx)` → `(RoundResult, error)`。**error 只在整轮全砸时非 nil**。
- `refreshTrackedCards`：一批被 `rejected`/`auth`/`ip_not_allowed` 拒掉之后，
  **这个账号的后续批次一批都不再发**（再发 100 张不是一个不同的请求）；
  只中断这个账号，保住既有的「一批失败不该让其余账号报废」。
- `CardSyncWorker.Work`：全砸且**没有一条值得再试** → `river.JobCancel(err)`。
  错误照样返回，所以不是静默成功；下一个 5 分钟周期本身就是重试。
- 「批量失败后退回逐张查」的兜底**行为一个字没改**，但现在会记
  `StepBatchStatusFallback` 与 `Recovered` 计数——这正是「同步真坏了」与
  「批量坏了但兜底接住了」的分界。

### 3. 按账号暂停（范围 3）

- 迁移 `000055_card_account_sync_pause`（`cards.account_sync_pause`）。
  行缺席 = 未暂停；`paused` 仍显式存布尔，好让 resume 之后 reason 与时间戳
  留得下来。预留 `expires_at`，**本片不实现自动恢复**。
- Action `cards.account_sync.pause` / `.resume`（`cards/account_sync.go`，
  在 `actions.go` 只追加两行）。**L1，不抬级**——理由见下面「替负责人做的决定」。
- `PgStore.SetAccountSyncPause` / `PausedAccounts`；`Store` 接口加这两个方法。
- Syncer **每轮只读一次**开关；读不到时**整轮失败，不回落到「当作没暂停」**。
- 六个遍历全部跳过暂停账号，结果里带中文 `SkipReason`（含运营填的理由原文）。

### 4. 告警（范围 4）

- `internal/platform/alerts/rules_cards.go`（新）：规则声明 + 判定。
  「同一账号同一步骤」连续 `ConsecutiveFailureThreshold`（默认 3）轮失败才开；
  **迟滞**：要连续 `cardSyncRecoverySamples = 2` 轮成功才算恢复。
  暂停的账号一条都不报。当前轮没失败时**完全不回看历史样本**。
- 新指标 `cards.sync.status`：`jobs.MetricCardSyncStatus`，由 `CardSyncWorker`
  每轮写一条 `ops.Observation`（`value_json` 里的 detail 是**已脱敏**那一份）。
- 连带四处必改：`ops/freshness.go` 白名单、`ops/metrickeys_test.go`、
  `contracts/ops/metric-rollup-policy.v1.json`、`ops/rollup_policy_test.go`
  的 `25/8` → `26/8`。

### 5. 批量上限 100 只钉一处（范围 5）

- `infini.batchStatusMax` → 导出的 `infini.BatchStatusMax`；删掉
  `cards/sync.go` 里重复的 `batchStatusChunk = 100`。
- `sync.go:246` 的 `discoveryPageSize = 100` 是**另一个数**（列表翻页大小），
  **没有**合并——「只钉一处」指的是同一个数字，不是同一个数值。
- 本地前置拒绝的 Detail 以「平台侧前置拒绝：」开头，与上游拒绝可辨
  （两者 Kind 与 op 逐字相同）。
- `TestBatchStatusMaxMatchesOpenAPI` **读契约文件**
  （`card.yaml` 的 `maxItems`），不复述那个数字。

## 复审补丁（第二轮，2026-09-09）

复审在真实形状上打出 7 个洞（2 个 fatal、5 个 major）。**每一个都先用探针在
worktree 里复现，再修，再补一条会红的用例。** 探针本身不留在树里，
它们的内容都变成了正式用例。

### 1（fatal）脱敏器对复合字段名整词匹配 → 复合键名的凭据原样进对外文本

`{"access_token":…}` / `{"client_secret":…}` / `{"x_api_key":…}` 全都漏过：
`\btoken\b` 在 `access_token` 里因为下划线是词字符而根本匹配不到。荒唐的是
`x-api-key`（连字符）挡得住而 `x_api_key`（下划线）挡不住——防线成不成立取决于
上游用了哪个分隔符。

这条不是「内部日志多了点东西」：这段文本进 ops 观测的 `value_json`，由
`rules_cards.go` 原样送进 `Finding.Detail`，再由 `notify.go` 投到 Telegram /
企业微信 / 通用 webhook，也就是**发出平台之外**。

**修法**：键名判定改成小写**子串**匹配，语义与
`internal/platform/audit.RedactDefault` 对齐（那边的注释早就写明「子串匹配，
覆盖 Access_Token 这类变体」）。为了让两份清单不会各自漂开，给 audit 加了
`SensitiveKeyFragments()`（返回副本），`connectors/infini/redact_shared_test.go`
**遍历**它逐项验证覆盖——那边加一项而这边漏了会当场红。这是「闸的范围要发现
不要手列」：手写一份「我覆盖了哪些」的对照表只会一起漂。

生产依赖方向没变：只有**测试**引用 audit，连接器的生产代码不 import 它。

### 2（fatal）脱敏跑在压平换行之前 → 跨行的键值对整对逃掉

分隔符字符类不含 `\n`，而压平又发生在脱敏之后，于是
`{\n "api_key":\n "sk-live-…"\n}` 先逃过脱敏、再被排成一行漂亮的可读文本。
美化过的 JSON 与网关错误页正是响应体最常见的两种形状。

**修法**：压平提到 `redactUpstreamText` 的**第一行**（而不是只放在 `bodyPrefix`
里）——三个 message 入口根本不经过 `bodyPrefix`，只改那里等于只覆盖一半的路。

### 3（major）`message` 那条路没有长度上限

响应体有 300 字节上限，`message` 一个字都不截：实测上游回 20 万字节的
message，`err.Error()` 就是 20 万字节。它会每 5 分钟落一次 ops 观测的
`value_json`、进 `alerts.detail`（无上限的 text 列），最后进 Telegram
sendMessage 的 body（单条上限 4096 字符）——一个话痨或被打崩的上游既能把库
撑大，又能让 `cards.sync.failed` 投不出去，而那条告警正是本片用来替换
「作业变红」这个响亮信号的东西。

**修法**：新增 `safeUpstreamText`（压平 → 脱敏 → 截断）作为**唯一**入口，
三个入口一起改。并加一条扫源码的门禁 `TestOnlySafeUpstreamTextReachesOutwardText`：
非测试文件里除 `redact.go` 外不许出现 `redactUpstreamText(` 的调用点。
写成源码扫描而不是再写三条针对具体入口的用例，是因为行为测不出
「下一个人会不会绕过去」——而这次正是三个入口各自忘了截断，且每条都全绿。

### 4（major）「观测必装」只写在注释里

`client.go` 那行 `.WithObservations(...)` 靠一句注释声明必装，没有任何测试
会因为它被删掉而红。而本片做了一次**有意的信号让渡**：部分成功返回 nil、
全砸且不值得再试落 `cancelled`，作业不再变红，于是「某个账号一直在失败」
只剩 `cards.sync.status` 这一个承载物。删掉那一行，全仓门禁照样全绿、
告警从此结构上不可能响、而且不会有任何报错。

**修法**：抽出 `newCardSyncWorkerFor(cfg, pool)`，加
`TestCardSyncWorkerAssemblyWiresObservations` 断言 observations 非 nil、
environment 与 interval 都传对了。

### 5（major）相邻词分支无条件吞掉下一个词 → 把答案吃掉，同时漏掉真凭据

实测：`signature mismatch for request 88213` → `signature [REDACTED] for …`；
`token expired at …` → `token [REDACTED] at …`；
`authorization denied for account LINFENG` → `authorization [REDACTED] for …`。
token / secret / signature / authorization 恰恰是拒绝类 message 里最高频的词，
而它们后面那个词**正是答案**。运维会拿到一句比三天前只多一个词的话。

反过来，凭据不紧贴标签时反而漏：本连接器自己的签名头
`Signature keyId="ak_live_…",…,signature="…"` 里，被吃掉的是字面量
`Signature` 这个词，而 `keyId` 原样留在一个 `[REDACTED]` 旁边——读日志的人
会以为这一段已经处理过了。

**为什么没被测出来**：`redact_test.go` 的夹具字面就是
`signature mismatch for api_key sk-live-…`，它自己带着这个 bug 却看不见，
因为**全套用例里没有一条正向断言「message 的人类可读部分要活下来」**。
缺席断言做了双向变异，正向断言这一侧一条都没有。

**修法**：拆成三件事——(a) 带分隔符的 `k=v` 无条件去值（这部分原本就是对的）；
(b) 没有分隔符的散文只在下一个词 `looksLikeSecret` 时才去（长度 ≥16 且数字
字母混排、或 ≥20 且大小写混排、或 `sk-`/`ak_` 这类前缀）；(c) 安全码另判，
`cvv`/`cvc`（子串，因此 `cvv2` 也命中）后面的 3~4 位数字一律去掉。
scheme（`Bearer`/`Basic`/`Negotiate`）后面那个 token 连 scheme 一起吃掉。
新增 `TestRedactKeepsDiagnosticProse` 逐字钉住三句诊断原话——**这是整组用例里
唯一能防住「脱敏器把答案吃掉」的东西**。

顺带修掉两个同源的坑：正则匹配是**不重叠**的，
`invalid cvv2 123` 会先被 (invalid, cvv2) 那一对吃掉、真正要判的 (cvv2, 123)
根本轮不到；`upstream said: token = sk-live-x` 会先被 (said, token) 吃掉。
两条规则都改成「只匹配键，值手工向后取」。

### 6（major）迟滞是死代码：声明 2 轮，实际 1 轮

`cardSyncFindings` 只给「**这一轮正在失败**」的键去数连续串，而
`cardSyncFailureStreak` 的迟滞循环只有在「最新样本是成功」时才进得去——
而当前观测**就是**最新样本（同事务写两份，代码注释自己也这么写）。两句话互斥，
迟滞循环永远进不去。于是失败 N 轮之后**成功 1 轮**，Reconciler 当轮就把告警
恢复掉了。

代价是对运维说了假话两处：`Rule.Recovery` 与告警正文都逐字写着
「需连续 2 轮成功才算恢复」。LINFENG 这种 80% 失败率的账号撞上一轮走运，
告警就会 resolve、下一轮再 fire——抖几次之后运维会把整条规则静默掉，
那才是真正把预警关掉。

原来那条用例之所以绿，是因为夹具把 `current` 造成失败、却在样本末尾追一条
成功——正是代码注释断言不会发生的那个状态。

**修法**：选了「真做迟滞」而不是「删掉迟滞」（派工第 4 条明写「有迟滞」）。
候选键改成取自**整个窗口**而不是「这一轮正在失败的」；`cardSyncStreakOf` 同时
返回失败串长度与末尾成功轮数，`open()` 判「失败 ≥ 阈值 且 末尾成功 < 2」。
恢复中的告警正文改成「连续 N 轮失败后刚成功 1 轮，还差 1 轮才算恢复」，
上游原话取自**最近一次失败**那一轮。**所有夹具改成生产形状**（current == 最新样本）。

**一处成本纪律被推翻，请负责人知悉**：原来「当前这一轮没有失败就完全不回看
历史样本」（照抄 R4）现在**不成立**——迟滞要判的正是「这一轮成功了但还没算
恢复」，而这一轮全绿恰恰是必须回看的时刻。留着那条省法等于用一条恒绿的成本
断言把迟滞永远钉死在关闭状态。代价是每个评估周期对 `cards.sync.status` 多一次
`ListSamples`（limit 20）。换上的成本断言是**更值得钉的那条**：
`TestCardSyncFailedReadsSamplesOncePerRound` 要求一轮只查一次，
不随失败的 (账号, 步骤) 数放大。

### 7（major）六个暂停检查点里有两个一条测试都没穿过

把 `advanceWithdrawals` 与 `reconcileOperations` 的暂停跳过整段删掉，
`cards` + `jobs` 全量**照样全绿**。原因是夹具里 `SyncOptions.Withdrawals` 是
nil（第一行就 return）、store 里也没有未收敛的操作（循环一次都不进）。
其中 `advanceWithdrawals` 是**动钱**那一步：一个被人为停掉的账号会继续被打
上游推进提现，而门禁不会有任何反应。

**修法**：夹具补上两个账号各一笔未收敛提现与一笔未收敛开卡；`spyClient` 加
`withdrawCalls`，并把带 alias 的列卡单独计成 `aliasListCalls`（对账与发现走的是
同一个上游方法，不分开就分不清删的是哪一处）。**七个计数器各配一条正向对照**
——夹具里没有那件活要干时，0 次调用不说明任何事。新增变异 M30 / M31 各自发红。

## 新增 API 字段清单（供 XM-WORKBENCH-TRUTH 消费）

`GET /api/v1/cards` 的响应信封新增一个顶层字段（`items` 内的字段一个没动）：

```jsonc
{
  "items": [ ... ],           // 未变
  "accounts": [ ... ],        // 未变
  "member_emails": [ ... ],   // 未变
  "paused_accounts": [        // 新增
    {
      "account": "LINFENG",
      "reason": "上游拒绝待查 XM-CARD-VISIBILITY",  // 运营填的，可能缺席
      "paused_by": "human:ops",                      // 可能缺席
      "paused_at": "2026-09-08T16:00:00Z"            // RFC3339 UTC，必有
    }
  ]
}
```

- 空数组表示「没有账号被暂停」**或**「暂停清单读失败」。读失败时整页仍返回
  200（同 `member_emails` 的既有纪律），**不做任何暗示**。
- 建议前端在卡片列表里给该账号的行挂一枚「已暂停同步」徽标，鼠标悬停显示
  `reason`，并按 `paused_at` 显示「已暂停 N 小时」。**后面这一半不是装饰**：
  告警对暂停账号是静默的，所以「忘了恢复的暂停」只能靠这个徽标被看见
  （两半缺一不可，见下面的决定）。

新增两个 Action（后台按钮）：`cards.account_sync.pause`（参数 `account`、
`reason`，两者必填）与 `cards.account_sync.resume`（参数 `account`）。
权限 `card.manage`，L1，仅人类身份。契约在
`contracts/actions/cards.account_sync.{pause,resume}.v1.json`。

## 合并点与需要别人做的事

### `internal/platform/alerts/rules.go`（XM-OPS-TRUTH 持有）—— 三处

派工说「若必须在 rules.go 注册，只允许追加一行」。**做不到一行，这里说明为什么**：

1. **常量 `RuleCardSyncFailed`（1 行 + 注释）必须在这个文件里。**
   `web/apps/admin-web/src/lib/labels.reconcile.test.ts:313` 用
   `goNamedConsts(goSource("internal/platform/alerts/rules.go"), /^Rule[A-Z]/)`
   抽规则键——**只读这一个文件**。常量挪到 `rules_cards.go` 会让那道门禁
   看不见新规则从而**恒绿**，规则就带着一个没有中文名、在静默下拉里选不到的
   键上线。宁可让门禁红着，也不要用文件位置把它骗绿。
2. **`Rules()` 的 2 行边界改动**：`return []Rule{` → `return append([]Rule{`，
   结尾 `}` → `}, cardSyncRules(cfg)...)`。必须进这个切片：`RuleKeys()` /
   `KnownRuleKey()` 都由它派生，而 `KnownRuleKey` 是静默窗口的存在性校验
   ——不在这里的规则**静默不了**。
3. **`Evaluate()` 里 7 行**：接在 `ApprovalQueueMetricKey` 那一段后面。

三处都在长字面量的两端或紧挨既有分支，冲突面已经压到最小。规则本体
（Rule 声明、判定、解析）全在新文件 `rules_cards.go`。

### `web/`（XM-WORKBENCH-TRUTH 持有）—— 两处，我不能改

1. `src/api/alerts.ts` 的 `ALERT_RULES` 加一条：
   `{ key: "cards.sync.failed", label: "卡片同步连续失败" }`。
   **label 必须与后端 Title 逐字相同**（`labels.reconcile.test.ts` 的最后一条
   断言就是比这个）。缺了它，运营在静默对话框里**根本选不到这条规则**。
2. `src/lib/labels.reconcile.test.ts:311` 的 `rulesSource` 只读 `rules.go`，
   于是抽不到 `rules_cards.go` 里的 `Key: RuleCardSyncFailed, Title: "…"`，
   `titleByKey.keys()` 会比 `ruleKeys` 少一条 → 那条断言会红。
   **正确的修法是把 `rulesSource` 扩成整个 alerts 包（glob），不是把
   Rule 声明搬回 rules.go**：一个「校验器只读手列的那一个文件」的门禁，
   本身就是「闸的范围要发现不要手列」的病例。

**在这两件事落地之前，前端 `labels.reconcile.test.ts` 会红两条。** 这是有意
承担的、可见的红，不是遗漏。

### 未做、且**不该顺手做**的

- **dbroles**：`contracts/database/role-policy.v1.json` 的 objects 只覆盖
  core/action/audit/ops/alerts/finance/public/ui **八个 schema，`cards` 一个
  对象都没有**（现存 8 张 `cards.*` 表也全都不在里面）。所以
  `cards.account_sync_pause` 今天在 dbroles 侧无处可登记；而 dbroles 策略是
  审批门控的，本片不扩。**这是一处既有欠账，报告但不关闭。**
  （派工提到的 `internal/platform/dbroles/drift_gate*` 在本工作树不存在，
  该目录下只有 policy.go / transition.go / verifier.go。）
- `ops/rollup_policy_test.go:40` 的 `len(policies) != 26` 与它上面两行的
  `TestPolicyCoversExactlyRegisteredMetrics`（钉的是集合）**是冗余的**，
  而且每加一条指标就要手改一次——正是本片在消灭的那种「同一个数字钉在两处」。
  删它属于 ops 的范围，本片只把 25 改成 26。
- `gofmt -l` 在本分支基线上就报 4 个文件
  （`connectors/infini/{alignment_test.go,endpoints.go}`、
  `internal/platform/httpapi/finance_test.go`、
  `internal/platform/integration/types_test.go`）。**我一个都没新增，也一个
  都没顺手格式化**——那会把无关改动混进本片的 diff。已核实：stash 掉本片全部
  改动后，同一份工具链 gofmt 报的是同样这 4 个文件。

## 上线后负责人需要做的动作

**第一次看到上游原话之后怎么判断 LINFENG。** 这是本片交付的全部意义：
它没有修好那个拒绝，它只是让原因终于可见。

1. 部署后等一个同步周期（5 分钟），到管理端「指标历史」查
   `cards.sync.status`，或直接看 worker 日志里 `msg=卡片同步部分成功` /
   `卡片同步未完全成功` 那条记录的 `first_failure_detail` 字段。
   连续 3 轮之后 `cards.sync.failed` 告警的正文里也会有同一句话。
2. 照那句话读，**不要拿下面这条假设去反推**：
   - 看到 **`upstream code 401` / `403`**：分类会自动落到 `auth` /
     `ip_not_allowed`，去核密钥与出口 IP 白名单，与本片无关。
   - 看到 **`upstream code` 后面跟着一句提到 id / card_ids / not found /
     scope 的话**：那就指向契约验证清单新加的第 11 条——
     `POST /v2/cards/status/batch` 的 `card_ids` 在 OpenAPI 里被描述成
     「Internal ORGANIZATION_CARD primary key ids」，而我们传的是
     `GET /v2/cards/list` 回来的 `id`。若确实是 id 空间不同，修法是在
     连接器里补一次 id 映射，**不是**改重试策略。
   - 看到 **提到权限的话**：注意我们**确实持有** `card.create`
     （契约「已知的上游权限划分」段），所以「权限不足」不是显然的解释；
     要么是上游新增了细分权限，要么是这条报错另有所指——把原话贴进契约的
     验证清单再判。
   - 看到 **「平台侧前置拒绝：一次最多 100 张，收到 N 张」**：这不是上游的
     问题，是我们自己发超了；但这一条现在不可能出现（分批步长与上限已经是
     同一个常量，并有对撞测试）。真出现了说明有人绕过了 `BatchCardStatus`。
3. **判定之前先把那句原话抄进** `contracts/connectors/infini.card.v1.md`
   的验证清单第 11 条（宪法 19 条：决定未回写仓库不算正式决定）。
4. 如果一时修不了：用后台的 `cards.account_sync.pause` 把该账号停掉并写明
   原因。**停掉之后 `cards.sync.failed` 对这个账号就不再响了**，所以请同时
   在卡片页看一眼暂停徽标——别让它停到下个月。
5. **`river.JobCancel` 的一个副作用要知道**：`rejected` 全灭的轮次现在在
   River 里落成 `cancelled` 而不是 `discarded`。盯 discarded 数量的看板会
   突然「变好看」，那不是修好了，是分类变了。真正的信号改看
   `cards.sync.failed` 与 `cards.sync.status` 的 `all_failed`。

## 变异验证

每一条都：改坏 → 跑定向测试确认发红 → 还原 → 复跑确认发绿。
最后用 `cmp` 逐文件核对 10 个被改过的文件与备份**逐字节相同**。

**M1–M23 是第一轮的，M24–M35 是复审补丁的。第二轮把 35 条全部重跑了一遍**
（不是只跑新增的那 12 条）：复审补丁动了脱敏器、告警判定与 `RunOnce` 周边，
旧变异是否**还**发红是一件要重新证明的事，不能从上一轮的结论继承——
第一轮的 M19 恰恰就是「发红了但发红的理由是错的」（夹具不是生产形状）。

| # | 变异 | 变红的测试 |
|---|---|---|
| M1 | `redactUpstreamText` → `return text`（恒等） | `TestRedactUpstreamTextRemovesCredentialsAndMasksCardNumbers` 的**缺席断言全红**（6 个子用例）、`TestBodyPrefixRedactsBeforeTruncating`、`TestDoSurfacesRedactedUpstreamMessage`、`TestDoRedactsRawBodyOnHTTPError` |
| M2 | `redactUpstreamText` → `return ""`（反向） | 同一批的**正向断言**红，**缺席断言 0 条红** —— 两条变异合起来才证明缺席断言不是恒真 |
| M3 | `bodyPrefix` 改成先截断再脱敏 | `TestBodyPrefixRedactsBeforeTruncating`（跨边界的卡号漏出来） |
| M4 | 信封 `code != 0` 分支退回 `NewError`（丢 Detail） | `TestDoSurfacesUpstreamCodeAndMessageInErrorText`、`TestDoSurfacesRedactedUpstreamMessage`、`TestLocalPreRejection…`、`cards.TestSyncErrorCarriesUpstreamCodeAndMessage` |
| M5 | 作业日志不再记按账号/步骤的失败明细 | `TestCardSyncWorkerLogsUpstreamReason` |
| M6 | 去掉 `river.JobCancel` 包装 | `TestCardSyncWorkerCancelsInsteadOfBurningRetriesOnRejected` |
| M7 | `retryableKind` 把 `rejected` 当成可重试 | `TestRoundRetryabilityFollowsUpstreamClassification/上游明确拒绝` + 上面那条作业用例（**分类是从真上游响应算出来的**，不是测试编的） |
| M8 | 本地前置拒绝的文案改成 `upstream code 0: …` | `TestLocalPreRejectionIsDistinguishableFromUpstreamRejection` |
| M9 | 领域层分批步长漂到 150（连接器仍 100） | `TestBatchChunkNeverExceedsConnectorLimit` |
| M10 | `BatchStatusMax` 100 → 50（与契约漂开） | `TestBatchStatusMaxMatchesOpenAPI` |
| M11 | 只删 `discoverCards` 里的暂停跳过 | `TestSyncSkipsPausedAccountAndSaysSo` 的 `listCalls == 0` |
| M12 | 只删 `refreshTrackedCards` 里的跳过 | 同一条的 `batchCalls == 0` |
| M13 | 只删 `fetchMissingSecrets` 里的跳过 | 同一条的 `revealCalls == 0` |
| M14 | 只删 `syncTransactions` 里的跳过 | 同一条的 `txCalls == 0`（五个计数器分开，就是为了一次只删一处时分得清） |
| M15 | 跳过了但不写中文跳过原因 | 同一条的 `SkipReason` 含「已暂停」+ 理由原文 |
| M16 | 暂停开关读不出来时回落到「当作没暂停」 | `TestSyncFailsClosedWhenPauseSwitchUnreadable` |
| M17 | 兜底救回来的卡数不再计数 | `TestBatchFailureFallsBackToPerCardAndIsVisible` |
| M18 | 上游拒绝后仍继续发该账号的后续批次 | `TestRejectedBatchStopsRemainingChunksForThatAccount`（发了 2 批） |
| M19 | 迟滞 2 → 1（一轮走运就清告警） | `TestCardSyncFailedHasRecoveryHysteresis` |
| M20 | 告警去重键去掉步骤 | `TestCardSyncFailedNeedsNConsecutiveRounds` + `TestCardSyncFailedSplitsByAccountAndStep` |
| M21 | 告警只按步骤 `skipped` 抑制，不按账号 `paused` | `TestCardSyncFailedSuppressesPausedAccounts` |
| M22 | 连续轮数阈值降到 1 | `TestCardSyncFailedNeedsNConsecutiveRounds` + `TestCardSyncFailedRequiresSameStep` |
| M23 | `AllFailed()` 退回「有失败就算整轮失败」 | `TestSyncIsolatesFailingAccountAndReportsPartialSuccess` + `TestSyncPANFetchFailureDoesNotAbortTheRound` + `TestSyncKeepsUnknownIntactWhenUpstreamUnavailable` |

复审补丁新增的 12 条（每一条都对应上面「复审补丁」里的一个洞，
**先复现原缺陷、再修、再让这条变异把修法钉死**）：

| # | 变异 | 变红的测试 |
|---|---|---|
| M24 | `isSensitiveKey` 的子串匹配改回整键相等（复现 fatal 1） | `TestRedactCatchesCompoundCredentialKeys`、`TestRedactCoversAuditSensitiveKeys`、`TestRedactHandlesSignatureHeaderShape` |
| M25 | `valueSpan` 不吃 scheme（只盖住 `Bearer` 这个词） | `TestRedactEatsCredentialAfterAuthScheme` 4 个子用例中的 3 个 + `TestRedactFlattensBeforeMatching` 的 html 子用例 |
| M26 | 去掉 `redactUpstreamText` 第一行的压平（复现 fatal 2） | `TestRedactFlattensBeforeMatching` 3 个子用例全红 |
| M27 | `safeUpstreamText` 不截断（复现 major 3） | `TestUpstreamMessageIsLengthCapped`（实测 20 万字节进对外文本）、`TestUpstreamRefusalMessageIsLengthCapped` 两个子用例 |
| M28 | `newCardSyncWorkerFor` 不挂 `.WithObservations`（复现 major 4） | `TestCardSyncWorkerAssemblyWiresObservations` |
| M29 | `looksLikeSecret` 恒为真（复现 major 5：把答案一起吃掉） | `TestRedactKeepsDiagnosticProse` **7 个子用例里红 6 个**——`signature mismatch` / `token expired` / `authorization denied` 三句逐字复现了复审报告里的输出 |
| M30 | 只删 `advanceWithdrawals` 里的暂停跳过（**动钱**那一步） | `TestSyncSkipsPausedAccountAndSaysSo` 的 `withdrawCalls == 0`。**修补丁之前这条变异全绿**——夹具的 `SyncOptions.Withdrawals` 是 nil |
| M31 | 只删 `reconcileOperations` 里的暂停跳过 | 同一条的 `aliasListCalls == 0`。**修补丁之前也全绿**——store 里没有未收敛的操作 |
| M32 | 候选键退回「只看这一轮正在失败的」（复现 major 6 的迟滞死代码） | `TestCardSyncFailedHasRecoveryHysteresis`（失败 N 轮 + 成功 1 轮，实际报 0 条） |
| M33 | 给 `audit.defaultSensitiveKeys` 加一项 `session_id`，infini 侧不动 | `TestRedactCoversAuditSensitiveKeys/session_id`——证明这道跨包门禁是**遍历**出来的，不是手抄的对照表 |
| M34 | `endpoints.go` 绕过 `safeUpstreamText` 直接调 `redactUpstreamText` | `TestOnlySafeUpstreamTextReachesOutwardText`（源码扫描）+ `TestUpstreamRefusalMessageIsLengthCapped` 两条同时红 |
| M35 | 把 `ListSamples` 挪进 (账号,步骤) 循环里 | `TestCardSyncFailedReadsSamplesOncePerRound`（`sampleCalls=4`） |

M30/M31 是这一轮**最值得单说**的两条：它们在复审补丁之前**变异后仍然全绿**
（`cards` + `jobs` 两个包一起跑），也就是说那两处暂停检查点的正确性当时完全
不设防，而其中一处是打上游推进提现的那一步。修的不是实现——实现本来就对——
修的是夹具：让被暂停的账号真的**有活要干**，缺席断言才不是恒真的。

两处因为变异验证而**被加强的测试**（原来的写法漏得过）：

- 脱敏用例原本先查「存在」再查「缺席」，恒等变异会在第一组就 `Fatalf` 停下，
  缺席那一组**一次都没执行到**。改成**先查缺席**、并把 `Fatalf` 换成 `Errorf`。
- `TestCardSyncFailedSplitsByAccountAndStep` 原本用两个**不同账号**，去掉去重键
  里的 step 之后仍然是两个不同的键 → 变异不发红。改成**同一账号的两个步骤**。
- M21 暴露出 `paused[key.account]` 这个判断在原来的夹具下是**恒真的多余条件**
  （当前轮的 skipped 步骤本来就被 `step.skipped` 过滤掉了）。没有删掉它，而是
  把夹具改成「同一轮里既有 skipped 步骤又有 failed 步骤」——判据是「这个**账号**
  被暂停了」而不是「这一条步骤被跳过了」，将来任何一个忘了检查暂停开关的新步骤
  都不会把一个已经停掉的账号重新叫醒。

## 测试与门禁

跑过（全绿）：

```
go vet ./connectors/infini/ ./internal/platform/{connector,cards,jobs,alerts,ops,httpapi}/
go test -p 1 -count=1 ./connectors/infini/ ./internal/platform/connector/ \
  ./internal/platform/cards/ ./internal/platform/jobs/ ./internal/platform/alerts/ \
  ./internal/platform/ops/... ./internal/platform/httpapi/
go test -p 1 -count=1 ./internal/platform/audit/
go test -p 1 -count=1 -run TestPgStore ./internal/platform/cards/   # 带 XM_TEST_DATABASE_URL
bash scripts/check-governance.sh                                     # exit 0
```

第二轮（复审补丁）实测耗时：`go vet`（8 个包）+ 上面那批 `go test`
2026-09-09T02:48:31Z → 02:48:44Z，**13 秒**；`check-governance.sh`
02:48:49Z → 02:48:53Z，**4 秒**。

**没跑**：`go test ./...` 全量（派工明令不跑）；前端（本 worktree 无
`node_modules`，且 `web/` 不在本片范围）；任何真实上游调用。

**第二轮没能重跑带库的那一条**（`-run TestPgStore`）：本次会话的机器上
`psql` 不在 PATH、Docker daemon 也没起，`scripts/dev/worktree-testdb.sh --print-url`
直接报「找不到可执行文件: psql」，于是 `XM_TEST_DATABASE_URL` 取不到、
那些用例按既有约定 skip。第一轮跑过（迁移 000055 真上过测试库、
`TestPgStoreAccountSyncPauseRoundTrip` 真跑过 Postgres 往返），而复审补丁
**一行 SQL、一行 store_pg.go 都没动**——但这仍然是一条「上一轮的绿」，
合入前请在有库的机器上补跑一次。

新增表已加进 `store_pg_integration_test.go` 的 TRUNCATE 清单
（漏了的症状很隐蔽：单跑绿、连跑红）。迁移 000055 已在本 worktree 的测试库
上真跑过（`version=55 dirty=false`），`TestPgStoreAccountSyncPauseRoundTrip`
验的是真 Postgres 上的往返。

### 第三轮（主控者补跑全量，2026-09-09）

派工明令不跑的全量，主控者在有库的机器上补跑了，抓到两处两轮都没看见的漏：

1. `internal/platform/notify` 的 `TestCatalogListsEveryAlertRule` 红：新规则
   `cards.sync.failed` 没写进 `docs/modules/notify/CATALOG.md`。已补一行（触发、
   自愈、该做什么，含「先暂停止血再恢复」的口径），表头「七条规则」改「九条」
   （之前 `approval.pending.too_long` 加进来时也没改）。
2. admin-web 的 `labels.reconcile.test.ts` 两条红：静默对话框下拉里没有这条规则的
   中文名；而「中文名逐字取自后端 Title」那条只扫 `rules.go`，本片把声明放在
   `rules_cards.go`，它根本看不见。修法：`api/alerts.ts` 的 `ALERT_RULES` 加
   `{ key: "cards.sync.failed", label: "卡片同步连续失败" }`；测试改成走目录发现
   `rules*.go`（非测试文件）并断言至少发现 `rules.go` 与 `rules_cards.go`。
   变异：把发现范围退回只认 `rules.go` → 「抽取器确实抓到了」与「Title 逐字」两条红，
   还原后绿。这是「闸的范围要发现不要手列」的又一次发作。

全量门禁（UTC，实测）：`go build` 0 / `go vet ./...` 0 / `go test -p 1 -count=1 ./...`
（`XM_TEST_DATABASE_URL` 指向 worktree 专用库 `xm_test_wt_xm_card_visibility`）
03:10:52Z 起 66s exit 0（含 `TestPgStoreAccountSyncPauseRoundTrip` 真跑）/ admin-web
typecheck 0、2115 用例全绿 / ui-admin 262 全绿 / `check-governance.sh` 0 /
gitleaks 11 条存量基线未新增。前端跑起来靠把 node_modules 用 junction 镜像到本
worktree（memory 里的既有做法），不是 `pnpm install`。

## 替负责人做的决定（都可以推翻，理由写在这里）

1. **`rejected` 一次退避重试都不给**，直接终结本轮该步骤。理由：`rejected`
   在全仓的定义就是「重试没有意义」，给它退避重试等于跟其余代码赖以推理的
   分类打架；分类若对某个码判错了，该修 `kindForBusinessCode`，不是修重试。
   **需要负责人权衡的反面**：`kindForBusinessCode` 的 default 分支是个 catch-all，
   一个**其实是瞬时**的未知码现在会从 3 次重试变成 0 次。缓解是它现在 15 分钟内
   就会在告警正文里带着原话出现——比三次静默重试严格地好。
2. **暂停不做自动到期**，`expires_at` 列先建好。理由：自动恢复会在没人看着的
   时候把同步重新打向一个仍然拒绝我们的上游，而这个切片存在的意义正是不要让
   失败反复且无人知晓。代价是「忘了恢复的暂停」，由 Query 的 `paused_at` 徽标
   承担——**这两半缺一不可**。
3. **暂停/恢复定 L1，不抬到 L2。** 三条理由：`assurance.probe.kill_switch.set`
   是同形状的先例且是 L1；L2+ 会把 `req.Params` 冻进审批单给第二个人看，而
   `reason` 是手打自由文本——抬级正是「粘贴进来的凭据被看见」的那条路径；
   需要审批的暂停开关在语义上自相矛盾（人去够它的时刻正是出事的时刻）。
4. **告警走新 ops 指标而不是给 Evaluator 注入 cards 数据源**（后者有
   `RunwaySource` 先例）。理由：注入要改 `Evaluator` 结构体 + 构造函数签名 +
   `Evaluate` 的 nil 判，全在 XM-OPS-TRUTH 持有的 `rules.go` 里；走指标只多三处
   加法式的契约改动，而且顺带把按账号/步骤的结果放上了 `/metrics/history` 的
   时间线。**需要负责人确认**：这是 `cards.*` 前缀第一次进 ops 指标命名空间
   （此前只有 sub2api/newapi/cpa/finance/platform）。
5. **告警对暂停账号完全静默**（而不是降级成 warning）。理由见决定 2 的两半。
6. **`RunOnce` 的 error 只在整轮全砸时非 nil。** 这意味着「6 步里砸 5 步」
   不再让作业变红。**这是一次真实的信号让渡**：替代它的是
   `cards.sync.status` 观测 + `cards.sync.failed` 告警。少了那两样，这次改动
   就是把一个吵闹但真实的信号换成一个安静的盲区——**所以它们不是可选的
   后续，是同一次改动的另一半**，两者都在本分支里。

## 风险

- **前端两条断言会红**，直到 XM-WORKBENCH-TRUTH 落地上面那两件事。
- **`cards` schema 完全在 dbroles 漂移门禁之外**（既有欠账，不是本片引入）。
- **批量拒绝的根因仍然未知。** 本片让原因可见，**没有**修好那个拒绝；
  契约里新加的第 11 条只是一条待验证的假设，请不要当结论用。
- **部分成功不再让作业变红**：若 `cards.sync.status` 观测因为装配漏项而没写
  （`WithObservations` 没接上），就会既不红也没告警。这条此前**只由一句注释
  守着**（复审 major 4），现在装配抽成了 `newCardSyncWorkerFor` 并由
  `TestCardSyncWorkerAssemblyWiresObservations` 钉住（变异 M28 发红）；
  形状仍由 `TestCardSyncWorkerWritesObservationWithPerAccountDetail` 钉。
  但那条测试守的是**这一个装配函数**，不是「River 客户端里确实用了它」——
  上线后仍请顺手确认 `/metrics/history?metric_key=cards.sync.status` 有数据。
- `connector.Error.Error()` 是**七个连接器共用**的类型。`NewError` 一字节未改，
  由 `TestNewErrorTextUnchangedForOtherConnectors` 与既有的
  `connector/transport_test.go` 一起守住。
