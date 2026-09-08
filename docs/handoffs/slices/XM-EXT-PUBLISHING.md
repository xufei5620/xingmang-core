# XM-EXT-PUBLISHING：内容发布从只读蓝图建成真实页面（第一层）

## status

implemented，**已在分支内提交、未推送、未部署**。全部本地门禁自己跑通（见「门禁结果」）。

## branch / commit / base

- branch: `ai/claude/XM-EXT-PUBLISHING`
- commit: `7cdbef4`
- worktree: `K:/星芒统一控制平台/wt-XM-EXT-PUBLISHING`
- base: `8c446e5`（`feat(admin-web): 订阅批次与代理资产的退款、终止四个入口`）
- 只动了本 worktree 下的文件；没有碰 `K:/发票/`、其它 `wt-*`、以及
  `K:/sub2api-src` / `K:/newapi-src`（那两个只**读**，用于站内公告调查）。

## summary

ADMIN-IA §5.4 原本裁定扩展能力四页「只读蓝图，不得因此提前建后端」。产品负责人
2026-09-08 推翻了这一条在**内容发布这一页**上的适用，要求真建。本片按规矩先改
`docs/architecture/ADMIN-IA.md`（新增 §5.4.1 记录裁定变更与日期，并在 §九 补一条
落地记录），再改代码。

交付**第一层**：内容的生命周期在平台内是真的——草稿与不可变版本、素材引用、
排期、渠道与账号登记（凭据只经 CredentialRef）、发布审批（复用 XM-0030a 的审批
中心）、发布记录。**第二层（真正的出站投递）不做**，并且这条缺席在库层、代码层
与用例层三处钉住，还做了变异验证。

---

## 一、站内公告那条线：**不通**，不是「以后再接」

产品负责人的原话是「站内公告也很不错，但是不能改动 sub2api 和 newapi 的源码」。
按派工要求去查了两条路，结论如下。**两条都不通，所以这条线本片一格都不做，
也不在页面上留任何假入口。**

### 1.1 上游自己的公告接口：**有，而且很完整——但平台不许写它们**

**Sub2API（读的是只读参考克隆 `K:/sub2api-src`，未做任何修改）**

- 管理端有一整套公告 CRUD：`backend/internal/server/routes/admin.go` 的
  `registerAnnouncementRoutes` 注册了
  `GET/POST /api/admin/announcements`、`GET/PUT/DELETE /api/admin/announcements/:id`、
  `GET /api/admin/announcements/:id/read-status`；
- 用户端有 `GET /api/announcements` 与 `POST /api/announcements/:id/read`
  （`routes/user.go`）；
- 数据结构齐全：`handler/dto/announcement.go` 有 title / content / status /
  notify_mode / **targeting**（定向）/ starts_at / ends_at，还有独立的
  `announcement_reads` 表做已读状态；
- **生产版本具备这个能力**：该功能引入于 `b7f69844e`（`git describe --contains`
  给出 `v0.1.178~18^2~601^2~1`，即 v0.1.178 之前就有），而生产实例无论按平台
  自己钉的 0.1.179 还是主机上实际的 0.2.1 二进制，都在这之后。

也就是说，**功能上完全够用**。挡住它的是三条，一条比一条硬：

1. **平台发不出 POST 给它。** `connector.ReadOnlyTransport` 在 `RoundTrip` 层
   拒绝一切非 GET/HEAD（ADR-018 闸 4）。`connectors/sub2api/client.go` 的注释
   把四道闸的落点写得很清楚。
2. **ADR-021 明文排除这条路。** 新增的 `connector.VendorWriteTransport`
   （为 Infini 卡服务加的）自己写着：「**适用范围仅限「平台作为客户购买服务的
   供应商」。复用本 ADR 去写 NewAPI/Sub2API 或任何「平台不拥有其业务真相」的
   上游，仍然违反 ADR-018 与宪法条款 5**」。
3. **权限面过大。** Sub2API 的管理路由挂在 `AdminAuthMiddleware` 上，而它的角色
   只有 admin/user 两档、无细分（`users.role VARCHAR(20)`，001_init 起未变）。
   为了发公告，平台得持一把**整站管理员**凭据——那比公告本身危险得多。

**NewAPI（只读参考克隆 `K:/newapi-src`）** —— 能力弱得多，而且同样被排除：

- 公告不是实体，是一个**全局设置项** `console_setting.announcements`
  （`setting/console_setting/config.go`：「系统公告 (JSON 数组字符串)」）；
- 唯一的写入口是 `PUT /api/option/`（`router/api-router.go` 的 `optionRoute`，
  中间件是 **`RootAuth()`** ——比 admin 还高一档），`controller/option.go` 里
  `case "console_setting.announcements"` 只做一次格式校验；
- 没有单条 API、没有已读状态、没有定向。整块 JSON 覆写意味着**读-改-写会丢
  更新**：平台和运营同时改一次，后写的把前一次抹掉，而且没有任何冲突检测。
- 一样受 ADR-018 / ADR-021 排除。

### 1.2 平台自有通道：**只有企业微信群机器人，收件人是运维不是终端用户**

- `internal/platform/notify` 名字容易误会——它**只是消息信封**（渲染企微
  markdown 的头尾、域徽标、严重度、编号），不含任何投递；
- 真正的投递在 `internal/platform/alerts/notify.go`：`WeComNotifier`（企微群
  机器人 webhook）与 `WebhookNotifier`（自建 https 端点）。两者的收件人都是
  **运维群**，不是 Sub2API/NewAPI 的终端用户；
- **平台没有邮件能力**：全仓 `grep -in "smtp|net/mail|gomail|sendmail"` 在 Go 代码里
  一条都没有，`go.mod` 里也没有任何邮件库。SMTP 只存在于**开票系统**（另一个
  仓库，`K:/发票/`），那条线的产物不经过这个后台。

### 1.3 结论与本片的处理

**站内公告 = 等被管平台开放接口 / 等产品负责人另立 ADR。** 具体在等的是一次
**架构裁定**（要不要为「平台向被管平台写公告」开一条新的写通道，以及愿不愿意让
平台持有上游的整站管理员凭据），不是排期，也不是接线工作量。

本片因此：

- `publishing.Platform` 这个闭集**刻意不含** announcement / sub2api / newapi
  （`internal/platform/publishing/model.go`，库层 CHECK 同）——渠道下拉框里放一个
  发不出去的取值，等于摆一个假入口；
- 「渠道与账号」那一格页尾就地写清这条线为什么不在这里，并点名 ADR-018 与
  ADR-021（`PublishingPage.tsx`，有用例逐字钉住）。

---

## 二、数据模型与迁移

迁移 **`000052_content_publishing`**（另两个 agent 用 000050/000051，未占用）。
新 schema `publishing`，六张表：

| 表 | 回答什么 | 要点 |
|---|---|---|
| `channel` | 渠道与账号登记 | `credential_ref` 有库层正则，只收 `secret://<scope>/<name>`；唯一键 (environment, platform, handle) |
| `draft` | 草稿 | 状态只有 DRAFT/SCHEDULED/ARCHIVED，**没有 PUBLISHED**；CHECK 强制「SCHEDULED ⇔ 有排期时间」 |
| `draft_revision` | 不可变修订 | 主键 (draft_id, version)；发布记录钉的就是某一版 |
| `asset` | 素材**引用**（不是上传） | `uri` 强制 https——素材地址会随对外内容一起公开 |
| `draft_asset` | 草稿引用了哪些素材 | asset 侧 `ON DELETE RESTRICT`：还被引用的素材删不掉 |
| `publish_record` | 发布记录 | `result` 闭集**只有 `NOT_DELIVERED`**；另有 CHECK 禁止「未投递却带平台返回编号/投递时刻」 |

两个刻意的取舍，都写在迁移注释里：

- **`publish_record.result` 只有一个取值**，是为了让「接上出站投递器」必须经过
  一次显式迁移与一次显式评审，而不是某天有人往枚举里补两个词就悄悄成立了。
- **没有 `approval_id` 列**：Handler 拿不到单号（`action.Kernel.ExecuteApproved`
  只把 approvalID 放进审计事件的 resource_id，不放进 Handler 的 context），为此
  改内核是一次共享热点文件的改动，不值当。链路仍完整，只是绕一跳：
  `approval_request.execution_run_id → action_run.id → audit_event.action_run_id`。
  **加一列永远为空的字段比不加更糟**。

### 关于 `internal/platform/dbroles/` 授权 —— 按现状**没有改**，理由请复核

派工要求「新表要同步 dbroles 授权」。查下来现状是：

- `contracts/database/role-policy.v1.json` 是 **XM-DBR0 冻结的目标拓扑快照**
  （只有两次提交：`8edb9b7` 定义、`d71e5f4` 加校验器），不是随迁移增量维护的东西；
- 实测漂移：**迁移里建的 64 张表中有 38 张不在这份 artifact 里**——
  自 000019 之后新建的表（ops 的三张、cards 全部、sms 全部、assurance 全部、
  staff/server/credential 全部，以及 XM-0030a 的 `core.approval_request` /
  `approval_decision`）一张都没登记过；
- 角色拆分本身**尚未应用**：XM-DBR0 的 handoff 写明 DBR2/3/4 逐片另批，
  staging/production/live cutover 仍 NO-GO，`db/migrations/` 与 `deploy/` 里
  没有任何 GRANT，平台今天仍以单一登录身份跑；
- `scripts/check-governance.sh` 不检查这份 artifact。

所以本片**跟随既有先例不改它**（改它等于单独给 `publishing` 6 张表补登记，
而它旁边 38 张表还缺着，反而制造「这份文件是最新的」的错觉）。
**这不是我认为该这样，是我不想在一个交接片里替 DBR 线做决定** —— 建议在
DBR2 落地时一次性补齐全部 44 张表；本片的 6 张表清单已列在上表。

### 关于 sqlc —— 本片**零足迹**

`go tool sqlc generate` **一次都没跑**。核对过：`git diff 8c446e5..HEAD -- 'internal/platform/*/gen/'`
为空，本片两个 commit 里没有任何 `gen/models.go`。

不需要跑的原因：`internal/platform/publishing/store_pg.go` 是**手写 pgx**，与
`approval` / `sms` / `cards` 同一形态；`sqlc.yaml` 里也没有 publishing 的条目。

**据此提醒下一个人**：仓库里已存在一处 sqlc 漂移（8 个 `gen/models.go` 缺 000025
之后新增表的模型，全量重新生成会各 +462 行）。那不是本片造成的，本片也没有碰它。
`scripts/ci-local.sh` 的 sqlc 一致性检查今天本来就是红的——**别为了让它变绿去全量
重新生成**，那会把三个工作树各自在做的迁移卷成三份互不相同的巨大 diff。

**生产上线要注意的一点**：本迁移 `CREATE SCHEMA publishing`（与 000001~000025
建 core/action/audit/ops/alerts/finance/ui/assurance 同一形态，有先例）。角色拆分
真正落地后，新 schema 的 default ACL 需要一并配——那正是开票线 RC101 栽过的坑
（新表无权限、通知 worker 刷 42501）。

---

## 三、审批中心怎么复用的

**一行审批逻辑都没有重写。** 复用方式：

1. `publishing.publish.submit` 定级 **L3**，于是内核（`action/kernel.go`）自己就把
   它受理成一张审批单，返回 202 + `approval_request_id`；
2. 前端用 `submitAction`（`api/platform.ts`，XM-ACTION-REASON 那一片建好的），
   两种结局都画出来——**不把「已受理为审批单 X」显示成成功**；
3. 「审批队列」那一格直接读审批中心的 `GET /api/v1/approvals`，按
   `action_id === "publishing.publish.submit"` 过滤；
4. **投票与触发执行不在本页做**，页面上就地写明它们在审批中心页面完成。
   在这里再开一套入口等于把同一件事实现两遍。

---

## 四、Action 清单与风险等级 + 依据

七个 Action，**全部只给人**（`PrincipalTypes` 只有 HUMAN；有用例遍历注册表钉住）。
等级钉在 Go 与 `contracts/actions/*.v1.json` 两处（仓库没有门禁校验一致性，
这次两边是手工对齐的）。

| Action | 等级 | 权限 | 依据 |
|---|---|---|---|
| `publishing.draft.set` | L1 | `publishing.manage` | ADR-003「修改低风险平台配置」；草稿完全在平台内，且每版留不可变修订 |
| `publishing.draft.archive` | L1 | `publishing.manage` | 同上；归档不是删除 |
| `publishing.asset.set` | L1 | `publishing.manage` | 纯登记 |
| `publishing.asset.remove` | L1 | `publishing.manage` | 仍被引用时库层 RESTRICT 挡住 |
| `publishing.channel.set` | **L1** | `publishing.manage` | 见下面「一次被测试推翻的定级」 |
| `publishing.channel.set_status` | L1 | `publishing.manage` | 危险方向单向（只会让渠道**更不容易**发出去）；把「暂停一个出事的渠道」压进审批队列，等于在最需要立刻停手时加一道等 |
| `publishing.publish.submit` | **L3** | `publishing.publish` | 见下 |

### 4.1 `publishing.publish.submit` 为什么是 L3

判据只用仓库里写下来的话（XM-RISK-RESTORE 的纪律）：

1. ADR-003 的 L4 是「退款、生产基础设施高影响动作、开票关键动作」，L3 是
   「服务切换、账号批量导入、敏感配置」。对外发布不属于花钱那一类，但它与
   `cards.withdraw.execute` 共享同一条最要紧的性质：**把东西送到平台之外，
   不可逆、不可追回**——发出去的内容即便删除也已经被抓取、被截图。
2. 原型自己写的话（`blueprints/ext.ts` 审批队列格，逐字）：「对外发布属于有外部
   影响的操作，必须人工审核，不会有自动放行。」**L2 允许提交人自批**
   （`approval.DefaultPolicy`），自批不是审核；L3 是「两票且审批人≠提交人」，
   那才是这句话说的东西。
3. 宪法 9 条：「L3/L4 必须审批」。

**现在定级而不是等第二层接上再抬，是刻意的**：今天这个动作发不出去任何东西，
L3 的摩擦此刻是零成本的；事后抬级是一次行为变更，而且要在「已经有人依赖这条路
很顺畅」之后做——XM-RISK-RESTORE 整片就是在还这笔账。

### 4.2 一次**被测试推翻**的定级：`publishing.channel.set` 从 L2 退回 L1

这一条最初写成 L2（理由是「它决定以后以谁的名义、用哪把钥匙对外说话」，与
`registry.connector.create` 对齐）。写完之后
`TestChannelRejectsPastedPlaintextCredential` 立刻红了，**红出来的是一个真问题**：

> 内核对 L2+ 的顺序是 …→ 权限 → Schema → **风险闸（冻结参数、落审批单）**，
> Handler 在这之后才跑。而 `action.Schema` 的字段只支持 `Required` 与 `Enum`，
> **表达不了 CredentialRef 的形状**——那道校验只能写在 Handler 里。于是运营一旦
> 把 token 本身粘进 `credential_ref`，L2 会先把这个串**原样冻进
> `core.approval_request.params_json`**（永久落库、还会显示给审批人看），
> 然后才轮到 Handler 说「这不是引用」。

用一次性探针实测确认过：`ApprovalSubmission.Params["credential_ref"]` 里就是那个
原串。也就是说，把这个动作定成 L2 会**亲手造出一条明文凭据泄漏路径**（宪法 7 条），
换来的只是「一票可自批」的一张单。

退回 L1 的正面依据也站得住：同类先例
（`finance.platform_channel_binding.set`、`server.supplier.set`、
`finance.upstream_account.set`）都是 L1，而渠道登记比第一条还轻——它**自己不会让
任何内容发出去**，真正不可逆的那一下是 L3 的发布。护栏一道没减：只收
CredentialRef（领域层 + 库层两道）、`publishing.manage` 权限、全量审计。

**留了口子**：等 `action.Schema` 支持字段形状校验（正则/格式），这个动作可以安全
地回到 L2——那时明文会在冻结之前就被拒。已进 follow_ups。

### 4.3 新 scope 与角色（已同步 `STAFF_ROLE_CATALOG`，两向都有测试钉着）

三个 scope：`publishing.read` / `publishing.manage` / `publishing.publish`。

- `admin` 拿到 **read + manage**（不给就等于这一页对唯一能用它的人也是 403——
  卡片上线当天撞过这件事）；
- **`publishing.publish` 刻意不进 admin**，与 `fund.withdraw` / `sms.purchase`
  同一条设计意图：把「对外不可逆」的动作从日常操作角色里拿出来。审批拦的是
  「这一篇发不发」，权限拦的是「谁能提起这件事」；
- 新角色 **`content-publisher`** = `{publishing.read, publishing.publish}`。
  带 read 是为了让持发布权的人看得见自己要发的东西，否则他必须同时是 admin；
  **不带 manage**——改稿与发稿刻意分开；
- `oidcauth/rolemap.go` 的 `platformScopePrefixes` 补了 `"publishing."`：Realm 里
  出现一个 `publishing.publish` 角色就是配置漂移（ADR-016 / CR-0001 §5）；
- `web/apps/admin-web/src/api/staff.ts` 的 `STAFF_ROLE_CATALOG` 补了
  `content-publisher`（`rolemap_catalog_test.go` 两向都钉着，漏一边就红）。

---

## 五、凭据怎么处理的

**明文一个字都不进来。** 三道：

1. 领域层 `NormalizeCredentialRef`：非空时必须解析成合法 `secrets.CredentialRef`，
   否则 `INVALID_PARAMS`。**错误信息里不回显原串**——
   `ParseCredentialRef` 的错误消息带着原串，而那一刻它可能正是一条被误粘的
   明文 token，拼进去等于把它写进 ActionRun、审计事件与访问日志；
2. 库层 CHECK：`credential_ref = '' OR credential_ref ~ '^secret://…$'`，
   挡住任何绕开 Action 的路径（修数据脚本、将来的批量导入）；
3. 风险等级选 L1 而不是 L2（见 §4.2），让上面第 1 道**在参数被冻结之前**跑到。

审计摘要里只记 `credential_ref_present: true/false`，不记引用本身。
页面上引用**原样回显**（它不是秘密，运营要看得出这个渠道绑的是哪一条）。

---

## 六、哪几格保持蓝图态，各自在等什么

| 位置 | 状态 | 在等什么 |
|---|---|---|
| 真正的出站投递（X 等平台） | **蓝图态** | 第二层：Connector 契约、OAuth、速率限制、失败重试、幂等键，以及一次放开 `result` 枚举的迁移 |
| 站内公告 | **蓝图态** | 产品负责人另立 ADR（见 §一）。**不是排期** |
| 发布记录的「互动数据」列 | 留列不留数 | 没有出站投递就没有回读通道 |
| 发布记录的「平台返回编号」列 | 留列不留数（库层强制为空） | 同上 |
| 内容日历「到点自动发」 | 不做，且页面写明 | 需要一个定时投递任务；没有投递器时它没有意义 |

页面上**每一处都就地写清**，不靠一句总的免责声明：渠道表逐行写「未接投递器」、
发布记录逐条带出那句解释、页头一条常驻门禁。理由是横幅读一次就被忽略，
而每一行都写着的表格骗不了人。

---

## 七、「当前没有出站投递器」是怎么钉住的

这是本片对使用者最重要的一句交代，也是一条**缺席型主张**，所以三层都钉了，
并且每一条都做了变异验证（明细见 §八）：

1. **库层**：`publish_record.result` 闭集只有 `NOT_DELIVERED`；带编号或投递时刻的
   「未投递」行被 CHECK 拒绝（`TestPublishRecordRejectsDeliveredShape` 对真库验证）。
2. **代码层**：`publishing.Service` 持一张 `deliverers` 表，生产装配
   （`cmd/platform-api/main.go`）传 **nil**。这不是可以在部署时打开的开关——
   本仓库**没有任何 `Deliverer` 实现**。表里真有东西时 `Publish` **fail closed
   报错**（而不是半接上就发），错误说清缺的是幂等、重试与库层枚举。
3. **接口层**：`GET /publishing/channels` 逐行给 `can_deliver`，整页给
   `platforms_with_deliverer`；依赖为 nil 时**按不能发处理**（答不上来时说「能发」
   是最坏的一种错）。前端**不写死**这个事实——写死的那一刻，接上投递器的那天
   没人会想起来改它。

---

## 八、变异验证明细（含对照组）

七次变异，全部实测。**每一次都核对了红在哪一行**——红在锚点上而目标断言没跑到
的那种，不算数（下面第 4 条第一次就是这样，已重做）。

| # | 变异（改条件，不删代码） | 期望 | 实测结果 |
|---|---|---|---|
| 1 | `TestNoOutboundDelivererIsRegistered` 里给 `NewService` 传 `{x: &stubDeliverer{}}` | 变红 | ✅ 红在 `deliver_test.go:69`「平台 x 登记了出站投递器」 |
| 2 | `Service.Publish` 里 `Detail: NotDeliveredReason` → `"投递中"` | 变红 | ✅ 红在 `deliver_test.go:118`（detail 逐字断言），锚点全过 |
| 3 | `NotDeliveredReason` 改成「投递中，请稍后查看。」 | 变红 | ✅ 红在 `deliver_test.go:144`（措辞断言） |
| 4 | `channelSetDefinition` 的 `RiskLevel` L1 → L2 | 变红 | ⚠️ **第一次红错了地方**（红在「错误应当说清该填什么」，因为用例没带 reason，L2 先撞上「必须提供 reason」那道闸，根本没走到冻结参数那一步）。给用例补上 reason、并把泄漏断言排到最前，重做后 ✅ 红在 `actions_test.go:337`「被拒绝的明文串被冻进了 1 张审批单的 params」 |
| 5 | httpapi 用例的 `fakeDelivery` 换成 `{PlatformX}` | 变红 | ✅ 红在 `publishing_test.go:150`「can_deliver 为真」，锚点（这一行确实返回了、引用原样回显）全过 |
| 6 | `PublishingPage.tsx` 渠道行条件 `channel.can_deliver ?` → `true ?` | 变红 | ✅ 红在「渠道那一格逐行写『未接投递器』」与「对照组」两条 |
| 7 | `DeliveryGate` 的 `length > 0` → `length >= 0` | 变红 | ✅ 红在「页头常驻门禁」与「对照组」两条 |
| 8 | `navigation.ts` 的 `built` true → false（想验「共享蓝图横幅不出现在本页」那条） | 变红 | ❌ **没红**——`built` 不影响 router.tsx 里的显式路由，页面照常渲染。改成「同时删掉显式路由 + built:false」之后确实红了，但红在**锚点**上（找不到本页自己的门禁横幅），目标那行 `queryByText` 根本没跑到。**这次变异什么都没证明**，结论写进了用例注释，见 §八末尾 |
| 9 | `PlaceholderGate` 的横幅措辞去掉「仅预览、不保存、不发布、不执行」 | 变红 | ✅ 红在第三个断言（拿仍是蓝图的 `/ext/integration` 证明这个查询找得到那句话）。验完**已还原**，`git status` 确认 `PlaceholderPage.tsx` 未被改动 |

**对照组（确认它不是「怎么改都红」）**：

- **不该变红的变异**：`model.go` 的 `maxTitleLen` 200 → 199（一个真实的条件改动，
  与投递无关）。缺席那四条用例**全部保持绿**——✅ 实测。
- **常驻的对照组**：`TestAbsenceAssertionsAreMutationChecked` 与
  `TestPublishingChannelsDeliverabilityIsMutationChecked` 各自带一个
  「不注册投递器时一切照旧 / 依赖为 nil 时 fail closed」的子用例，
  `PublishingPage.test.tsx` 也有一条「同一份夹具不拨开关时仍然是发不出去」。
  把变异做成常驻代码而不是一次性手工操作——手工验过一次，下一个人重写实现时
  不会再验一次。
- 变异 6、7 期间，「后端说能投递时页面改口」那条用例**始终是绿的**，
  说明它测的确实是另一个方向，不是恒真。

**一条如实降级的断言（变异 8 的结论）**：`router.test.tsx` 里「已建成的
`/ext/publishing` 不再挂那条『不发布、不执行』的蓝图横幅」，它的**缺席那一半
结构上不可证伪**——`PublishingPage` 与 `PlaceholderPage` 永远不会同时渲染，
没有任何「改一个条件」的变异能让它在锚点还绿着的时候单独变红。
**没有把这一点含糊过去**：用例注释里写清了这条能证明什么、不能证明什么，
并补了第三个断言——拿一个仍是蓝图的页面（`/ext/integration`）证明这个查询确实
找得到那句话。缺席断言最常见的恒真成因是**查询本身失效**（文案改了、被拆进多个
元素），第三条把它挡住了，变异 9 验证了这一点。

**其它有意义的正向锚点**（本仓库栽过「旧实现下照样绿」四次，逐条问过）：

- 「发布落单」那条同时断言**没有写发布记录**——落单不等于做了；
- 「L1 动作不走审批」那条断言**数据真的改了**（读回库），而不只是「没落单」——
  后者在 Handler 整个失效时也会绿；
- 「暂停/退役的渠道拒绝发布」先证明 **ACTIVE 时发得成**；
- 「素材仍被引用时删不掉」先证明**去掉引用后删得掉**；
- 「缺 publishing.read 时 403」先证明**带着 scope 时 200**（否则 403 可能只是
  路由没挂上）；
- 「未知 ?sub= 不回落」断言页签条**没有渲染出来**，且前面有正向的提示文案锚点。

### 测试期间发现并修掉的一个真 bug

`PgStore.RemoveAsset` 原本只把 `23503`（foreign_key_violation）翻成 `ErrConflict`，
但 **`ON DELETE RESTRICT` 报的是 `23001`（restrict_violation）**。集成用例
`TestAssetStillReferencedCannotBeRemoved` 撞了出来——症状会是「素材仍被引用」
漏成一句原始驱动错误。已改成两个码都认。

---

## 九、files_changed

**新增**

- `db/migrations/000052_content_publishing.{up,down}.sql`
- `internal/platform/publishing/`：`doc.go` / `model.go` / `permissions.go` /
  `deliver.go` / `store.go` / `store_pg.go` / `service.go` / `actions.go`
  + `memstore_test.go` / `deliver_test.go` / `actions_test.go` /
  `store_integration_test.go`
- `internal/platform/httpapi/publishing.go` / `publishing_test.go`
- `contracts/actions/publishing.*.v1.json`（7 个）
- `web/apps/admin-web/src/api/publishing.ts`
- `web/apps/admin-web/src/pages/PublishingPage.tsx` / `PublishingPage.test.tsx`
- `docs/handoffs/slices/XM-EXT-PUBLISHING.md`（本文件）

**修改**

- `docs/architecture/ADMIN-IA.md` —— §5.4 裁定变更（新增 §5.4.1）+ §九 落地记录
- `cmd/platform-api/main.go` —— 注册 7 个 Action、注入只读 Query 与投递器判定
- `internal/platform/httpapi/router.go` —— `Deps.Publishing` / `Deps.PublishingDeliver`
  + 五条只读路由（`publishing.read`）
- `internal/platform/oidcauth/rolemap.go` —— 三个 scope、`content-publisher` 角色、
  `publishing.` 前缀
- `internal/platform/oidcauth/resolver_test.go` —— 钉住「publish 不进 admin、
  content-publisher 持有它、且不带 manage」
- `web/apps/admin-web/src/api/staff.ts` —— `STAFF_ROLE_CATALOG` 补 `content-publisher`
- `web/packages/ui-admin/src/navigation.ts` —— `/ext/publishing` 的 `built` → `true`
- `web/packages/ui-admin/src/navigation.test.ts` —— 占位清单 4→3、已建清单 +1、
  阶段标签样本换成 `/ext/app`
- `web/apps/admin-web/src/router.tsx` —— 显式路由（`built:true` 之后它掉出
  `placeholderRoutes`，不补就 404）
- `web/apps/admin-web/src/router.test.tsx` —— 「未建·后置」4→3、蓝图横幅样本换成
  `/ext/integration`，并**新增一条**「已建成的 /ext/publishing 不再挂那条『不发布、
  不执行』的蓝图横幅」（含它自身局限的说明，见 §八末尾）

**`web/apps/admin-web/src/pages/PlaceholderPage.tsx` 的 `/ext/*` 横幅与占位文案由
team-lead 统一处理，本片未动。** 三个 `/ext/*` 工作树共享这一个文件，各改一遍
合并时必撞；team-lead 会在三片落地后一次性改对（届时只剩 `/ext/ai` 该挂那条横幅）。
本片受影响的两处，供他核对：

- `PlaceholderGate`（约 159–163 行）按 `path.startsWith("/ext/")` **无条件**挂
  「只读蓝图：仅预览、不保存、不发布、不执行」。**这句话对本页今天已经不会出现**
  ——`built:true` 让 `/ext/publishing` 有了自己的路由，根本不经过 `PlaceholderPage`。
  已加用例钉住（`router.test.tsx`「已建成的 /ext/publishing 不再挂那条…」），
  但那条断言的局限也写在注释里了，见 §八末尾；
- `PLACEHOLDER_COPY["/ext/publishing"]`（约 31 行）现在是死条目。

**本片自己那条横幅是本片负责的**：`PublishingPage.tsx` 的 `DeliveryGate` 说的是
「投递器未接」（今天为真、接上后要改），不是「整页不执行」。它有测试 + 两次
变异钉住（变异 6、7）。

**另外没碰**：`blueprints/ext.ts`（蓝图规格作为冻结的设计记录保留，
`blueprints.test.ts` 仍在测它）。

---

## 十、门禁结果（全部自己跑通）

| 门禁 | 结果 |
|---|---|
| `go test -p 1 -count=1 ./...`（八个代理变量全 unset，`XM_TEST_DATABASE_URL` 指向 worktree 专属库） | ✅ 退出码 0，无 FAIL |
| `go vet ./...` | ✅ 退出码 0 |
| `bash scripts/check-governance.sh` | ✅ 退出码 0 |
| `pnpm --config.verify-deps-before-run=false -r run typecheck` | ✅ 5/5 Done |
| `pnpm … -r run test` | ✅ design-tokens 10、ui-primitives 16、ui-admin 261、admin-web 1934，全绿 |
| `pnpm … -r run build` | ✅ 退出码 0；**`web/apps/admin-web build: ✓ 361 modules transformed.`**，产物 `dist/assets/index-BcqtKGvY.js`（1,330.33 kB），且 `grep "未接投递器" dist/assets/*.js` 命中——新页面确实进了产物 |

测试库：`bash scripts/dev/worktree-testdb.sh` 建的 `xm_test_wt_xm_ext_publishing`，
迁移 000052 已应用并验证六张表存在。

**没跑**：真浏览器冒烟（本片没有起 dev server 做人工点击）、生产/预发部署、
`git push`（按派工不推）。

---

## 十一、risks

1. **L3 = 两票且不许自批。** 今天如果只有一个人持 `approval.decide`，发布单会一直
   停在 PENDING 直到过期。这不是缺陷，是 L3 的定义——但它是不是此刻想要的，
   **得产品负责人拍**。退回 L2 只需改一处等级 + 一处契约；用例钉的是行为不是
   字面等级，退回后「发布落审批单」那条会红，那正是它该做的事。
2. **`publishing.publish` 默认没有人持有。** 它只在 `content-publisher` 角色里，
   而那个角色要人显式指派。不指派的话「提交发布」按钮对所有人 403。
   这与 `fund-operator` / `sms-operator` 同一形态，是刻意的，但上线前要有人被指派。
3. **新 schema 的角色授权。** 见 §二末尾——角色拆分落地时 `publishing` schema 的
   default ACL 要一并配，否则会重演开票线 RC101 那种「新表无权限」。
4. **契约与 Go 的等级一致性靠手工。** 仓库没有门禁校验
   `contracts/actions/*.json` 与 `action.Definition` 是否一致（XM-RISK-RESTORE 的
   handoff 也提过）。本片两边是手工对齐的。
5. **页面上的写操作没有真浏览器验证过。** 弹窗里的 `datetime-local` → RFC3339
   转换、Select 的空选项等交互只有 jsdom 用例覆盖。

---

## 十二、follow_ups

1. **第二层：真正的出站投递。** 需要（缺一不可）：X 的 Connector 契约 + 兼容矩阵、
   OAuth 授权流程、速率限制、失败重试与幂等键、一条经 ADR-021 登记的写通道（
   而 ADR-021 今天只覆盖「平台作为客户购买服务的供应商」，X 算不算得先裁定）、
   以及一次放开 `publish_record.result` 枚举的迁移。**别只做一半**：`Service.Publish`
   在发现有投递器但链路没接通时会 fail closed 报错，那是刻意的。
2. **站内公告的架构裁定。** 见 §一。要产品负责人回答两件事：愿不愿意为「平台向
   被管平台写公告」开一条新写通道（改 ADR-018/021），以及愿不愿意让平台持有
   Sub2API 的**整站管理员**凭据（它没有角色细分）。
3. **`action.Schema` 支持字段形状校验（正则/格式）。** 补上之后
   `publishing.channel.set` 可以安全地回到 L2；今天挡住它的是「L2 会在 Handler
   之前冻结参数」这条时序（§4.2）。这条对**所有**带格式约束的 L2+ Action 都有用。
4. **`contracts/actions/*.json` 与 Go 声明的一致性门禁。** 同一个事实钉在两处、
   没有校验，本仓库已经吃过「两处只改一处」的亏。
5. **`dbroles` / `role-policy.v1.json` 的 38 张表补登记**（见 §二）。
   本片的 6 张表清单已备好。
6. **定时投递任务。** 有了投递器之后，「到点自动发」需要一个 River 任务；
   本片的内容日历就地写明了今天没有它。
7. **`PlaceholderPage.tsx` 的 `/ext/*` 横幅与 `PLACEHOLDER_COPY`** —— 归 team-lead，
   三片落地后一次性改。本片不动，也**不建议别的片顺手改**：三个工作树共享它。
8. **发布记录与审批单的关联**目前要经审计链绕一跳（§二）。若日后觉得值得，
   可以在内核给 Handler 的 context 里注入 approvalID——那是一次共享热点文件的
   改动，值不值得由内核那条线定。
