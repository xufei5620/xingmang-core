# XM-EXT-INTEGRATION：「接口与自动化」从只读蓝图建成真实页面

- **status:** implemented，本地全门禁通过；已 commit 到本分支，**未 push、未部署**。
- **branch:** `ai/claude/XM-EXT-INTEGRATION`（基线 `8c446e5`）。
- **commit:** 本分支**单提交**，parent（基线）`8c446e5`。提交 SHA 不写在这里——
  它是本文件内容的函数，写进去就会改变它自己；取值用 `git rev-parse HEAD`。
- **来源：** 产品负责人 2026-09-08 裁定，推翻 ADMIN-IA §5.4「扩展能力四页只读蓝图、
  不得因此提前建后端」对**这一页**的限制。裁定已按仓库规矩先写进
  `docs/architecture/ADMIN-IA.md` §5.4.1，再动代码。
- **上线影响：** 一条新迁移（两张新表）、四个新 L1 Action、两条新只读端点、
  两个新 scope、一页新前端。既有端点/Action/权限一律未改，**除了**
  `oidcauth.DefaultRoleScopeMap` 的 `admin` 多了两个 scope。
  没人点开这一页时生产行为不变。

---

## 一、地基调查：四格今天到底能接什么

调查对象是交办给的那几个模块。**结论与交办的假设有三处不同**，逐条写在下面。

### 1.1 「API调用方」——能接真数据，但真数据不在交办说的那几个模块里

| 交办点名的地基 | 实测它到底是什么 | 能不能用 |
|---|---|---|
| `internal/platform/principal/` | 纯类型包，零 I/O，不依赖 DB/HTTP（`doc.go` 明写）。**没有任何存储** | 只能借它的 `Type` 闭集 |
| `internal/platform/credentials/` | **出站**凭据登记（我们调上游用的 CredentialRef），`store.go` 顶部原话「明文以文件形式写进 SecretProvider…数据库只存引用」 | 不是入站 API Key，**不可用** |
| `internal/platform/platformusers/` | **被管平台的终端用户**（Sub2API/NewAPI 的客户），`doc.go`：「被管平台终端用户清单」的只读网关 | 与「谁在调我们」无关，**不可用** |
| `internal/platform/requestlog/` | **被管平台的**请求日志（nginx 在 NewAPI/Sub2API 之间抄录），`service.go` 顶部：「reqlog 只抄 NewAPI 与 Sub2API 两家」 | 回答的是「谁在调 Sub2API」，不是「谁在调星芒」，**不可用** |

**平台今天没有服务身份登记面。** 身份来源只有两处：`localauth`（员工账号，
全是 HUMAN）与 `oidcauth`（Keycloak Realm 角色 → scope）；
`httpapi.devHeaderResolver` 只在非生产可用。没有 client-credentials，
没有入站 API Key，没有任何一张表记着「哪些机器身份该来调我们」。

**真正记着「谁在什么时候调了我们什么」的只有两处**，都不在交办的清单里：

- `action.action_run`（`db/migrations/000002`）：每次经 Action 内核的**写操作**，
  含 `principal_id / principal_type / action_id / environment / risk_level /
  status / duration_ms / started_at`。已有端点 `GET /api/v1/actions/runs`
  （scope `action.read`），`RunFilter` 支持按 `PrincipalID` 过滤。**这是真数据。**
- `audit.audit_event`：同一批调用的 before/after 摘要（scope `audit.read`）。

**读操作查不到。** `httpapi.AccessLog`（`middleware.go:118`）确实记了
`principal_id / method / path / status / duration_ms`，但它只写进程的
结构化日志（`http_request` 行），**不落库、没有端点**。所以任何基于
`action_run` 的「谁来过」都只覆盖写操作——这句限定被做成响应字段
`observed_note` 随每次请求下发，前端照它渲染，不是写死在界面上。

**结论：这一格接真数据，做法是「登记簿 × 观测对账」。** 单给一张登记簿是
一份什么也不校验的台账；单给 `action_run` 的聚合又答不出「谁本该来」。
两侧一起给，才回答得了这一格真正要问的两个问题：
**登记了却从没做过写操作**（该清理）与**做过写操作却没登记**（该查）。
两句都带「写操作」这个限定，页面上也一样——见 §六。

### 1.2 「Webhook」——告警外发 ≠ 业务事件 webhook，这一格保持蓝图态

交办要求分清这两件事。**逐条查完的结论是：不是同一件事，差得很远。**

`internal/platform/notify/` 不是一个投递器，是**消息信封**（域徽标 / 严重度 /
环境 / 编号 / 处理入口）。真正发消息的是三个各自独立的实现，共四条链路：

| 链路 | 触发者 | 凭据引用 | 有没有投递记录 | 代码 |
|---|---|---|---|---|
| 告警 · 企微群机器人 | worker 每轮告警评估 | `secret://alerts/wecom-webhook`（`XM_ALERT_WECOM_WEBHOOK_REF`） | **有**：`alerts.alert.notify_status / notify_error / notified_at`（迁移 000007，且有 CHECK 让「静默失败」在库层不可表示） | `alerts/notify.go` |
| 告警 · Telegram Bot | 同上 | `XM_ALERT_TELEGRAM_BOT_REF` | **有**，与上一条共用那三列 | `alerts/notify.go` |
| 卡片事件 · 企微 | 收到 Infini 回调时 | `secret://cards/notify-webhook` | **没有**：即发即忘 | `cards/notify.go` |
| 接码验证码 · 企微 | 取到验证码时 | `secret://sms/notify-webhook` | **没有**：即发即忘 | `sms/notify.go` |

判断依据（不是措辞之争）：

- **收件人不同。** 这四条把消息推给**运营看**（群机器人聊天记录），不是把
  事件推给**外部系统消费**。`cards/notify.go` 的注释自己写着「卡号只带掩码…
  群机器人的消息留在聊天记录里，那不是我们能控制的存储」。
- **业务事件 webhook 的四样必需品一样都没有**：订阅登记（地址 + 事件类型 +
  密钥引用 + 状态）、签名、重试、逐次投递记录。四条链路的地址是写死在代码里
  的常量引用，事件类型是各自领域的硬编码，没有签名，没有重试，一半连成败
  都不落库。
- 平台唯一一个带签名校验的 webhook 是**入站**的 `cards_webhook.go`
  （Infini 打给我们），方向相反。

**所以这一格保持蓝图态**（冻结列头 `Webhook / 方向 / 事件类型 / 目标地址 /
签名 / 成功率 / 最近投递 / 状态 / 详情` 原样留着），落款换成如实归因，
并在它上面加一张**如实的**「出站通知通道」表——四条链路各自的凭据引用与
「有没有投递记录」都写出来。把告警外发摆进 Webhook 那张表冒充订阅，
正是本仓库最忌讳的那类事。

那张表的数据写在前端常量里，因为它描述的是**代码事实**而不是运行时数据；
每一行都注明了可以逐字核对的源文件。**只显示引用不显示地址**——群机器人
地址本身就是凭据（`credentials.ExpectedRefs()` 里那条的 Purpose 原话：
「企业微信群机器人 Webhook 地址（含 key，视为凭据）」）。

### 1.3 「自动化流程」——按裁定只做登记，没有执行器

平台今天没有任何规则引擎、没有事件订阅、没有规则表。按产品负责人的指令
只建**数据模型与登记**（建 / 改 / 停用），**不建执行器**。

理由写在页面上、写在端点响应里、也写在迁移的表注释里：自动触发 Action 会
绕开人工审批那道闸——L2 及以上必须有人批（宪法 9 条），而机器凑不出审批人；
就算是 L1（直接执行），「哪些 L1 允许被规则自动触发」本身也是一个需要单独
裁定的授权决定。

### 1.4 「运行记录」——五类，真有的两类都在别处

| 记录种类 | 有没有 | 在哪 |
|---|---|---|
| Action 执行记录 | 有 | `/actions?sub=runs`（`action.action_run`） |
| 告警投递状态 | 有，只覆盖告警那条通道 | `/alerts`（`alerts.alert` 的三列） |
| 卡片 / 接码推送结果 | **没有** | 两条通道即发即忘 |
| 流程运行记录 | **没有** | 没有执行器就没有运行 |
| HTTP 读请求 | **没有可查询记录** | 只进进程访问日志 |

这一格因此不自己造一份记录，也不把 Action 执行记录复制一遍（那会变成第二个
要维护的读数）：它渲染一张「五类各在哪里」的对照表 + 可点的入口，
下面保留冻结的「流程运行记录」蓝图列头。

### 1.5 逐格结论

| 格 | 状态 | 依据 |
|---|---|---|
| API调用方 | **接真数据**（新登记簿 × `action_run` 观测对账） | §1.1 |
| Webhook | **保持蓝图态** + 一张如实的出站通道表 | §1.2 |
| 自动化流程 | **接真数据，只有登记没有执行器** | §1.3 |
| 运行记录 | **保持蓝图态** + 一张「五类各在哪里」的对照表 | §1.4 |

---

## 二、数据模型与迁移

`db/migrations/000051_integration_registry.{up,down}.sql`（占用 **000051**，
按交办避开并行切片的 000050 / 000052）。

### 2.1 `core.api_client`

```
id uuid PK
principal_id   text NOT NULL           -- 与 principal.Principal.ID / action_run.principal_id 同源
principal_type text NOT NULL CHECK IN ('HUMAN','SERVICE','AI','SERVER_AGENT')
display_name   text NOT NULL
purpose / owner / notes  text NOT NULL DEFAULT ''
expected_scopes text[]  NOT NULL DEFAULT '{}'   -- 期望值，不是生效值
credential_ref text NOT NULL DEFAULT ''
    CHECK (= '' OR ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$')
status         text NOT NULL CHECK IN ('active','disabled')
environment    text NOT NULL REFERENCES core.environment
created_at/by, updated_at/by
UNIQUE (environment, principal_id)
```

三处刻意：

- **`principal_id` 与 `action_run.principal_id` 同源**，不另起「客户端编号」
  再维护映射——那等于给对账多加一层会漂的中间层。
- **列名是 `expected_scopes` 不是 `scopes`**：叫后者会让人以为改这一列就能改授权。
- **`credential_ref` 的 CHECK 与 `secrets.ParseCredentialRef` 的字符集逐字一致**
  （`^[a-z0-9][a-z0-9-]{0,63}$` 两段）。库层与解析器不一致的话，一条能存进来
  的引用会在解析时才炸。有一条集成用例直接用 SQL 绕过 Go 校验去撞这条 CHECK。

### 2.2 `core.automation_rule`

```
id uuid PK
name text NOT NULL
description / trigger_detail / notes  text NOT NULL DEFAULT ''
trigger_kind text NOT NULL CHECK IN ('manual','schedule','event','webhook')
target_action_id      text NOT NULL      -- 无外键，不校验是否已注册（见下）
target_action_version text NOT NULL
status text NOT NULL CHECK IN ('draft','registered','disabled')
environment text NOT NULL REFERENCES core.environment
created_at/by, updated_at/by
UNIQUE (environment, name)
```

两处刻意：

- **状态词表里没有 `enabled` / `active` / `running`。** 这张表没有执行器，
  一个叫 `enabled` 的状态会被读成运行开关。有一条 Go 单测（`TestRuleStatus
  HasNoEnabledSpelling`）与一条库层用例（用 `status='enabled'` 去撞 CHECK，
  带 `registered` 的对照组）一起钉住。
- **`target_action_id` 不做外键、不校验是否已注册。** 注册表是进程内的运行期
  对象，不在数据库里；在库层假装能校验它，只会在 Action 改版本那天变成一条
  挡住登记的假约束。补偿在读侧：列表端点对照注册表，如实给出
  `target_action_registered` 与 `target_action_risk_level`。

### 2.3 dbroles ——**没有改，这是刻意的，需要验收线确认**

交办要求「新表要同步 `internal/platform/dbroles/` 授权」。查完之后**没有照做**，
理由如下，请验收线判断是否要另开一片补上：

- `contracts/database/role-policy.v1.json` 是一份**经人工批准的快照契约**，
  与 `dbroles/policy.go` 的 `defaultObjects()` 双向对账
  （`OBJECT_UNKNOWN` / `OBJECT_MISSING` 两条违规码）。往里加表必须同时改
  Go 侧清单、重新生成 JSON、**并在 `role-policy-state-events.v1.jsonl` 追加一条
  `policy-update` 事件**；而 `policy_test.go:TestCheckedInPolicyContractLoads`
  硬钉了 `len(events) != 2` 与 `events[1].CurrentPolicySHA256`。这条链是设计
  来拦「改了库权限范围但没走审批」的，绕过它就是绕过一道治理闸。
- **XM-SERVER0 撞过完全一样的情况并做了同样的决定**（那份交接文档
  §risks 第一条：「已经 `git checkout --` 撤销了对 `policy.go` 的改动，
  `dbroles` 目录在本片里完全未改动」），并留下 follow_up 1。
- **今天不影响运行**：`deploy/compose/launch.yaml` 仍以
  `${POSTGRES_USER:-xingmang}`（owner）给 API/worker 组装 DSN，DBR3 runtime
  cutover 未合入。同一状态下未被策略覆盖的**共 40 张表 + 3 个整 schema**
  ——完整清单见 §2.5，不是「十余张」这种约数。
- 交办里那句「曾因漏授权导致生产失败 15 分钟」对应的是**开票仓库**
  （`invoice_app` 的表权限只来自 compose 的 permissions 作业），与本仓库
  这套 A/B 轮换策略不是同一套机制。

- **同一批里的第三个数据点**：并行的 `XM-READONLY-QUERIES` 那一片给
  `core.connector` / `core.connection` 加只读 Query 时**撞上同一条冻结契约，
  同样没有擅自改**，并把授权留给人拍板（那份交接文档的头部就写着
  「⚠️ 有一件事需要你拍板…dbroles 授权撞上一条冻结契约」）。三片各自
  独立撞到同一处、做了同一个决定。

**结论：本片与既有一批表处于同一状态，没有新增一类缺口；但在任何
DBR3/受限角色切换之前，这批表（含本片两张）必须先被一次性补进策略。**
**team-lead 2026-09-08 已批准这条偏离**，并要求把完整欠账清单交出来——见 §2.5。

### 2.5 策略契约的完整欠账清单（DBR3 的前置）

盘点方法：连**已灌完全部迁移**的测试库读 `information_schema.tables`，
与 `contracts/database/role-policy.v1.json` 的 `objects` 做差集
（不靠肉眼扫迁移文件——迁移里有 DROP/改名，只有库是真相）。

```
库里实际对象：73      契约登记的表/视图：33      欠登记：40      契约里有而库里没有：0
```

**这笔债是静默的**：运行期仍以 owner（`${POSTGRES_USER:-xingmang}`）连库，
所以今天一个症状都没有；只有角色拆分真正落地那天才会**一起爆**。

**先说一个比缺表更严重的形态：三个 schema 连 USAGE 授权都没有。**
契约里有 USAGE 的只有 `action / alerts / audit / core / finance / ops / public / ui`
八个；而 **`assurance` / `cards` / `sms` 三个 schema 整个不在里面**。
没有 schema USAGE，里面的表就算逐张补了授权也用不了——**补的时候必须先补
这三个 schema**，否则会得到一份「看起来补齐了但仍然连不上」的策略。

**A. 欠登记的 40 张表**（按 schema 分组；本片新增的两张已标出）

| schema | 条数 | 表 |
|---|---|---|
| `core` | 14 | `api_client`←**本片**、`automation_rule`←**本片**、`approval_request`、`approval_decision`、`connector_config`、`credential_ref`、`server_asset`、`server_domain`、`server_service_note`、`server_supplier`、`staff_account`、`staff_login_challenge`、`staff_session`、`staff_totp_recovery_code` |
| `sms` ⚠️无 schema USAGE | 12 | `alert_event`、`balance_snapshot`、`balance_threshold`、`consumer_quota`、`cost_event`、`provider_status`、`routing_rule`、`sms_code`、`sms_email`、`sms_operation`、`sms_order`、`sms_resource` |
| `cards` ⚠️无 schema USAGE | 8 | `card_challenge`、`card_operation`、`infini_card`、`infini_card_transaction`、`webhook_event`、`withdraw_address`、`withdraw_limit`、`withdraw_request` |
| `ops` | 3 | `metric_observation_daily`、`metric_rollup_receipt`、`metric_rollup_state` |
| `assurance` ⚠️无 schema USAGE | 3 | `probe_declaration`、`probe_result`、`probe_run` |

**本片贡献 2 / 40。** 补齐这 40 张（+3 个 schema）是一件比本片大得多的事，
且要走 `role-policy` 自己的批准流程（改 Go 侧 `defaultObjects()` + 重新生成
JSON + 追加 `policy-update` 事件 + 改
`policy_test.go` 里硬钉的 `len(events) == 2`）。

**B. 与「欠登记」不是一回事的 5 个对象**（已登记且显式标了
`no_runtime_access`，属于**明令隔离**）：
`audit.archive_operation_intent` / `archive_put_receipt` / `archive_segment` /
`archive_terminal_receipt`、`public.schema_migrations`。

> **2026-09-08 team-lead 决定：这已经不是「谁判断得对」的问题，而是那份契约的
> 维护流程本身跟不上开发节奏——四片（XM-SERVER0、XM-READONLY-QUERIES、
> `ext-app`/`ext-publishing` 同批，以及本片）各自独立撞上同一处并各自绕开。
> 这条会作为**独立问题提给产品负责人**，而不是继续以「每片各自绕开」的方式
> 积累。** 记在这里是因为决定未回写仓库不算正式决定（宪法 19 条）——
> 验收线读到本片时应当知道这件事已在升级中，而不是又一条被推迟的 follow_up。

> **并行片 `XM-READONLY-QUERIES` 确实在改这份策略，与本片不矛盾。**
> 它要读的 `public.schema_migrations` 就在 B 组——那是**破隔离**（明令禁止），
> 只能走正式的策略变更（建 `core` 只读视图 + 追加 `policy-update` 事件）；
> 本片这两张是 A 组的**欠登记**，与另外 38 张同状态，单独补反而制造
> 「有的补了有的没补」的不一致。**两类要分开处理，别混成一批。**

盘点脚本是一次性只读程序（只查 `information_schema`），没有入库，
不在提交里；上面的数字可用同样方法复现。

### 2.4 sqlc ——**一个 `gen/` 文件都没碰**

仓库里有一处**既有的** sqlc 漂移（`internal/platform/*/gen/models.go` 8 个文件
缺 000025 之后新增表的模型），跑一次全量 `go tool sqlc generate` 会把这 8 个
文件各重写约 +462 行；而现在有三个 agent 在三个工作树里各自新增迁移
（000050 / **000051** / 000052），谁跑全量生成都会产出一份含那 462 行历史
漂移的巨大 diff，三份互不相同、合并必撞。

**本片压根没进这条路：两张新表的仓储是裸 pgx（`store_pg.go`），
`action.PgCallerStore` 的汇总查询同样是裸 pgx。** 这不是为了规避漂移临时
选的，而是本仓库**最新几个模块的既定写法**——`sms`、`approval`、`cards`、
`credentials`、`localauth` 都没有 `gen/` 目录，用的是 `store_pg.go` + 裸 SQL。

所以：

- **没有跑过 `sqlc generate`**（一次都没有）；
- 提交里**没有任何 `gen/` 文件，也没有改 `sqlc.yaml`**（`git show --stat`
  与 `git status -- '*gen*' sqlc.yaml` 双向确认）；
- **没有可保留 / 需还原的生成结果**，因为一份都没产生；
- 那 8 个文件的历史漂移**没有顺手修**——修它会把另外两个 agent 正在做的
  迁移一起卷进来，而我看不到他们的表。
- `scripts/ci-local.sh` 的 sqlc 一致性检查今天本来就是红的（已知漂移），
  **本片没有让它变得更红，也没有为了让它变绿去重新生成**。

---

## 三、Action 清单与风险等级

四个全部 **L1 / HUMAN-only / 三环境**，权限 `integration.manage`。
契约同步写在 `contracts/actions/integration.*.v1.json`（等级钉在 Go 与 JSON
两处，仓库没有门禁校验一致性——两处都已写 L1 并附相同理由）。

| Action | 做什么 | reason 必填 |
|---|---|---|
| `integration.api_client.set@1` | 登记 / 改一个调用方（`client_id` 空 = 新建，整行替换语义） | 否 |
| `integration.api_client.set_status@1` | 单独启用 / 停用一条登记 | **是** |
| `integration.automation_rule.set@1` | 登记 / 改一条规则 | 否 |
| `integration.automation_rule.set_status@1` | 改一条规则的登记状态 | 否 |

**L1 的依据（据实，不是照抄）：**

- `api_client.*`：这张表**不是授权面**——登记不发凭据、不授予权限、不设配额，
  停用不会让任何请求被拒绝；授权仍完整地由 Keycloak 角色与员工账号角色决定。
  它不触碰第三方系统、不改变任何运行中的行为，写错改回即可，与
  `server.asset.set` / `finance.upstream_account.set` 完全同一档。
- `automation_rule.*`：这张表**没有执行器**——登记一条「当 X 发生时执行 Y」
  不会让 Y 跑起来。所以它写的是一条纯记录，同上一档。

**两条必须一起交待的重估条件**（已写进 Go 注释与 JSON 的 `notes`）：

1. 哪天请求路径上真的会查 `api_client`，等级必须重估——那时「停用一行」
   等于切断一条无人值守链路，不再是 L1。
2. 哪天真接上规则执行器，等级要按**被触发的目标 Action 里最高的那一档**重估
   ——一条能自动触发 L4 的规则本身就该按 L4 来批。

**HUMAN-only 的理由**：调用方登记簿决定「我们认为谁该来调我们」，规则登记簿
决定「我们打算让机器做什么」；让机器身份自己往这两张表里写，等于让它给自己
发通行证与工单（ADR-009）。交办提到的「对 SERVICE 开放的 Action 抬到 L2+ 会
让无人值守链路停摆」在这里不适用——这四个本来就不对 SERVICE 开放，且都是 L1。

**测试不钉字面等级**：`TestActionDefinitionsAreValidAndHumanOnly` 断言的是
`!def.RiskLevel.RequiresAdvancedControls()`（行为：不需要审批）与
`PrincipalTypes == [HUMAN]`，不是 `== "L1"`。

---

## 四、端点契约

两条只读端点，权限 `integration.read`。写路径不在这里——走
`POST /api/v1/actions/{id}/versions/{v}/execute`，权限由内核裁决。

### 4.1 `GET /api/v1/integration/api-clients?window_days=<1..90>`

```jsonc
{
  "items": [ { …登记字段…, "observed": { "run_count", "failed_count",
                "first_seen_at", "last_seen_at", "last_action_id",
                "last_status" } | null } ],
  "unregistered": [ …同一个 observed 形状… ],
  "window_days": 7,               // 默认 7；非法或越界一律 400，不静默夹边界
  "observed_since": "…",
  "observed_truncated": false,    // 窗口内身份数超过 200 时为 true
  "observed_source": "action.action_run",
  "observed_note": "只统计经 Action 内核的**写操作**…不能说明它没来过。",
  "registry_note": "登记簿不是授权面：登记不发凭据…"
}
```

- `observed` 为 `null` 表示窗口内**没有做过写操作**，不是编一个 `run_count: 0`
  的活动对象——后者与「真的跑了 0 次」长得一模一样。
- 两句限定随响应下发而不是写死在前端：一份读数与它的限定必须同源（宪法 12 条）。
- 环境取自调用者身份；`?environment=` 自称另一个环境一律 403，且**被拒之后
  一次库都不查**（有用例钉住）。
- `APIClients` 与 `CallerActivity` **缺任一半就不挂载这条路由**（404）。
  只给登记簿会变成一张什么也不校验的台账；变异实验里放宽这个条件之后，
  半装配的端点直接 500——这正是那道 nil 网关拦住的东西。

### 4.2 `GET /api/v1/integration/automation-rules`

```jsonc
{
  "items": [ { …登记字段…, "target_action_registered": bool,
               "target_action_risk_level": "L1" | "" } ],
  "automatic_execution": false,   // 恒 false，**不是配置项**
  "execution_note": "规则登记在此，但当前不会自动执行：平台没有规则执行器。…"
}
```

Handler 只拿一个 `ActionDefinitionLookup`（`Lookup` 的那一半），**丢掉
`Lookup` 返回的 handler**：给它一个能执行的对象，会让「这个端点会不会触发
Action」变成一个需要读实现才能回答的问题——而这一页的全部意义就是让那个
答案一眼可见。

---

## 五、dbroles 与 scope

- **dbroles：未改，见 §2.3。**
- **新 scope 两个**，都加进 `oidcauth.DefaultRoleScopeMap` 的 `admin`，
  `staff` 一个都不给：
  - `integration.read` — 读两张登记簿。**不复用 `registry.read`**：那个默认发给
    `staff`，而调用方登记簿是一张授权面的地图（谁该来调我们、期望持有哪些
    scope）+ `action_run` 里观测到的调用方，泄漏面与 `action.read` 同一档，
    看板角色不该顺带拿到。
  - `integration.manage` — 写两张登记簿。
- `resolver_test.go:TestDefaultRoleScopeMapIsConservative` 补了双向断言
  （admin 必须含、staff 必须不含）。
- **`STAFF_ROLE_CATALOG` 未改**：那份钉的是**角色**（与
  `DefaultRoleScopeMap` 的键对账），本片只往既有 `admin` 角色加 scope，
  没有新增角色。

---

## 六、前端

- `navigation.ts`：`/ext/integration` 的 `built` 翻成 `true`；
  `stage` **保持「后置」**——建成的只是四格里的两格半，分组本身仍是后置能力。
- `router.tsx`：加显式路由 `ext/integration`。`placeholderRoutes` 只收
  `!item.built`，漏加就会落到 `*` 兜底 404（`/jobs` 与治理段三页各撞过一次）。
  有一条 router 用例钉住，变异（删掉这行）实测变红。
- **`PlaceholderPage.tsx` 的 `/ext/*` 横幅与占位文案由 team-lead 统一处理，
  本片未动**（三个工作树各有一份副本，各改一遍合并必撞）。

  **附一条实测更正，供 team-lead 决定改法时参考：那条横幅并没有挂在这一页上。**
  `PlaceholderGate` 只在 `PlaceholderPage.tsx:91` 被调用一处，也就是只作用于
  **由 PlaceholderPage 渲染的页**；`/ext/integration` 现在有自己的显式路由渲染
  `ExtIntegrationPage`，根本不经过 PlaceholderPage。所以「页面能执行了、横幅
  还说不执行」这个组合在本片交付后**不会出现**——`router.test.tsx` 里那条
  `/ext/integration 落到真页面…` 的用例已经同步断言了
  `queryByText(/仅预览、不保存、不发布、不执行/)` 为 null，且是先 await 正向
  锚点再同步断言（实测通过）。
  仍然需要 team-lead 处理的是**另一件事**：`PlaceholderGate` 里那条
  `path.startsWith("/ext/")` 的判断今天靠「这一页还走不走 PlaceholderPage」
  间接成立，而不是靠它自己的条件——等三片都落地、只剩 `/ext/ai` 时，
  把它收成按 `built` 判断才是真正对的。
- `PLACEHOLDER_COPY["/ext/integration"]` 那条同样未动。它**今天已经是死代码**：
  `blueprint?.description ?? PLACEHOLDER_COPY[item.path]` 里蓝图那份恒胜出
  （这一页有蓝图），何况已经不走 PlaceholderPage。清理时一并删即可。
- `blueprints/ext.ts` **未改**：它是冻结的设计产出（`blueprints.test.ts` 与
  `navigation.ts` 逐字对账）。页面照 `ChangesPage` 的 `honestBlueprintTab()`
  做法就地换落款。
- 新增 `api/integration.ts` 与 `pages/ExtIntegrationPage.tsx`。
- **「只覆盖写操作」这个限定放在会被误读的那句话旁边**，不只押在下面的卡里：
  页签说明本身写成「谁在调我们的 API **做写操作**…**读操作不在这份观测里**」，
  未登记那张表的标题是「**做过写操作却没登记**」而不是「来过却没登记」。
  理由：「谁在调我们的 API」不加限定会被读成包含读请求，而读请求今天查不到
  （只进进程访问日志）。有专门用例钉住，变异（换回不带限定的旧措辞）实测变红。
- **没有复用 `GET /api/v1/connectors` / `/connections`**（并行片
  `XM-READONLY-QUERIES` 刚做的两条），也**没有造第二条同义端点**——
  它们与这一格是**相反方向**：那两条描述的是「平台有哪几种连接实现、
  某个连接用哪把出站凭据连上游」（`core.connector` / `core.connection`，
  `credential_ref` 是**我们调别人**用的）；本片的调用方登记簿描述的是
  「哪些身份来调**我们**」。同一条区分已经写在 §1.1 里对
  `internal/platform/credentials/` 的判断上。这一页不读连接器与连接。

页面上四处刻意的取舍：

1. **IP限制 / 调用配额留列不留数。** 冻结的九个列头一个不动，这两格渲染
   「未接入」而不是空——平台没有按调用方的 IP 允许清单（`localauth/adminip.go`
   那份是管理端登录白名单，不是同一件事），`ratelimit` 是进程内的通用 GCRA，
   没有按调用方的配额，也不读这张表。
2. **「详情」列不编一个详情页**，直接链到 `/actions?sub=runs&principal=<id>`
   ——这一行真正的详情就是它跑过哪些 Action，而那份记录已经有页面了。
3. **状态开关放行展开区，不新增第十列**：那九个列头是冻结的设计产出。
4. **规则表的「运行记录」列恒为「不会运行」，不是「暂无」**：后者会让人以为
   等一等就有了。变异（改成「暂无」）实测变红。

---

## 七、变异验证明细

每一条缺席型断言都做了变异 + 对照组。**变异一律改条件 / 接上被丢弃的东西，
不删代码**；每条都核对了「红在目标断言那一行」而不是红在锚点上。

| # | 变异 | 目标用例 | 实测红在 | 对照组（不该红） | 结果 |
|---|---|---|---|---|---|
| M1 | `ListAutomationRulesHandler` 里把 `defs.Lookup` 丢弃的 handler 接起来并调用（`if def, _, found` → `if def, h, found` + `h(...)`） | `TestListingAutomationRulesDoesNotExecuteTargetAction` | `integration_test.go:439`「列出规则不该执行目标 Action，实际执行了 1 次」——正是目标断言 | `TestAutomationRulesReportNoAutomaticExecution` + `TestAPIClientsReconciles…` | ✅ 红 / 对照绿 |
| M2 | `integration.AutomaticExecution()` 返回 `true` | `TestAutomaticExecutionIsFalse`（Go 单测）+ `TestAutomationRulesReportNoAutomaticExecution`（端点） | `types_test.go:139` 与 `integration_test.go:340`，均为目标断言 | M1 的两条「不会被执行」用例保持绿（它们证明的是「没有发生」，与这条报告的不是一件事） | ✅ 红 / 对照绿 |
| M3 | 把注册表接进规则登记的写路径，登记成功后执行目标 Action（3 行） | `TestRegisteringAutomationRuleDoesNotExecuteTargetAction` | `actions_test.go:406`「登记规则不该执行目标 Action，实际执行了 1 次」——目标断言 | `TestAutomaticExecutionIsFalse` / `TestActionDefinitionsAreValid…` / `TestAPIClientRoundTrip` | ✅ 红 / 对照绿 |
| M4 | 对账分流条件翻转（`!known` → `known \|\| true`） | `TestAPIClientsReconcilesRegistryAgainstObserved` | `integration_test.go:182`「未登记调用方应恰好是 staff_bob」 | 见下一行的对照 | ✅ 红 |
| M5 | 路由挂载条件放宽（`APIClients != nil && CallerActivity != nil` → 只判前者） | `TestAPIClientsNotMountedWithoutBothHalves` | `integration_test.go:283`「只有登记簿一半时端点不该存在，实际 500」 | M4+M5 同时施加时 `TestAutomationRulesReport…` / `TestAPIClientsRequiresIntegrationScope` / `TestAPIClientsUsesCallerEnvironment` 全绿 | ✅ 红 / 对照绿 |
| FM1 | 前端 `RULE_STATUS_LABELS.registered` 改成「已启用」 | `已登记的规则不呈现为运行态…` | 该用例（其余 1942 条全绿） | 同一次运行里的其余全部用例 | ✅ 红 / 对照绿 |
| FM2 | 规则表「运行记录」列文案「不会运行」→「暂无」 | 同上 | 同上 | 同上 | ✅ 红 / 对照绿 |
| FM3 | 删掉 `router.tsx` 里 `ext/integration` 那条显式路由 | `/ext/integration 落到真页面…` | 该用例（其余 1943 条全绿） | 同一次运行里的其余全部用例 | ✅ 红 / 对照绿 |
| FM4 | 把页签说明换回不带「写操作」限定的旧措辞 | `「只覆盖写操作」这个限定出现在会被误读的那句话旁边…` | 该用例（其余 1944 条全绿） | 同一次运行里的其余全部用例 | ✅ 红 / 对照绿 |

补充的两类防伪（不是变异，但同样是为了防恒真）：

- **对照组内建在用例里**：
  `TestRegisteringAutomationRuleDoesNotExecuteTargetAction` 有一个子测试
  「对照组」，用**同一个内核、同一个注册表**直接执行 spy，计数必须 0→1；
  `TestListingAutomationRules…` 末尾直接调 handler 验证计数器是活的。
  没有它们，「计数恒为 0」也可能只是因为计数器根本不工作。
- **`automatic_execution: true` 的正向用例**：前端多一条用例让 fixture 给
  `true`，断言徽章改口成「自动执行：开」——证明「自动执行：关」那条断言
  **认得出**另一种取值，不是一个恒显示「关」的死徽章。
- **缺席断言一律先 `await` 正向锚点再同步断言**，没有一条 `waitFor` 套在
  缺席断言外面（那几乎必然恒真）。
- **两条库层 CHECK 用例各带对照组**：撞 `credential_ref` CHECK 的那条，
  换成合法引用同一条 SQL 必须成功；撞 `status='enabled'` 的那条，
  换成 `registered` 必须成功——否则那条红可能来自列名写错之类的别的原因。

---

## 八、门禁结果

全部在 `K:/星芒统一控制平台/wt-XM-EXT-INTEGRATION` 本地跑通。

| 门禁 | 结果 |
|---|---|
| `go test -p 1 -count=1 ./...`（带 `XM_TEST_DATABASE_URL`，八个代理变量全 unset） | **exit 0**，79 个包 ok / no test files（基线 78，本片 +1 = 新增的 `integration` 包） |
| `go vet ./...` | **exit 0** |
| `bash scripts/check-governance.sh` | **exit 0**（迁移不可变检查确实执行了——本地能解析 `origin/main`，没有打出「跳过」提示） |
| `pnpm --config.verify-deps-before-run=false -r run typecheck` | **exit 0**，5/5 Done |
| `pnpm --config.verify-deps-before-run=false -r run test` | **exit 0**；design-tokens 10 / ui-primitives 16 / ui-admin 262 / admin-web **1945** 全绿 |
| `pnpm --config.verify-deps-before-run=false -r run build` | **exit 0**；grep 确认 admin-web 真有产物行：`web/apps/admin-web build: ✓ 361 modules transformed.`，storybook 也 Done（没有出现「打完包后 storybook 崩掉让 admin-web 根本没跑」那种假绿） |

`-p 1` 没漏。八个代理变量（`HTTP_PROXY / HTTPS_PROXY / ALL_PROXY / NO_PROXY`
及各自小写）全部 unset——本机不这么做时 `connectors/*` 会成片假红。

数据库集成用例**确认真的跑了、不是 skip**：`-run` 单点验证过
`TestAPIClientEnvironmentIsScoped` / `TestAPIClientCredentialRefCheckIsEnforced
InDatabase` / `TestAutomationRuleStatusCheckIsEnforcedInDatabase` /
三条 `TestAggregateCallers*` 全部 `--- PASS`。

---

## 九、files_changed

**新增**

```
db/migrations/000051_integration_registry.up.sql
db/migrations/000051_integration_registry.down.sql
internal/platform/integration/{doc,types,permissions,store_pg,actions}.go
internal/platform/integration/{types_test,actions_test,store_integration_test}.go
internal/platform/action/callers.go
internal/platform/action/callers_integration_test.go
internal/platform/httpapi/integration.go
internal/platform/httpapi/integration_test.go
contracts/actions/integration.api_client.set.v1.json
contracts/actions/integration.api_client.set_status.v1.json
contracts/actions/integration.automation_rule.set.v1.json
contracts/actions/integration.automation_rule.set_status.v1.json
web/apps/admin-web/src/api/integration.ts
web/apps/admin-web/src/pages/ExtIntegrationPage.tsx
web/apps/admin-web/src/pages/ExtIntegrationPage.test.tsx
```

**修改**

```
docs/architecture/ADMIN-IA.md            §5.4 + 新 §5.4.1 裁定变更、§九 切片条目
cmd/platform-api/main.go                 装配 integration.Store + 注册四个 Action + 三个 Deps
internal/platform/httpapi/router.go      Deps 四个字段 + 两条路由（nil 网关）
internal/platform/oidcauth/rolemap.go    admin 加两个 scope
internal/platform/oidcauth/resolver_test.go  双向断言
web/packages/ui-admin/src/navigation.ts       built: true
web/packages/ui-admin/src/navigation.test.ts  两份清单 + 一条新用例
web/apps/admin-web/src/router.tsx             显式路由
web/apps/admin-web/src/router.test.tsx        okHandler 兜底 + 两条断言 + 一条新用例
```

---

## 十、risks

1. **dbroles 未同步（§2.3）；策略契约已落后现实 40 张表 + 3 个整 schema（§2.5）。**
   与既有 38 张表同状态，今天不影响运行（runtime 仍以 owner 连库）——
   **正因为不影响，它是一笔静默的债**：只有 DBR3 角色拆分落地那天才会一起爆。
   偏离已由 team-lead 2026-09-08 批准（理由：单独补本片两张反而制造
   「有的补了有的没补」的不一致，比统一地欠着更难收拾）。完整欠账清单与
   补法见 §2.5 与 follow_up 1。
2. **调用方登记簿不参与鉴权。** 这是本片刻意的设计（它是台账不是闸门），
   页面、端点响应、迁移注释、Action 契约四处都写了这句话。风险在于
   **将来有人把它当成闸门**——真要做成授权面，必须重估 Action 等级
   （§3 的重估条件 1）并在请求路径上加查表，那是另一片。
3. **观测侧只覆盖写操作。** 「某个身份一次都没出现」推不出「它没来过」。
   这句限定随响应下发并显示在页面上，但仍可能被快速扫一眼的人误读。
   要覆盖读操作需要把访问日志落库并建 Query，本片没做。
4. **观测侧有 200 个身份的上限**，超出时 `observed_truncated: true` 且页面
   显示一条「这份观测不完整」的告警条。上限是防一个乱来的调用方用随机身份
   把结果撑爆；正常规模远不到。
5. **出站通知通道那张表是前端常量**，描述的是代码事实。四条链路的凭据引用
   若在代码里改名，这张表会漂。已在注释里注明每行的源文件以便核对，
   但**没有机器校验**（跨 Go/TS 边界）。
6. **规则的 `target_action_id` 允许指向未注册的 Action**（刻意，§2.2）。
   读侧会标出来，但如果有人把这张表当计划清单用，可能积累一批指向已下线
   Action 的死规则。

---

## 十一、not_run

- **没有起真实进程验证过端到端**（没有 `deploy-local`、没有浏览器）。
  验证止于 Go 集成测试（真库）+ vitest（jsdom）。
- **没有跑 Storybook 之外的视觉回归**：这一页没有新增 ui-admin 组件，
  用的都是既有的 `DataTableV2` / `StatTile` / `PageState` / `Badge`。
- **没有做迁移回滚演练**：`000051` 的 down 只是两条 `DROP TABLE IF EXISTS`，
  未在真库上跑过 down→up 往返。
- **没有 push、没有部署、没有碰生产**。

---

## 十二、follow_ups

1. **【优先级最高】策略契约的欠账必须在 DBR3 之前一次性补齐——完整清单见 §2.5。**

   规模不是「本片两张表」，是 **40 张表 + 3 个整 schema**：
   `core` 14、`sms` 12、`cards` 8、`ops` 3、`assurance` 3；其中
   **`assurance` / `cards` / `sms` 三个 schema 连 USAGE 授权都没有**——
   不先补这三个，逐张补表也用不了。

   **这笔债的危险之处在于它是静默的**：运行期仍以 owner 连库，今天零症状，
   只有角色拆分真正落地那天才会一起爆，而且是**一起**。做 DBR3 的人拿到
   §2.5 那张表就能直接开工，不必自己再盘一遍（盘法也写在那里：连已灌完
   全部迁移的库读 `information_schema`，不要扫迁移文件——迁移里有 DROP/改名）。

   补的时候要走 `role-policy` 自己的批准流程：改 Go 侧 `defaultObjects()`
   + 重新生成 JSON + 追加 `policy-update` 事件 + 改 `policy_test.go` 里
   硬钉的 `len(events) == 2`。

   **注意与 `XM-READONLY-QUERIES` 那一片分开**：它改策略是因为
   `public.schema_migrations` 被显式标了 `no_runtime_access`（**破隔离**，
   明令禁止），必须走正式变更；本片这两张是**欠登记**，与另外 38 张同状态。
   两类别混成一批。
2. **规则引擎要不要真执行**——需要产品负责人单独裁定：能执行到哪个风险等级、
   L2+ 的审批人从哪来、Kill Switch 在哪。裁定之后才谈执行器与 Action 等级重估。
3. **通用业务事件 Webhook**（§1.2）：要做的话是一个独立切片，
   四样必需品（订阅登记 / 签名 / 重试 / 逐次投递记录）一样都不能省，
   密钥只经 CredentialRef。
4. **卡片与接码两条推送通道补投递记录**：它们今天即发即忘，
   一条推不出去的验证码不会留下任何痕迹。与本片无关，但是在调查中发现的
   真实缺口，登记在此。
5. **访问日志落库**：让「谁读了什么」也可查，才谈得上完整的调用方画像。
   注意它会显著增加留存面，属于要单独权衡的事。
6. **调用方登记簿是否要变成授权面**：如果决定要，等级重估 + 请求路径查表 +
   缓存策略都要一起设计（§3 重估条件 1）。
7. **`web/apps/admin-web/src/lib/designSpec.ts:230` 那句话要重写**（team-lead
   普查发现，**文件不在本片范围内，未动**）。原文：「流程节点 — 等审批链前端与
   『接口与自动化』（/ext/integration）落地，两处共用同一种节点。」两个前提
   现在都不成立了：审批链前端早已落地，`/ext/integration` 就是本片。
   重写时要注意别把它改成「已落地、两处共用同一种节点」——**本片没有做
   流程画布，也没有节点组件**，规则登记是一张表不是一张画布；那句话真正
   该等的是「规则引擎要不要真执行」的裁定（follow_up 2）。
8. **`PlaceholderPage.tsx` 的清理**由 team-lead 统一做（见 §六）：
   `PLACEHOLDER_COPY` 里 `/ext/integration` 那条死代码可删；
   `PlaceholderGate` 的 `/ext/*` 判断建议收成按 `built` 判断，
   而不是继续靠「这一页还走不走 PlaceholderPage」间接成立。
