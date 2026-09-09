# alerts —— 告警中心（Foundation-A 档）

> 规格 §9.3 告警生命周期 · §9.4 告警渠道 · §22.2 第 13 项 · XM-0033

平台内部告警的评估、去重、静默与投递。它回答的是「**上游数据对不对、
渠道还能不能用**」；「**平台自己挂了怎么办**」由外部看门狗回答，
见 `deploy/watchdog/README.md`（宪法 23 条，两者必须分开）。

## 这个模块的四条铁律

1. **去重是第一位的。** 同一个真实问题在多轮评估里必须收敛成**一条**告警 +
   递增的 `fire_count`，而不是每分钟新开一条。去重键由规则声明，
   库层还有一条部分唯一索引兜底。
2. **静默不是解决。** 命中静默窗口的告警进 `SILENCED` 且不投递，但它仍然
   活着——窗口一过、条件仍成立，它要转回 `OPEN` 并重新投递。把静默实现成
   「删掉」或「标记已解决」等于让平台替人撒谎（宪法 12 条）。
3. **投递状态与告警状态正交。** 一条 `OPEN` 的告警完全可能还没投递出去，
   而那正是最危险的组合：有人以为「告警会通知我」。两个事实分开存、分开显示。
4. **凭据只在发请求那一瞬存在。** Telegram Bot Token、企业微信群机器人
   Webhook 地址均经 SecretProvider 解析，绝不进日志、错误串、`notify_error`
   或任何 API 响应（宪法 7 条）。

---

## 第一批规则

规则**写死在代码里**（`internal/platform/alerts/rules.go`），不入库。
理由：规则要跑代码——读 ops 观测、解 `value_json`、算持续时间。做成数据就得
同时发明一门表达式语言和它的沙箱，那是 Foundation-B 的事。在那之前，
改规则是一次带 PR、评审与测试的代码变更，这在本阶段反而是更强的保障。

规格 §9.3 要求每条规则包含九项，下表逐列对应。
`TestRulesDeclareAllNineSpecFields` 机械地保证一项都不为空。

| 规则键 | 名称 | 数据来源 | 条件 | 持续时间 | 严重度 | 恢复条件 | 去重键 |
|---|---|---|---|---|---|---|---|
| `metric.sync.failed` | 指标同步失败 | `ops.metric_observation`（当前态）+ `ops.metric_observation_sample`（迟滞判据） | **连续 3 轮**采集失败（少于 3 轮的抖动不报） | 3 个采集周期（默认 900s） | **critical** | **连续 3 轮**不再失败（中间只成功一两轮不算恢复） | `metric.sync.failed:<env>:<metric_key>` |
| `metric.data.stale` | 指标数据陈旧 | `ops.metric_observation` | 状态为 `stale` 且滞后 ≥ 阈值 + 2 个采集周期 | 2 个采集周期（默认 600s） | warning | `observed_at` 重回阈值内 | `metric.data.stale:<env>:<metric_key>` |
| `metric.sync.consecutive_failed` | 同步长期失败 | `ops.metric_observation_sample` | 当前正在失败，且**最近 12 条样本里失败 ≥ 9 次**（滑动窗口，不是连续串） | 12 个采集周期 | **critical** | 窗口内失败次数回落到 9 次以下（中途成功一两轮既不清零计数，**也不关闭告警**） | `metric.sync.consecutive_failed:<env>:<metric_key>` |
| `channel.token.invalid` | 渠道 token 失效 | `sub2api.channels.balance` 的 `channels[].token_valid` | `token_valid` **明确为** `false` | 0 | warning | 恢复为 `true`，或该渠道从上游清单消失 | `channel.token.invalid:<env>:<channel_id>` |
| `channel.balance.low` | 渠道余额不足 | `sub2api.channels.balance` 的 `channels[].balance_minor_units` | 余额 < 阈值（默认 500000 最小货币单位） | 0 | warning | 余额回到阈值之上，或该渠道消失 | `channel.balance.low:<env>:<channel_id>` |
| `upstream.runway.low` | 上游可用天数不足 | `finance.balance_history` ÷ `finance.profit_daily`（近 7 个完整业务日的日均消耗，设计稿 §10.4） | 计量型上游的可用天数**算得出来**且 ≤ warning 档（默认 10 天） | 0 | warning（≤ critical 档 5 天升为 **critical**） | 天数回到告警档之上，或不再算得出天数 | `upstream.runway.low:<env>:<upstream_account_id>` |
| `upstream.version.changed` | 上游版本变化 | connector probe 观测（`*.connector.health`）的 `version` 与同一指标的历史样本 | 最新观测的版本与历史里最近一个**不同**的版本不一致，且这个版本**没有被人核对过** | 0（立即） | warning | 有人执行 `alerts.upstream_version.acknowledge` 核对了这个版本，或下一轮不再变化 | `upstream.version.changed:<environment>:<metric_key>:<new_version>` |
| `approval.pending.too_long` | 审批单挂太久 | `platform.approval.queue` 的 `oldest_pending_age_seconds`（XM-0030c） | 最久那张**未过期**的 `PENDING` 审批单已等待 ≥ 4 小时 | 0（观测本身就是一个持续量） | warning | 最久那张降回门槛以内（有人批了或驳了），或队列清空 | `approval.pending.too_long:<env>:queue` |
| `cards.sync.failed` | 卡片同步连续失败 | `cards.sync.status` 的 `value_json.accounts[].steps[]`（XM-CARD-VISIBILITY） | 同一账号的**同一步骤**连续 3 轮失败（被暂停同步的账号一条都不报） | 3 个采集周期（默认 900s） | **critical** | 同一账号同一步骤**连续 2 轮**成功（1 轮走运不算），或该账号被暂停同步 | `cards.sync.failed:<env>:<account>/<step>` |

> 上面两行都是 `TestEveryRuleHasARowInBothDocs` 补出来的。
> `approval.pending.too_long` 从 XM-0030c 起就在 `Rules()` 里，却一直没写进
> 这张表；`cards.sync.failed` 是 2026-09-09 集成合并时补的——它在
> XM-CARD-VISIBILITY 分支上只写进了 CATALOG.md，而要求「两份文档都有行」的
> 那条测试当时还在 XM-OPS-TRUTH 分支上，两条分支各自门禁全绿，
> 合到一起才红。那条测试的覆盖范围从 `Rules()` **发现**而不是手列，
> 所以漏一条就当场红。

余下三项（通知渠道 / 静默策略 / 负责人）全部规则相同：
渠道 = 已配置的 telegram + webhook；静默策略见下一节；负责人 = `platform-ops`。

### 可用天数规则（R5）的三处取舍

**它是唯一一条不读 ops 观测的规则。** 前四条的数据来源都是
`ops.metric_observation`，而可用天数是平台**自己算出来的**（余额 ÷ 近 7 日
日均消耗，两侧原料都在自己的库里）。把它塞进一条 ops 指标再由本包解 JSON，
会多出一处「谁来算」与一处「怎么解」，而算它的代码本来就在 `finance` 里。
所以本包声明了一个瘦接口 `RunwaySource`，由 `*finance.SummaryStore` 满足；
`finance` 不 import `alerts`，没有环。

**算不出天数的一律不告警。** 「余额还没读到」是采集覆盖率的问题
（设计稿 §7 的覆盖率边界——两个真实驱动的余额读取都还没接通，**当前是常态**），
不是「快见底了」。把它报成告警，每个环境一上来就是满屏红，
然后这条规则就会被静默掉——那才是真正把预警关掉的方式。
订阅型渠道同理：它没有余额这个概念（§7 末段），判据是接入方式而不是有没有数。

**一条规则两档，不是两条规则。** 严重度逐条按天数算（`Finding.Severity`
才是落库的那个，`Rule.Severity` 只是文档字段）。拆成「< 10」与「< 5」两条的话，
一个 3 天的上游会同时命中两条，于是一个条件产出两条告警、要静默两次。

**阈值与看板共用数据库快照。** `platform-api` 的
`/finance/upstreams/summary` 与 `platform-worker` 的 R5 评估，均在各自的
请求/评估轮次从 `finance.runway_threshold_config` 读取同一 `revision`；
API 会把 revision/source/updated_at 回报给前端，worker 会把 revision 与三档
写进告警 detail。两者不再在运行时读取或比较 `.env`，因此滚动发布期间也不会
因进程启动时机不同而漂移。

`XM_FINANCE_RUNWAY_WARN_DAYS` / `XM_FINANCE_RUNWAY_CRIT_DAYS` 仅是
`runway-threshold-bootstrap` 生命周期命令的一次性输入。空值可在 bootstrap
阶段使用 5/10/20 默认档；非正、非整数或不递增会让 bootstrap 失败，绝不静默
导入。数据库缺行、current/history 不配对或读取失败时，API 返回 503，worker
整轮 fail closed；不会把默认档伪装成可信结果。无 DB provider 的静态
`RuleConfig`/旧摘要处理器仍为兼容测试保留，但不是生产路径。

### 版本变化规则（R6）为什么存在

**它来自一次真实的两小时中断。** 2026-09-06，运营在 Sub2API 后台点了升级，上游
二进制从 `0.1.179` 变成 `0.2.1`。平台的连接器兼容矩阵、开票代理的运行时钉子、
运维手册里的三处版本号全都还写着旧值，而**没有任何地方告诉任何人上游动过**。
几天后按手册抬钉子时，四条采集流因为清单不符崩掉、readyz 503 了两个小时。

所以这条规则报的不是故障，是**一个要去核对的信号**：桥接契约、兼容矩阵、各处
运行时钉子都该重新过一遍。severity 因此是 warning——真正的故障（如果有）会由
别的规则以 critical 报出来。

**判据是「与历史里最近一个不同的版本比」，不是「与上一条样本比」。** 探测每轮
都写一条样本，按上一条比的话，这条告警只在变化后的那一轮里存在，评估周期错开
一次就永远看不见。往回找最近一个不同值，告警会一直在，直到有人处理它。

**去重键带新版本。** 只带指标键的话，`0.1.179 → 0.2.1` 之后再 `0.2.1 → 0.3.0`
会复用同一行，而人已经确认过前一条了——第二次变化就被静悄悄吞掉。

**读不出版本不告警。** 一条没有 `version` 字段的观测（探测失败、连接器还没实现
`Version`、上游根本不报版本）不是「版本变了」。把缺失当成变化会在每次探测失败
时都响一遍，而那时候 R1 已经以 critical 说清「你现在是瞎的」——与「`token_valid`
缺失时不告警」同一条纪律。

**判据看的是观测里有没有 `version`，不是指标键叫什么。** 将来多一个连接器探测，
它自动被这条规则覆盖，不必回来改代码。


### 几个刻意的取舍

**去重键必须带 `<environment>`。** 库层那条部分唯一索引只建在 `dedup_key`
一列上，不带环境的话 staging 与生产的同一条指标会抢同一行——一个环境的
评估轮次会改写另一个环境的告警（宪法 15 条）。

**`R1-failed` 的迟滞：连续 N 轮才开，连续 N 轮才关（N=3，`rules.go` 的
`DefaultSyncFailedHysteresisRounds` 是它唯一的定义处）。**

在 2026-09-08 之前，这条规则**没有任何持续时间门槛**：一失败下一分钟就报，
一成功下一分钟就撤。生产上那三条 NewAPI 指标因此每 5 分钟翻一次面，而界面上
「已持续 7 分钟」每次恢复都归零，于是没人看得出它其实已经这样很久了
（见 `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md` §二）。

开与关用**同一个 N**：不对称的迟滞会让人在事后无法用一个数解释这条告警的
行为（「为什么 15 分钟才报、5 分钟就撤」）。

**代价必须知道**：采集周期 300s 时，N=3 意味着**一次 10 分钟以内的上游读取
失败从此不再产生告警**。这是有意的取舍——那段降级仍然看得见（运行保障页的
新鲜度、后台任务页的运行记录都逐轮可见），数据真的停更时 `metric.data.stale`
会接手。每一轮被迟滞压住或保持的条数都进 `alert_evaluate` 的结构化日志
（`hysteresis_suppressed` / `hysteresis_held`）——一个不留痕的抑制器就是
下一个「安静地给你一个旧答案」。

判据是**纯函数**（`foldSyncFailure`），从历史样本折叠出来，不落任何新状态：
worker 重启、多副本、River 换一个节点跑，答案都一样。

**`R1-failed` 与 `R4-consecutive` 会同时活跃。** 一条链路长期失败时两条告警
都在。它们不是重复：前者说「现在坏了，而且不是一次抖动」，后者说「这一小时
里四分之三的采集都是失败的」——同一事实的两个不同时间尺度，而后者正是决定
要不要升级处置的信号。Foundation-A **不做抑制**：抑制的正确做法是把低阶告警
标记成「被抑制」而不是「已恢复」，而 `SILENCED` 这个状态在本档已经被静默窗口
占用了。补法留给 Foundation-B。

**R4 的计数是滑动窗口，不是连续串（K=9 / W=12）。** 旧实现从样本尾部往前数，
遇到第一条成功就停——于是「多数轮失败、偶尔成功一轮」这种形态**从来没
升级过**。2026-09-08 生产上那三条 NewAPI 指标（`newapi.channels.status` /
`newapi.users.total` / `newapi.recharge.daily`）正是这样每 5 分钟翻一次面的。
K 取 9 而不是照搬旧的 3 是有理由的：一串完全交替的失败/成功在 12 条窗口里
恰好凑出 6 次失败，阈值取 3 或 6 的话，R1 的迟滞刚压住的那种翻面抖动会原样从
R4 冒出来，而且同样是 critical。

> ⚠️ **不要拿 `card_sync` 当这条规则的例子**（本片的第一版文档、`rules.go`
> 注释与提交消息 `c9d4d9a` 都错在这里）。R4 读的是
> `ops.metric_observation_sample`，而 card_sync 根本不产出指标观测——`ops` 的
> 已注册指标白名单里没有任何 `card_*` 键（`internal/platform/ops/freshness.go`）。
> 它的失败只存在于 `river_job`，而 `river_job` 不进告警引擎。card_sync 那 288
> 条的合并展示在 `/ops/overview` 的 `failed_jobs_by_kind`，不在这条规则里。

**R4 的开与关是两条不同的判据。** 开还要求「当前正在失败」，关**只看窗口
计数**：已经开着的这条告警，在最新一轮采集成功、但窗口里还有 9 次失败时
**继续挂着**（每轮进 `alert_evaluate` 的 `chronic_held` 计数）。

这一条是审稿改出来的。在它之前，R4 只在当前失败时产出命中，而 Reconciler 对
本轮没再命中的活跃告警一律 `Resolve`——于是**任何一轮采集成功都会把 R4 关掉**，
窗口里还有 9 条失败也照关。声明里那句「一次成功不再清零计数」因此是假的
（这份文档与 `docs/modules/notify/CATALOG.md` 各抄了一份给运营看），而且行为上
`FFFSFFFSFFFS` 这种劣化形态会让 R4 每隔几轮 open→resolved→reopened 一次，
每次都是新行、重新投递、critical——R1 的迟滞刚压住的抖动，换个 `rule_key` 从
R4 原样冒出来。

**代价（实测）：一次彻底的硬故障，R4 的 critical 升级从 15 分钟（旧的「连续
3 轮」）推迟到 45 分钟（窗口 12 里凑够 9 次失败）。** 采集周期 300s。这不是
「从零开始等 45 分钟」：R1 仍然在第 3 轮（15 分钟）给出 critical，R4 回答的是
另一个问题（「这一小时里四分之三的采集是失败的」）。若这个代价不可接受，
备选是 K=6 / W=8（30 分钟）——纯交替在 8 条窗口里只凑得出 4 次，仍压得住抖动。

**`metric.sync.consecutive_failed` 这个键名保留不改**，尽管判据已经不是
「连续」。rule_key 是静默窗口的匹配键与历史告警的分组键，改名等于让已存在的
静默窗口和历史一起失配——名字略微名不副实是保留键名必须付的代价。

**`R1-failed` 与 `R1-stale` 互斥，不会同时响。** `ops.Observation.Freshness`
的状态优先级里 `failed` 高于 `stale`，一条记录只会落在其中一个状态上。

**`token_valid` 缺失时不告警。** 「上游没给这个字段」与「token 失效了」是两回事。
把前者当后者会在契约变更那天造成全渠道误报，而真正的问题（Connector 契约
缺字段）该由契约测试拦，不该由告警冒充。同理，余额字段缺失时不告警。

**同步失败时不评估渠道规则。** `value_json` 里留的是上一次成功的旧值
（见 `jobs.Sub2APISyncWorker.failureObservation`），拿它去判「现在余额够不够」
是在用过期数据下现在的结论。这两种情况 `R1-failed` 已经以 critical 报出
「你现在是瞎的」，那才是此刻真正需要处理的事。

**历史样本一轮一条指标只查一次，而且只在两种情况下查**：当前正在失败
（可能要开），或者这条 R1 告警还开着（可能要关）。健康且没有告警的指标一条
样本查询都不发——评估每 60 秒一轮，无条件给每条指标配一次历史查询是纯浪费。
R1 的迟滞与 R4 的滑动窗口共用那一次查询的结果。

**「这个上游版本我核对过了」是 R6 唯一正常的结束方式。**

在它之前，`upstream.version.changed` 只能等旧探测样本被挤出回看窗口
（200 条 ≈ 16h40m）后自己消失。2026-09-08 那条的真实结局是「今晚 20:52 前后
自己消失，不是因为有人核对了，是因为证据过期了」。

现在有了 `alerts.upstream_version.acknowledge`（L1）：它把
`(environment, metric_key) → version` 记进 `alerts.upstream_version_ack`，
规则命中时若观测版本等于已核对版本就不再命中，既有 OPEN 告警在下一轮被
**通用恢复逻辑**转 `RESOLVED`（不长第二套语义）。

比较是**逐字相等**：核对过 `0.2.2` 不等于核对过 `0.2.3`，核对过 `0.2` 更不等于
核对过 `0.2.3`。一条上游只有一个**当前**已核对版本，新的核对覆盖旧的，
历史留在审计链里。

---

## 生命周期与状态

五个状态逐字来自规格 §9.3。

```text
                    条件命中
                       │
        ┌──────────────┴──────────────┐
        │                             │
   命中静默窗口                   未命中静默
        │                             │
        ▼                             ▼
    SILENCED ──── 窗口过期 ────►    OPEN ────► ACKNOWLEDGED
        │         条件仍成立          │  人工确认      │
        │                            │               │
        └────────────┬───────────────┴───────────────┘
                     │ 条件不再成立（自动）
                     ▼
                  RESOLVED ──── 24h 内复发 ────► REOPENED
```

- **活跃 = `OPEN` / `ACKNOWLEDGED` / `SILENCED` / `REOPENED`**。这个四元组在
  三处必须逐字一致：`000007` 迁移的部分唯一索引谓词、`db/queries/alerts.sql`
  里每条查询的 `IN` 列表、`alerts.activeStatuses`。任何一处漏改，去重就会失效
  （同一个问题每 60 秒新开一条）。`TestStatusIsActiveMatchesQueryPredicate`
  守住 Go 那一侧。
- **`RESOLVED` 留在唯一索引外**是有意的：同一个问题反复发生、反复恢复是常态，
  历史上的每一次都要作为独立的一行留下来——那正是「这周炸了七次」的证据。
- **`REOPENED`** 表示「24 小时内解决过又回来了」。窗口是必要的：没有窗口的话，
  三个月前发生过一次的问题今天再来仍会被标成「复发」，那个标签就不再携带信息。
- **`ACKNOWLEDGED` 持续命中仍是 `ACKNOWLEDGED`**，且不再投递。
  「有人接手了」这个事实不该被下一轮评估抹掉。

### 自动恢复

「本轮 findings 里没有这个 `dedup_key`」= 恢复条件已满足。这是**全部规则共用**
的恢复实现：每条 Rule 的 `Recovery` 字段描述的都是「触发条件不再成立」的
具体形态，而在实现上它们统一表现为同一件事。规则各写各的恢复判定会立刻长出
第二套语义，而且必然与触发判定漂移。

---

## 静默窗口

`alerts.silence.create@1`（L1）建窗口。三个必填参数：`rule_key`、
`duration_minutes`、`reason`。

| 语义 | 说明 |
|---|---|
| `rule_key` 为空串 | **全局静默**：该环境下所有规则都不投递。这是一个明确的选择，不是漏填——所以字段是必填的，只是允许空值 |
| `rule_key` 非空 | 必须是已注册的规则键。拼错的键会静默**零条**告警而创建者以为已经静默了，所以后端当场拒绝并列出可选值 |
| 窗口区间 | 左闭右开 `[starts_at, ends_at)`。`starts_at` 恒为服务端此刻，调用方只传时长 |
| 时长上限 | **7 天**。更长等于永久关掉这条规则，而且没有任何东西会提醒有人去解除它。更长的诉求应走「改规则」或「停用采集」（宪法 26 条的 Kill Switch），那两条路都有变更记录 |
| `reason` | 非空是**库层 CHECK**。没有理由的静默在事后复盘时与「有人手滑」不可区分。它同时进审计事件的 `reason` 列 |
| 对已响告警的作用 | 已经 `OPEN` 的告警命中新建的窗口会转 `SILENCED` —— 让正在响的告警闭嘴正是建窗口的目的 |
| 静默期间 | `fire_count` **照常递增**：那是「这个问题持续了多久」的证据（**评估轮数**，不是触发次数），不该因为没人想听就不记。`trigger_count` 在 `OPEN → SILENCED` 这一步**不**递增——让它闭嘴不是「又响了一次」 |

**窗口过期后**：条件仍成立 → 转回 `OPEN`，`notify_status` 推回 `pending`
重新排队投递。这条转换是 `Store.Upsert` 里唯一会重置投递状态的路径；
平时（`OPEN` 持续命中）绝不重置，否则每 60 秒重发一次同样的消息。

**没有「取消」**：平台不提供撤销静默的能力，窗口只会自己到期。按早了就只能
等——这是取舍，不是漏做：一个能被随手撤销的静默窗口，其审计价值远低于
一个必须等它走完的窗口。

### 列出窗口（`GET /api/v1/alerts/silences`，XM-SILENCE-LIST）

静默此前**只能建不能看**。看不见的静默窗口比看得见的危险得多——运营按得下
去，却回答不了「现在有哪些静默生效中、是谁按的、什么时候到期」。

| 参数 | 说明 |
|---|---|
| `environment` | 不传用调用者自己的；传了必须一致（规格 §20.5），否则 403 |
| `state` | `active`（默认，只给此刻生效的）或 `all`（含未开始与已过期）。其余取值当场 400 |
| `limit` | 正整数，上界由 `alerts.MaxListLimit` 钳制。非正整数当场 400 |

响应是 `{items, limit, truncated, as_of}`。每条带一个服务端算出的
`state`：`active` / `scheduled` / `expired`——判据是
`alerts.Silence.Active`，与投递侧压制告警用的是**同一段代码**，前端不自行
比对时间。`as_of` 是判定所用的时刻。

`state=active` 走 `ListActiveSilences`（SQL 带时间条件、无 LIMIT，limit 由
HTTP 层兑现）；`state=all` 走 `ListSilences`（按 `starts_at` 倒序，SQL 带
LIMIT）。**不是「全取回来再在 Go 里筛」**：那样会先被 limit 截断，一屏历史
窗口就能把生效中的挤掉，页面于是显示「当前没有静默」。管理端出于同样的理由
分两次请求，「生效中」的计数不跟历史列表共用一份数据。

---

## 投递（规格 §9.4）

```text
平台告警中心 → Telegram Bot API / 自建 Webhook / 企业微信群机器人
```

零新增依赖：三个渠道都是一个 POST JSON 的 HTTP 端点，标准库够用。
引一个 SDK 只为发一条消息，换来的是一整棵传递依赖树和一条需要跟着升级的
供应链（宪法 25 条）。

### 环境变量

| 变量 | 说明 |
|---|---|
| `XM_ALERT_EVALUATE_ENABLED` | 评估任务开关，默认 `true`（宪法 26 条的停用开关） |
| `XM_ALERT_EVALUATE_INTERVAL` | 评估周期，默认 `60s`。必须快于采集周期 |
| `XM_ALERT_TELEGRAM_BOT_REF` | Bot Token 的 CredentialRef，形如 `secret://alerts/telegram-bot` |
| `XM_ALERT_TELEGRAM_CHAT_ID` | 目标会话。不是秘密，但也不进日志 |
| `XM_ALERT_TELEGRAM_TOKEN` | 上面那个引用在 env Provider 下的落点（明文，**只填在不入库的 `.env` 里**） |
| `XM_ALERT_WEBHOOK_URL` | 自建端点，**必须 https** |
| `XM_ALERT_WECOM_WEBHOOK_REF` | 企业微信群机器人 Webhook 地址（含 key）的 CredentialRef，形如 `secret://alerts/wecom-webhook`（XM-ALERT-WECOM）。与上面两个不同：**整个地址就是凭据**，走文件优先的 SecretProvider 链（XM-CRED0），可在后台「设置→凭据」页直接粘贴 |
| `XM_ALERT_WECOM_WEBHOOK` | 上面那个引用在 env Provider 下的落点（明文，**只填在不入库的 `.env` 里**，仅在还没在后台填过时兜底） |
| `XM_ALERT_BALANCE_THRESHOLD_MINOR_UNITS` | 渠道余额阈值，最小货币单位的整数，默认 `500000` |

### 配错就拒绝启动，没配则只是警告

这条区分是 `jobs.newAlertNotifier` 的全部要点，也与 XM-0022 的 sub2api 配置
**刻意相反**：

- sub2api 配错时每轮都会往看板写一条说得清原因的 `SyncFailed` 观测，
  故障是**可见**的，所以让 worker 照常启动是对的；
- 告警链路没有这个性质。一个因为 URL 拼错而没建起来的渠道，唯一的痕迹是
  启动时一行日志，之后每一条告警都会静静地不投递，而运维会以为自己配好了。
  等到真出事那天才发现没人被通知——那正是这个模块存在的意义被完全抵消的时刻。

所以：**ref 拼错 / URL 不是 https / 只配了一半 / 配了企微 ref 却没有对应
SecretProvider → 进程起不来。**三个渠道的变量全部留空 → 告警照常评估落库，
每轮打一条 `alert_notify_skipped` warn（「仅落库未投递」），`notify_status`
停在 `pending`。那是真话，不粉饰成「排队中」。企微渠道的地址是否真的解析
得出合法凭据要等发送那一刻才知道（同 Telegram 的 Bot Token）——启动时只
校验引用形状与装配完整性，ref 配了但后台还没粘贴凭据不会让进程起不来，
会在每条告警的 `notify_error` 上如实显示解析失败。

### 投递语义

- **至少一个渠道收下就算 delivered。** 两个渠道配着、Telegram 通了、Webhook
  挂了，如果整体判失败，下一轮会重试，于是 Telegram 每 60 秒收到一条一模一样的
  消息——为了如实记录一个次要渠道的故障，把主渠道变成了垃圾消息源，
  而收件人最终会静音它。代价是部分失败不进 `notify_error`（库层 CHECK 要求
  `delivered` 不带错误），补偿是每个失败渠道单独打一条 warn 日志。
- **失败自动重试**：`notify_status=failed` 的告警仍在待投递队列里，下一轮再试。
- **单轮投递上限 50 条**：一次上游全挂能一口气产出上百条告警，无界地逐条发
  HTTP 会把一轮评估拖过 River 的 JobTimeout，结果是整轮回滚重来，投递永远
  追不上。有上限则每轮稳定投出一批，队列会排空。
- **只投递 `OPEN` / `REOPENED`**：`ACKNOWLEDGED` 表示有人接手了，再吵是噪声；
  `SILENCED` 投递就是在违背静默本身。

### 凭据不泄漏

Bot Token 长在 **URL 路径**里（`/bot<token>/sendMessage`），而 `net/http` 造的
错误默认带 URL：

```text
Post "https://api.telegram.org/bot123456:AAH.../sendMessage": dial tcp ...
```

那个错误不经过我们的手就已经带着 token 了。任何一处 `return err` 都会把它送进
`notify_error`（会落库、会回前端）或日志。所以 `TelegramNotifier` 的每一条
错误路径都显式脱敏，`TestTelegramNotifierNeverLeaksToken` 逐条穷举
（含「上游把请求 URL 回显进错误描述」和「连不上」两种最刁钻的路径）。

Webhook 那边更严：**整个 URL 可能就是凭据**（Slack / 飞书的 incoming webhook
地址里带 token），所以那边连脱敏都不做——直接不把原始错误往外冒，只报分类
与状态码。

企业微信群机器人（XM-ALERT-WECOM）与 Webhook 同一个前提——**整个地址就是
凭据**（鉴权 key 直接嵌在查询参数里）——但走的是 Telegram 那条路线：地址
经 CredentialRef 解析、由 SecretProvider 在发送那一瞬现场给出，因此
`WeComNotifier` 手上有确切的敏感字符串，可以像 Telegram 那样显式 `redact`，
不必像 Webhook 那样完全放弃细节。脱敏对象不止「完整地址整串出现」这一种：
一个把请求路径回显进错误页的网关只会带出路径 + 查询串，不含 scheme/host，
这种情况下按整串匹配会漏判，所以查询串本身也单独作为一个敏感片段。
`TestWeComNotifierNeverLeaksWebhookURL` 覆盖这两种匹配方式，外加「上游返回
非 0 errcode」「响应不是合法 JSON」「连不上」等路径。

---

## 权限

| 能力 | Scope | 风险等级 |
|---|---|---|
| 读告警（`GET /api/v1/alerts`） | `ops.read` | — |
| 读静默窗口（`GET /api/v1/alerts/silences`） | `ops.read` | — |
| 确认告警（`alerts.alert.acknowledge@1`） | `alerts.alert.manage` | L0 |
| 创建静默窗口（`alerts.silence.create@1`） | `alerts.silence.manage` | L1 |
| 核对上游版本（`alerts.upstream_version.acknowledge@1`） | `alerts.alert.manage` | L1（永久锁定） |
| 撤销已核对版本（`alerts.upstream_version.revoke@1`） | `alerts.alert.manage` | L1（永久锁定） |

**读复用 `ops.read`**：告警内容就是运营指标的判读结果——「渠道甲余额只剩
3000」这条告警泄漏的信息，与 `ops.read` 能直接读到的余额数字完全相同。
多一个 scope 只是多一个要维护、要授予、会漏授的授权面。

**确认与静默分两个 scope**：确认只是说「我看见了」，告警仍留在列表里；
静默是让告警不再出现也不再投递，一个足够宽的窗口等于临时关掉整套告警。
两件事的爆炸半径差一个量级。

**确认是 L0 而不是 L1**：ADR-003 把「确认普通告警」列在 L1 的例子里，
但那一行的完整表述是「修改低风险平台配置、确认普通告警」——L1 描述的是
**改配置**那一类。确认不改变任何配置、不影响任何运行中的系统，且完全可逆。
静默才是真正会改变系统行为的那个，它是 L1。

**跨环境闸门在 Handler 里**：内核只校验「这个 Action 允许在你的环境执行」，
它不认识资源。一个 staging 身份完全可能拿着生产告警的 UUID 打过来，
所以 `acknowledgeHandler` 在读到资源之后显式比对 `alert.Environment`
与 `Principal.Environment`（宪法 15 条）。

**核对与撤销上游版本复用 `alerts.alert.manage`，且必须永久留在 L1。**

复用而不新增 scope：核对上游版本与确认告警是同一类「我看过了」，爆炸半径远
小于静默——它只让**这一条**上游的版本提醒停下来，不会让任何别的告警闭嘴。

留在 L1 的理由不是「感觉不严重」，是硬的：L2 及以上会把整包 `params` 原样冻进
`core.approval_request.params_json`，并由 `GET /api/v1/approvals` 回给每一个能
看审批队列的人。而这两个 Action 各有一个**自由文本参数**——`acknowledge` 的
`note`、`revoke` 的 `reason`，都是 ≤200 字节、除长度外没有任何形态校验，
**形状上装得下凭据**。所以抬级会给它们开一条展示通道：这两个 Action 因此
**必须**锁在 L1，在 `action.Schema` 支持形状校验（Pattern / Redacted 标记）
之前不得抬级。`TestUpstreamVersionActionsArePinnedToL1BecauseFreeTextParamsExist`
把这条钉住。

> ⚠️ 这段话的第一版写的是「本 Action 的**两个**参数在形状上装不下凭据」
> ——它把 `note` 数漏了，读起来像「所有参数都被约束过，抬级也安全」。
> 一条把自己的理由说错了的注释，比没有注释更容易被拿去做相反的决定。

另外两个参数确实收得住，但收法与第一版写的也不一样：`metric_key` 的判据是
**「这个环境下这条指标此刻确实观测到了一个上游版本」**，不是 `ops.KnownMetricKey`
那份手列白名单。改的理由见下一段；`version` 仍然必须与平台自己观测到的
`value_json.version` **逐字相同**才会落库——「调用方给什么就存什么」这条路
根本不存在。`source` 由服务端从观测里读出后写入，不来自参数。

**结束一条告警的范围，必须与产生它的范围同源。** R6 的命中范围是**发现出来
的**（判据是这条观测里有没有 `version`，不是它的键叫什么，将来多一个连接器
探测就自动被覆盖），而 `ops.KnownMetricKey` 读的是一份手列的白名单
（`ops/freshness.go` 的 `registeredMetrics`）；落库侧只校验 `ValidMetricKey`，
所以一条未注册的指标观测完全可以存在。两个范围一旦漂开就会出现**「告警响得
起来、但按钮点不动」**：R6 对某条带 `version` 的未注册指标命中，Action 直接
拒绝，这条告警又回到本片要消灭的状态——只能等旧样本被挤出窗口后自己消失。
今天不出事只是因为白名单**恰好**覆盖了现有连接器。已注册清单退到**提示文案**
里：拼错照样能拿到有用的报错，但它不再是准入闸。

它也确实不该是 L0：L0 的定位是「保存个人视图、低影响偏好」，而这个动作会让
一条规则不再命中、让既有告警在下一轮被解决，与 `alerts.silence.create` 同一档。

**「已核对」必须看得见、也必须撤得掉。** 一条建立起来就撤不掉的抑制器是
任务书致命清单里点名的东西：点错一次，那条「去核对桥接契约与兼容矩阵」的
提醒对该版本**永久**消失，而唯一的自动解除条件是上游再升一次版本——那是
外部事件，不在操作者手里；`acknowledge` 又要求 `version` 与当轮观测逐字相同，
连「用另一个值覆盖掉」这条路都走不通。所以本模块同时提供：

- 读：`GET /api/v1/alerts/upstream-versions`（`ops.read`）——列出这个环境下
  全部已核对记录：哪条上游、哪个版本、谁标的、什么时候标的，并在响应里带上
  撤销用的 Action ID。**`note` 不在响应里**：上面那条「自由文本形状上装得下
  凭据、所以不能进展示通道」的理由，对这个端点与对审批队列是同一条——本端点
  只要 `ops.read`（`staff` 就有），而审计事件的读路径单独要 `audit.read`
  （「审计事件带前后摘要，敏感度高于 `ops.read`」）。要看当时写了什么备注，
  走 `GET /api/v1/audit/events`，那是它本来就该在的那一档；
  `TestListUpstreamVersionAcksDoesNotEchoTheNote` 把这条缺席钉住；
- 写：`alerts.upstream_version.revoke@1`（L1，`reason` 必填）——按
  `(environment, metric_key)` 撤销，**不带 `version` 参数**（让调用方再报一次
  版本号只会多出「上游已经又升级了所以你撤不掉」这种失败形态，而那恰恰是
  最需要撤销的时刻）。下一轮评估这条提醒就回来了。

---

## 数据模型

`db/migrations/000007_alerts.up.sql` 建了 `alerts.alert` 与
`alerts.alert_silence`；`db/migrations/000054_alerts_trigger_and_version_ack.up.sql`
给前者加了两列，并新建 `alerts.upstream_version_ack`。几处值得单独说的：

- **`fire_count` 与 `trigger_count` 是两个量。** 前者是**评估轮数**（每 60 秒
  一轮，条件仍成立就 +1），后者是真正的**触发次数**（新开 +1、静默过期转回
  `OPEN` 重新投递 +1；持续命中不加）。2026-09-08 界面上那个「触发 669 次」
  其实是「持续了 668 分钟」——一个数被当成了另一个数在用。
- **`first_opened_at` 与 `trigger_count` 跨 `RESOLVED → REOPENED` 一起继承**
  （限 24 小时复发窗口内，与 `reopenLookback` 同一个窗口，不新增第二个
  「多久算同一件事」的常量）：前者取上一条的**有效**首开时刻，后者取上一条的
  值 +1。所以「已持续」不会因为中间恢复过一次就归零，「触发几次」也不会。
  超过复发窗口的是一件新事，两个都重新起算。

  **两个字段必须同进同退。** 前端被告知要把它们并排渲染成「已持续 X，
  触发 N 次」；只继承时刻的话，一条开→关→开四轮的告警会显示成「已持续
  4 小时，触发 1 次」——两个数各自都对，合成出来的那句话是假的。它与被它
  替换掉的「触发 669 次」是同一类误读，只是方向相反：那个多报，这个少报。
  例外只有一种，而它对两个字段**同时**成立：继承链的上一环本身是本列上线前的
  旧行（两列皆 `NULL`）时，这一行的两列也都留 `NULL`——次数在界面上是「—」，
  时刻由读取侧兜底并标成「（估计值）」。理由逐字相同：「不知道」是诚实的，
  从 1 重新起算不是；同样地，上一环那个「有效首开时刻」本身就是用 `opened_at`
  兜出来的估计值，把它写进这一行的 `first_opened_at` 就等于把估计值洗成确定值
  ——库里从此分不出「记下来过」与「兜的底」，`first_opened_at_estimated`
  恒为 `false`，那个后缀再也不会出现。

  **代价明说**：留 `NULL` 之后「已持续」从**本行**的 `opened_at` 起算，比真实
  时长短掉中间那一段复发间隔。库里只有「确定值」与「不知道」两档，没有第三档
  能存住「这是估计值但它更早」。少报一段并明说是估计，好过报一个更准的数却
  谎称它确定（宪法 12 条）。要两全得给这一列配一个 `estimated` 标记列。
- **两列都可空、都不回填。** 本列上线前就存在的行不知道自己被触发过几次，
  也不知道第一次是什么时候开的。填 0 或 `now()` 会造出一个看起来像真答案的
  假答案（宪法 12 条）。读取侧的兜底规则只写在 `Alert.EffectiveFirstOpenedAt`
  一处，HTTP 层调它，不各写一遍。
- **`alerts.upstream_version_ack` 的主键是 `(environment, metric_key)`**，不含
  `version`：一条上游只有一个**当前**已核对版本，新的核对覆盖旧的，历史留在
  审计链里。不复用 `alert_silence`（它是限时窗口、按 `rule_key` 匹配、只挡投递
  不挡命中——用它实现等于把一件做完的事做成一个 7 天后会复发的提醒），也不
  挂在 `alerts.alert` 上（那会随保留期清理被删掉，而那个失效没有任何人做错
  任何事、也不留痕迹）。

- **部分唯一索引覆盖四个活跃状态**（含 `SILENCED`），而不只是
  `OPEN/ACKNOWLEDGED/REOPENED`。这是相对任务书原始描述的一处**有意收紧**：
  `SILENCED` 是活着的状态，窗口过期后要按 `dedup_key` 找回它并转回 `OPEN`。
  如果它不在唯一索引里，同一个被静默的问题会每 60 秒新插一行——一天 1440 条
  僵尸告警，而且窗口一过会同时炸出来。
- **一致性 CHECK 让静默失败在库层不可表示**：`failed` 必须有 `notify_error`，
  `delivered` 必须有 `notified_at`，`RESOLVED` 必须有 `resolved_at`。
  与 `ops.metric_observation` 同一条思路。
- **`reason` 非空是库层约束**，不只是代码层。代码路径会长出第二条，表约束不会。

---

## Runway 阈值与影响预览（XM-C-RUNWAY0）

R5 的可用天数分类现在由 `finance.RunwayThresholds.Classify` 统一提供：
`days <= critical` 为 critical，`days <= warning` 为 warning，
`days <= serious` 仅为展示关注色，超过 serious 为 healthy。`serious` 不会
创建或投递 R5 通知；订阅型和余额未知对象也不会被编成告警。

规则页 `/alerts?sub=rules` 读取 `finance.runway_threshold_config` 的 revision，
并可对 proposed 三档做纯只读影响预览。预览把当前活跃 R5 与当前分类先做一致性
检查（缺失、意外、严重度不匹配、重复），再报告将打开/升级/降级/恢复的对象；
`evaluation_at` 与每个余额自己的 `observed_at` 分开显示。页面在 Foundation-B /
C3c 之前不读取写权限、不展示提交按钮，也不发送 Action。

worker 若注入 `RunwayThresholdProvider`，每轮评估只读取一次快照，并把
`threshold_revision` 与三档整数写入 R5 detail，便于和 finance history 对账。
配置缺行或读取失败时整轮 fail closed，不能用空结果把既有告警恢复掉。

## Foundation-A 边界

规格 §9.3 列了 12 项生命周期能力。本档实现了其中 7 项：
去重、静默、确认、解决、重新打开、通知投递状态、失败重试。

**没做的 5 项，全部留给 Foundation-B：**

| 能力 | 为什么本档不做 |
|---|---|
| **分组** | 需要先有一套「什么算同一组」的语义（按规则？按资源？按时间窗？），而那套语义只有在真实告警量跑起来之后才判断得出。现在定等于拍脑袋 |
| **抑制** | 正确做法是把低阶告警标记成「被抑制」而不是「已恢复」，需要第六个状态；`SILENCED` 已被静默窗口占用。见上文 R1/R4 那一段 |
| **升级** | 升级要回答「多久没人处理算超时、升给谁」，那是排班与 on-call 的问题，平台还没有排班模型 |
| **关联 Incident** | §2.3 定义 `Incident` = 多告警聚合后的事件。没有分组就没有聚合，先做分组 |
| **关联 Runbook** | Runbook 目录（`docs/runbooks/`）已有，但「哪条规则对应哪个 Runbook」需要规则入库才好维护。等规则从代码搬进库 |

**另外没做的：**

- **按严重度路由渠道**（critical 走 Telegram、warning 只走 Webhook）：
  本档全部规则共用同一组已配置渠道；
- **告警静默的解除**：窗口到期自动失效，没有「提前解除」的 Action。
  需要提前解除时，建一个覆盖同一规则的、更短的新窗口不管用（旧窗口仍生效）——
  这是一个已知缺口，Foundation-B 补 `alerts.silence.cancel`；
- **告警保留期清理**：`alerts.alert` 会随时间单调增长。按 60s 评估、
  少量规则估算，一年也只有千级行，本档不做清理任务，但它在路线图上
  （与 `ops.metric_observation_sample` 的清理一起做）。

---

## 相关文件

| 路径 | 内容 |
|---|---|
| `internal/platform/alerts/rules.go` | 规则声明与评估器 |
| `internal/platform/alerts/reconcile.go` | 评估 → 落库 → 自动恢复 → 投递的编排 |
| `internal/platform/alerts/notify.go` | Telegram / Webhook / 企业微信 / 扇出 |
| `internal/platform/alerts/store.go` | 去重 Upsert 与状态转换 |
| `internal/platform/alerts/actions.go` | 四个 Action 的声明与 Handler |
| `internal/platform/jobs/alert_evaluate.go` | River 周期任务与渠道装配 |
| `internal/platform/httpapi/alerts.go` | `GET /api/v1/alerts` |
| `internal/platform/httpapi/alerts_upstream_versions.go` | `GET /api/v1/alerts/upstream-versions`（已核对版本清单） |
| `db/migrations/000007_alerts.up.sql` | 表结构与索引 |
| `web/apps/admin-web/src/pages/AlertsPage.tsx` | 告警中心页 |
| `deploy/watchdog/README.md` | 外部看门狗（异故障域，独立通知链） |
