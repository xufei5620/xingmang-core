# XM-NOTIFY-ENVELOPE：一个群、一眼看懂——企业微信通知的分类、严重度与模板

> 状态：设计冻结（产品负责人 2026-09-06 提出："目前添加的凭据所有的通知都是一个 Webhook 地址，
> 所以需要对通知建立规范，做好分类，通知模板，让我们清楚在企业微信那边收到每条消息代表什么"）。

## 0. 现状与问题

平台今天有**三条互相独立**的企业微信群机器人通道，各有各的凭据引用、各写各的消息格式：

| 通道 | 凭据引用 | 触发者 | 现有消息开头 |
|---|---|---|---|
| 告警 | `secret://alerts/wecom-webhook` | `platform-worker` 的告警评估轮次 | `> [CRITICAL] 某某标题` |
| 卡片事件 | `secret://cards/notify-webhook` | `platform-api` 的 Infini 回调 | `**卡片验证码** acct · 1234` |
| 接码验证码 | `secret://sms/notify-webhook` | `platform-api` 的取码调用 | `**接码验证码**` |

三条通道**刻意不合并**（各自的领域字段完全不同，合并会让任一方加字段都要改另一方），这条决定
不变。问题不在通道，在**消息本身不自我说明**：

- 三种开头三种风格，收件人要靠记忆分辨这条是什么；
- 没有环境（staging 的告警和生产的告警长得一模一样）；
- 没有稳定编号，出事后无法在群里搜"上次那条是什么"；
- 没有严重度分级（卡片消费和卡片被锁一样重）；
- 没有"该去哪处理"，看到了也不知道下一步点哪。

产品负责人把同一个 Webhook 地址填进了三个凭据（这是合理用法），于是三种消息落在同一个群里，
上述四点缺失全部放大。

## 1. 目标与非目标

**目标**：一个新的共用包 `internal/platform/notify`，定义**信封**——每条发往企业微信的消息都由
它渲染出统一的头部与尾部，正文仍由各域自己写。收件人在群里看到任意一条消息，一眼能答出：
这是哪个域、多严重、哪个环境、编号是什么、该去哪处理。

**非目标**：
- 不合并三条通道，不共用凭据，不改任一域的字段；
- 不改投递状态机（告警的 pending/failed 重试、卡片与接码的"丢一条推送可接受"）；
- 不引入新的外部依赖，不改 Webhook 请求形状（仍是 `msgtype=markdown`）。

## 2. 信封

```go
type Envelope struct {
    Domain      Domain   // 告警 / 卡片 / 接码 / 开票
    Kind        string   // 域内稳定事件键，进编号；例 "metric.sync.failed"、"transaction"
    Severity    Severity // info / warning / critical
    Environment string   // production / staging / ...
    Title       string   // 一行话，可能含上游文本
    Lines       []Line   // 正文，label: value
    Action      string   // 该去哪处理（我方文案，闭集）
}
```

渲染成：

```
【星芒·告警】<font color="warning">警告</font> · production
> 标题：Sub2API 渠道余额不足
> 环境：production
> 规则：channel.balance.low
> 详情：渠道 A 余额 ¥312.00 低于阈值 ¥5000.00
> 编号：XM-ALERT-channel.balance.low
> 处理：管理后台 → 告警与故障
```

**三条排版纪律**（沿用两处现有实现里已经写死的理由，不新发明）：

1. **只有我方闭集用 markdown 语法**。域名、严重度、环境、编号、处理入口全部来自封闭枚举，
   给它们上色/加粗是安全的。标题、详情、商户名、指标键这些**上游决定的字符串**一律进
   `> ` 引用块、不带任何标记——带标记就要转义，漏转义的后果是消息排版错乱甚至发不出去。
2. **长度按字节截断**（企微 `content` 上限 4096 **字节**，不是字符）。截断只发生在正文，
   头部与编号行永远保留——一条被截断到看不出是什么的消息等于没发。
3. **凭据绝不进消息**，也绝不进错误串（Webhook 地址本身就是凭据）。

## 3. 分类与严重度

| 域 | 徽标 | 事件键（Kind） | 严重度 | 处理入口 |
|---|---|---|---|---|
| 告警 | 【星芒·告警】 | 六条规则的 `rule_key` 原样 | 规则逐条判定（`Finding.Severity`） | 管理后台 → 告警与故障 |
| 卡片 | 【星芒·卡片】 | `challenge` / `transaction` / `status_change` | 验证码=warning（有时效）；消费=info；状态变更=warning | 管理后台 → 卡片管理 |
| 接码 | 【星芒·接码】 | `code` | warning（有时效） | 管理后台 → 接码中心 |
| 开票 | 【星芒·开票】 | `request.submitted` 等 | info（提交）；warning（积压） | 管理后台 → 开票（申请审核） |

严重度的中文与配色（企微 markdown 只认三种颜色）：

| Severity | 中文 | 企微 color |
|---|---|---|
| critical | 严重 | `warning`（企微的红） |
| warning | 警告 | `warning` |
| info | 通知 | `comment`（灰） |

企微没有独立的红/橙，`critical` 与 `warning` 同色；靠中文词与徽标区分，不靠颜色承载唯一信息
（无障碍：颜色永远不是唯一区分手段）。

## 4. 编号

`XM-<域大写>-<Kind>`，例：`XM-ALERT-metric.sync.failed`、`XM-CARD-challenge`、`XM-SMS-code`、
`XM-INV-request.submitted`。它是**类型编号，不是实例编号**：同一类事件的所有消息共用一个编号，
群里搜它能捞出同类的全部历史。实例的唯一标识（告警的 dedup_key、卡片的账号+掩码）已经在正文里。

## 5. 落地范围

1. 新包 `internal/platform/notify`：`Domain`/`Severity`/`Envelope`/`Line`/`RenderWeComMarkdown`
   与按字节截断，含单测（含"上游文本里的 markdown 特殊字符原样出现在引用块里"这条）。
2. `alerts.FormatWeComMarkdown`、`cards.FormatNotification`、`sms` 的消息体改为组装 `Envelope`
   后交给渲染器。三处的既有断言按新头部更新，正文字段一个不少。
3. 三处的 Telegram / 纯文本路径不动（`alerts.FormatMessage` 仍是无标记纯文本）。

## 6. 开票申请通知（本设计的第一个新消费者，另片实现）

产品负责人同日要求："开票这边用户提交了开票应该需要有一个通知发到 Webhook 企业微信那边"。
它按本信封发 `XM-INV-request.submitted`。**取数与触发方式属另一片**（平台不能直连开票库，
开票线也不该持有平台的 Webhook 凭据），设计要点先记在这里：平台侧新增一个只读轮询
（复用 `platform-worker` 的周期任务四件套），经开票线**已存在**的管理端只读端点
`GET /api/v1/admin/invoice-requests?status=pending_review` 取新提交的申请，按 `submitted_at`
水位去重，只发编号、金额、来源平台与状态，**不发抬头、税号、银行账号、地址、电话等任何开票 PII**。
跨线读取需要一份 CR（平台线发起、开票线确认），与 CR-0009 同一条边界纪律。
