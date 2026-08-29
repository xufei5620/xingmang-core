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
4. **凭据只在发请求那一瞬存在。** Telegram Bot Token 经 SecretProvider 解析，
   绝不进日志、错误串、`notify_error` 或任何 API 响应（宪法 7 条）。

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
| `metric.sync.failed` | 指标同步失败 | `ops.metric_observation` | 新鲜度状态为 `failed` | 0（立即） | **critical** | 恢复到非 `failed` | `metric.sync.failed:<env>:<metric_key>` |
| `metric.data.stale` | 指标数据陈旧 | `ops.metric_observation` | 状态为 `stale` 且滞后 ≥ 阈值 + 2 个采集周期 | 2 个采集周期（默认 600s） | warning | `observed_at` 重回阈值内 | `metric.data.stale:<env>:<metric_key>` |
| `metric.sync.consecutive_failed` | 同步连续失败 | `ops.metric_observation_sample` | 最近 3 条样本连续为 `failed` | 3 个采集周期 | **critical** | 出现任意一条成功样本 | `metric.sync.consecutive_failed:<env>:<metric_key>` |
| `channel.token.invalid` | 渠道 token 失效 | `sub2api.channels.balance` 的 `channels[].token_valid` | `token_valid` **明确为** `false` | 0 | warning | 恢复为 `true`，或该渠道从上游清单消失 | `channel.token.invalid:<env>:<channel_id>` |
| `channel.balance.low` | 渠道余额不足 | `sub2api.channels.balance` 的 `channels[].balance_minor_units` | 余额 < 阈值（默认 500000 最小货币单位） | 0 | warning | 余额回到阈值之上，或该渠道消失 | `channel.balance.low:<env>:<channel_id>` |
| `upstream.runway.low` | 上游可用天数不足 | `finance.balance_history` ÷ `finance.profit_daily`（近 7 个完整业务日的日均消耗，设计稿 §10.4） | 计量型上游的可用天数**算得出来**且 ≤ warning 档（默认 10 天） | 0 | warning（≤ critical 档 5 天升为 **critical**） | 天数回到告警档之上，或不再算得出天数 | `upstream.runway.low:<env>:<upstream_account_id>` |

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

### 几个刻意的取舍

**去重键必须带 `<environment>`。** 库层那条部分唯一索引只建在 `dedup_key`
一列上，不带环境的话 staging 与生产的同一条指标会抢同一行——一个环境的
评估轮次会改写另一个环境的告警（宪法 15 条）。

**`R1-failed` 与 `R4-consecutive` 会同时活跃。** 一条指标连续失败 ≥3 轮时，
两条告警都在。它们不是重复：前者说「现在失败」，后者说「已经连续失败三轮，
不是一次抖动」——同一事实的两个不同持续时间断言，而后者正是决定要不要
升级处置的信号。Foundation-A **不做抑制**：抑制的正确做法是把低阶告警标记成
「被抑制」而不是「已恢复」，而 `SILENCED` 这个状态在本档已经被静默窗口占用了。
补法留给 Foundation-B。

**`R1-failed` 与 `R1-stale` 互斥，不会同时响。** `ops.Observation.Freshness`
的状态优先级里 `failed` 高于 `stale`，一条记录只会落在其中一个状态上。

**`token_valid` 缺失时不告警。** 「上游没给这个字段」与「token 失效了」是两回事。
把前者当后者会在契约变更那天造成全渠道误报，而真正的问题（Connector 契约
缺字段）该由契约测试拦，不该由告警冒充。同理，余额字段缺失时不告警。

**同步失败时不评估渠道规则。** `value_json` 里留的是上一次成功的旧值
（见 `jobs.Sub2APISyncWorker.failureObservation`），拿它去判「现在余额够不够」
是在用过期数据下现在的结论。这两种情况 `R1-failed` 已经以 critical 报出
「你现在是瞎的」，那才是此刻真正需要处理的事。

**R4 只在当前正在失败时才回看历史样本。** 语义上「连续失败」本就要求最新
那条是失败的；成本上健康时一条样本查询都不发。

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
| 静默期间 | `fire_count` **照常递增**：那是「这个问题持续了多久」的证据，不该因为没人想听就不记 |

**窗口过期后**：条件仍成立 → 转回 `OPEN`，`notify_status` 推回 `pending`
重新排队投递。这条转换是 `Store.Upsert` 里唯一会重置投递状态的路径；
平时（`OPEN` 持续命中）绝不重置，否则每 60 秒重发一次同样的消息。

---

## 投递（规格 §9.4）

```text
平台告警中心 → Telegram Bot API / 自建 Webhook
```

零新增依赖：Bot API 就是一个 POST JSON 的 HTTP 端点，标准库够用。
引一个 Telegram SDK 只为发一条消息，换来的是一整棵传递依赖树和一条需要
跟着升级的供应链（宪法 25 条）。

### 环境变量

| 变量 | 说明 |
|---|---|
| `XM_ALERT_EVALUATE_ENABLED` | 评估任务开关，默认 `true`（宪法 26 条的停用开关） |
| `XM_ALERT_EVALUATE_INTERVAL` | 评估周期，默认 `60s`。必须快于采集周期 |
| `XM_ALERT_TELEGRAM_BOT_REF` | Bot Token 的 CredentialRef，形如 `secret://alerts/telegram-bot` |
| `XM_ALERT_TELEGRAM_CHAT_ID` | 目标会话。不是秘密，但也不进日志 |
| `XM_ALERT_TELEGRAM_TOKEN` | 上面那个引用在 env Provider 下的落点（明文，**只填在不入库的 `.env` 里**） |
| `XM_ALERT_WEBHOOK_URL` | 自建端点，**必须 https** |
| `XM_ALERT_BALANCE_THRESHOLD_MINOR_UNITS` | 渠道余额阈值，最小货币单位的整数，默认 `500000` |

### 配错就拒绝启动，没配则只是警告

这条区分是 `jobs.newAlertNotifier` 的全部要点，也与 XM-0022 的 sub2api 配置
**刻意相反**：

- sub2api 配错时每轮都会往看板写一条说得清原因的 `SyncFailed` 观测，
  故障是**可见**的，所以让 worker 照常启动是对的；
- 告警链路没有这个性质。一个因为 URL 拼错而没建起来的渠道，唯一的痕迹是
  启动时一行日志，之后每一条告警都会静静地不投递，而运维会以为自己配好了。
  等到真出事那天才发现没人被通知——那正是这个模块存在的意义被完全抵消的时刻。

所以：**ref 拼错 / URL 不是 https / 只配了一半 → 进程起不来。**
三个变量一个都没配 → 告警照常评估落库，每轮打一条
`alert_notify_skipped` warn（「仅落库未投递」），`notify_status` 停在 `pending`。
那是真话，不粉饰成「排队中」。

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

---

## 权限

| 能力 | Scope | 风险等级 |
|---|---|---|
| 读告警（`GET /api/v1/alerts`） | `ops.read` | — |
| 确认告警（`alerts.alert.acknowledge@1`） | `alerts.alert.manage` | L0 |
| 创建静默窗口（`alerts.silence.create@1`） | `alerts.silence.manage` | L1 |

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

---

## 数据模型

`db/migrations/000007_alerts.up.sql`。两张表：`alerts.alert` 与
`alerts.alert_silence`。几处值得单独说的：

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
| `internal/platform/alerts/notify.go` | Telegram / Webhook / 扇出 |
| `internal/platform/alerts/store.go` | 去重 Upsert 与状态转换 |
| `internal/platform/alerts/actions.go` | 两个 Action 的声明与 Handler |
| `internal/platform/jobs/alert_evaluate.go` | River 周期任务与渠道装配 |
| `internal/platform/httpapi/alerts.go` | `GET /api/v1/alerts` |
| `db/migrations/000007_alerts.up.sql` | 表结构与索引 |
| `web/apps/admin-web/src/pages/AlertsPage.tsx` | 告警中心页 |
| `deploy/watchdog/README.md` | 外部看门狗（异故障域，独立通知链） |
