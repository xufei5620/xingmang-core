# Platform User Read Contract v2 设计

> 状态：DESIGN / 待产品与安全审批。本文只冻结边界和后续实施顺序，不授权
> Sub2API、NewAPI、reqlog、invoice 或 payment 的 real 实现，不授权新增权限，
> 不授权迁移、Action、生产联调、推送或部署。
>
> 任务：XM-C-USER0
>
> 依据：PROJECT-CONSTITUTION 第 2、5、7、12、13、14、18、20 条，
> ADR-004、ADR-006、ADR-018，platformusers v1 DRAFT，XM-B001 brief/report，
> reqlog v1 DRAFT，invoice v1 DRAFT，CR-0002 与已批准产品方向 CR-0003。

## 0. 独立审批事件

审批不传递、不合并，后序任务必须引用对应审批事件及其证据哈希：

| 事件 | 授权范围 | 明确不授权 |
|---|---|---|
| CORE_APPROVAL | Task 1~3：UserRef/codec、v2 Fake、Fake-only GetUser Query/UI；并批准 platform.users.read 扩大到 GetUser 基础事实 | 任何 Sub2/New/reqlog real、DailyUsage、Key scope、外部实例访问 |
| SUB2_REAL_APPROVAL | Task 4；产品+安全审阅 Sub2 脱敏样本、版本、只读路径和证据文件后单独批准 | NewAPI、reqlog、DailyUsage、Key、invoice/payment |
| NEWAPI_REAL_APPROVAL | Task 5；产品+安全审阅 NewAPI 脱敏样本、版本、GET 写黑名单和证据文件后单独批准 | Sub2、reqlog、DailyUsage、Key、invoice/payment |
| DAILY_USAGE_APPROVAL | Task 6 的 Fake/core capability 与 platform.users.read 数据面 | 任一 real DailyUsage；real capability 必须在对应 real Reader 同一 PR 再批 |
| KEY_SCOPE_APPROVAL | Task 7：新 scope platform.user_keys.read、角色映射与 Fake/core Query | 任一 real Key reader；真实样本批准另随对应 real PR |
| REQLOG_USERREF_APPROVAL | Task 8；审批项 4 明确批准 reqlog stable UserRef，并引用真实 API/源码、脱敏 fixture 与 retention/cursor 证据 | invoice/payment、本地 link 表 |

CORE_APPROVAL 通过只代表 core/Fake 可以实施，绝不构成 SUB2_REAL_APPROVAL 或
NEWAPI_REAL_APPROVAL。真实样本存在也不等于批准 real；批准文本必须明确写出事件名、
目标平台、证据路径/哈希和允许的任务号。CR-0002/3 与 M3 继续是外部独立门。

## 1. 要解决的问题

XM-B001 已经交付了诚实的用户详情壳，但真实数据仍只有 platformusers v1
列表 Query。详情页只能通过 q=userId、每页 200、最多 5 页的客户端精确扫描
取得基础用户；以下区域仍然没有可信数据源：

- Sub2API：近 7 天消费趋势、消费明细、充值记录、开票记录、API Key 元数据；
- NewAPI：按稳定平台用户 ID 过滤的区间请求；
- 两个平台：稳定、可直接判定 Not Found 的 GetUser。

现有数据域不能被揉成一个 Connector：

- platformusers 拥有被管平台原生用户、余额与平台原生 Key 元数据；
- reqlog 拥有逐请求元数据与高敏正文，正文另有 scope 和审计；
- payment 将拥有原始充值订单，当前仍是 M3 门控；
- invoice 拥有开票申请，CR-0002 尚未冻结，CR-0003 的平台隔离尚未实施。

因此 v2 的目标不是“让一个 Connector 返回整页”，而是建立稳定 UserRef 和
GetUser 核心，再让各领域以自己的 Query、权限、新鲜度、保留期和分页独立供数。

## 2. 方案比较与裁定

### 方案 A：platformusers 聚合全部领域

让 platformusers 同时调用 reqlog、invoice、finance/payment，并返回一个大 DTO。

否决原因：

- 破坏 ADR-006 的领域分治；
- 一个 scope 会顺带获得请求轨迹、充值订单、Key 清单与开票记录；
- 多个来源的水位、保留期、分页和错误被压成一个看似完整的响应；
- 任一外部域失败都会把基础用户详情拖成失败。

### 方案 B：稳定 UserRef + platformusers 核心 + 领域独立 Query

platformusers v2 只提供精确用户、每日消费序列和平台原生 Key 元数据；
reqlog、payment、invoice 保持独立，只接受同一结构化 UserRef 作为精确收窄条件。

裁定：采用。

### 方案 C：平台本地 link 表统一映射

平台落一张跨域映射表，把第三方记录映射到用户。

暂不采用。它需要迁移、写入口、有效期、来源证据、漂移检测和修复流程。真实来源
能原生提供稳定 user_id 时，本地映射只会制造第二份身份真相。只有来源确实无法
携带 UserRef、且产品批准人工维护时，才单独设计迁移和 Action。

## 3. 稳定身份：UserRef

内部契约统一使用结构化身份，不拼接字符串：

~~~go
type UserRef struct {
    Platform string // sub2api | newapi
    ID       string // 上游不透明、非空、原始用户 ID
}
~~~

判据：

1. 唯一性域是 environment + platform + ID；同 ID 跨平台不是同一用户。
2. 同邮箱、同 username、相同 token prefix 永远不能自动关联。
3. CR-0003 明确同邮箱在 Sub2API 与 NewAPI 是两个用户集合，不合并。
4. 领域记录无法证明 UserRef 时，返回未关联覆盖信息，不猜、不回落、不展示为该用户。
5. 过滤必须在来源侧分页和统计之前生效。拉全域一页后在平台侧按用户过滤，会让
   next_cursor、总数和统计全部失真。

跨域来源允许的关联证据只有：

- 原始记录直接携带 platform + source user ID；
- 来源系统自己的受信绑定表能按完整凭据或原生主键得到精确 user ID；
- invoice 按 CR-0003 实施后的 platform-scoped binding。

token prefix 只保留为人工追查证据；它不是唯一键，也不是关联证据。

## 4. canonical 路径 codec

复用 XM-B001 已冻结的 canonical 形状：

~~~text
segment = "u-" + lowercase_hex(UTF-8(user_id))
~~~

平台 HTTP 继续使用 API v1 命名空间，新增资源：

~~~text
GET /api/v1/platforms/{platform}/users/{canonicalUserId}
~~~

HTTP API 的 v1 与 Connector contract v2 是两套版本域；新增资源不要求把整个
平台 HTTP API 升为 /api/v2。

TS 与 Go 必须共享一份 golden fixture。严格要求：

- ID 非空，UTF-8 无损；
- 编码结果只有 u-[0-9a-f]+；
- 解码后字节长度最大 512；
- 解码后再编码必须与输入 segment 逐字相等；
- raw ID、空 payload、奇数 hex、大小写 hex、非法 UTF-8 全部拒绝；
- 点段、斜杠、查询符号、百分号、空格和 Unicode 均无损；
- 路由在调用 Service/Connector 前拒绝非法 segment。

## 5. platformusers v2 契约边界

v1 的 ListUsers 保留兼容；v2 在同一 Go 包中新增 capability-specific interface，
不扩大 v1 ReadClient 的方法集合。能力标识可以先在契约文档中保留名字，但任何
client 的实际 capability 列表只能在它实现对应 Reader 的同一 PR 中增加，并由
contracttest 同时证明；禁止先宣称 capability、以后再补实现。

### 5.1 核心 GetUser

~~~go
type UserDetailReader interface {
    GetUser(context.Context, GetUserQuery) (UserDetail, error)
}

type GetUserQuery struct {
    Ref         UserRef
    Day         string
    Granularity Granularity
}

type EvidenceSnapshot struct {
    ObservedAt time.Time
    Source     string
    Watermark  string
    IsPartial  bool
}

type UserDetail struct {
    Ref          UserRef
    User         User
    RegisteredAt time.Time // zero = unknown
    Period       Period
    Snapshot     EvidenceSnapshot
    Capabilities []registry.Capability
}
~~~

GetUser 的规则：

- 原生精确端点优先；
- 若只能扫描列表，必须完整耗尽才能返回 ErrNotFound；
- 达到请求/页数/游标循环上限返回 ErrLookupIncomplete；
- ErrNotFound 冻结映射为 Action CodeNotRegistered、HTTP 404、JSON error.code
  NOT_REGISTERED；
- ErrLookupIncomplete 冻结映射为 Action CodeExecutionFailed、HTTP 502、JSON
  error.code EXECUTION_FAILED，安全消息固定为“用户精确查找未完成，请重试”；
  前端按 5xx 显示可重试错误，绝不能映射成 404；
- context.Canceled / DeadlineExceeded 必须原样传播到调用链，不能转换成 Not Found；
- 不认识的平台或无 detail capability 返回 NOT_REGISTERED/501，不返回空用户；
- 不把 CustomerType 放进最小结构。Sub2API/NewAPI 对“客户类型”的语义尚未冻结，
  在真实样本与产品定义到位前继续 unavailable。

保留的能力标识：

~~~text
platformusers.user.detail_read
platformusers.user.daily_usage_read
platformusers.user.keys_metadata_read
~~~

每个平台只声明真实支持的子集；NewAPI 不继承 Sub2API 的布局或能力。detail
标识在 Fake UserDetailReader 的 PR 中首次声明；Sub2/New real 只有在各自
UserDetailReader 与 contracttest 同片完成后才声明。daily/key 同理。

### 5.2 DailyUsageSeries

~~~go
type DailyUsageReader interface {
    DailyUsage(context.Context, DailyUsageQuery) (DailyUsageSeries, error)
}

type DailyUsageQuery struct {
    Ref  UserRef
    Day  string // 空 = 服务端按 CST +08:00 解释今天
    Days int    // 默认 7，允许 1..31
}

type DailyUsagePoint struct {
    Day       string
    Consumed  Amount
    Requests  CountValue
}

type SeriesCoverage struct {
    ExpectedDays int
    CoveredDays  int
    Complete     bool
}

type DailyUsageSeries struct {
    From, To string // CST 业务日闭区间
    Points   []DailyUsagePoint
    Coverage SeriesCoverage
    Snapshot EvidenceSnapshot
}
~~~

规则：

- From..To 每个业务日恰好一项并按日期升序；
- Days=0 使用默认 7；仅接受 1..31，负数或大于 31 当场拒绝；
- 空 Day 的“今天”按固定 CST +08:00 服务端时钟解释；
- From 是 To 向前包含当天的 Days 个日历日，允许跨月/跨年；
- 已证明无消费 = KnownAmount(0)，没有读到 = UnknownAmount；
- CoveredDays 只计 Consumed/Requests 均有证据的日期；Complete 当且仅当
  CoveredDays==ExpectedDays 且 Snapshot.IsPartial=false；
- 缺一天必须 Complete=false，不能画成完整折线；
- 不跨币种求和，不经 float；
- 真实来源只给区间合计而不给逐日时，保持 capability 不支持。

### 5.3 KeyMetadata

~~~go
type KeyMetadataReader interface {
    ListKeyMetadata(context.Context, KeyMetadataQuery) (KeyMetadataPage, error)
}

type KeyMetadataQuery struct {
    Ref    UserRef
    Limit  int
    Cursor string
}

type KeyMetadata struct {
    ID           string
    Prefix       string
    Status       string
    CreatedAt    time.Time
    LastUsedAt   time.Time
    TodayPeakRPM CountValue
}

type KeyMetadataPage struct {
    Items       []KeyMetadata
    NextCursor  string
    Snapshot    EvidenceSnapshot
}
~~~

安全规则：

- Prefix 最多 8 个 Unicode code point；
- 禁止 full key、可调用 token、token hash、secret material；
- ID 是非敏感、不透明的记录 ID，不得可逆到完整 Key；
- 未知状态 fail closed 为 unknown；
- LastUsedAt、CreatedAt 零值明确显示未知；
- 页大小默认 50、最大 200，游标不透明并绑定 Ref 与过滤条件。

完整 Key 元数据列表扩大了凭据库存可见面，必须获得新 scope 审批后才能注册路由。

## 6. 各领域独立 Query

### 6.1 reqlog：逐用户 usage/request 元数据

reqlog 继续拥有请求元数据与正文。它必须先在真实抄录或受信 token 映射阶段写入
稳定 UserRef，并新增精确 user filter。当前 Username + TokenPrefix 不合格。

~~~go
type RequestLogSummary struct {
    User *UserRef // nil = 未关联；不能由 username/prefix 推导
    // existing fields remain
}

type ListFilter struct {
    User *UserRef // source 与 User.Platform 必须相等
    // existing filters remain
}
~~~

- 元数据仍要求 request.read；
- 正文仍要求 request.content.read，成功读取必须先写审计；
- 30 天 retention 当前仍是 DRAFT 假设，真实接入前必须核对；
- 过保留期或未关联必须在 coverage/retention 文案中可见。

### 6.2 payment：充值记录

充值订单属于 Payment 真相源，不进入 platformusers。payment Connector 当前目录为空，
受 M3 门控。未来必须提供按 UserRef 精确过滤、订单号/金额/方式/状态/时间的 PII-free
投影，并使用独立 payment.read scope。平台不得从余额差分推导充值。

### 6.3 invoice：开票记录

invoice 继续独立。CR-0002 尚未冻结；CR-0003 已批准平台隔离方向但未实施。
只读 DTO 必须新增 platform + source_user_id 和精确过滤，同时继续完全排除抬头、
税号、银行账号、地址、电话、邮箱等 PII。该变化必须回写 CR-0002/CR-0003 并由
开票线确认；平台仓库不能自行冻结。

## 7. 分页、保留期、新鲜度与覆盖率

每个 section 独立返回证据，页面不能用一个“数据新鲜”徽章覆盖所有域：

| 区域 | 分页 | 保留期 | 新鲜度/覆盖率 |
|---|---|---|---|
| GetUser | 无 | 不适用 | Snapshot；partial 时基础事实不可冒充完整 |
| DailyUsage | 最大 31 日，无游标 | 来源声明 | expected/covered days + Snapshot |
| Key metadata | keyset cursor，50/200 | 来源声明或 unknown | page Snapshot |
| reqlog | 自己的不透明 cursor | 当前 DRAFT=30 天 | retention + Snapshot.IsPartial |
| payment | 由 M3 契约冻结 | 未知 | 独立 Snapshot/coverage |
| invoice | keyset cursor | 未冻结 | invoice Snapshot/watermark |

统一纪律：

- 空 cursor = exhausted；失效 cursor 报错，不从头静默重来；
- cursor 必须绑定 UserRef 与过滤条件；
- offset 只能在契约明确降级并标 partial 时使用；
- page-level Snapshot 即使 items 为空也必须存在；
- 未提供 retention 不能显示 0 天或无限保留，只能显示 unknown；
- 任一 Fake section 出现时必须显示 Fake 证据，不能被其他真实 section 盖掉。

## 8. PII、错误与日志

- platformusers 只返回 masked email；明文在 Connector 内立即打码；
- reqlog usage 元数据不含 request/response body；
- invoice 类型层面不含任何 PII；
- payment 不返回 payer email、手机号、完整支付凭据或不需要的 trade metadata；
- Key metadata 永不返回完整 Key；
- 上游原始错误正文不进对外 Message、测试快照或日志；
- HTTP access log 只看到 canonical hex segment，不主动记录解码后的用户 ID；
- 反射/JSON 键扫描必须覆盖 email、phone、tax、bank、address、secret、token、
  credential、full_key 等禁词，并对明确允许的 email_masked/token_prefix 使用
  精确白名单。
- Key metadata 还必须扫描最终 HTTP JSON 的键和值：禁止 full_key/secret/
  credential/token_hash 等键；禁止响应值包含 fixture 中的完整 Key sentinel、
  email、CredentialRef 或其他凭据片段。只做 Go struct 反射不算通过。

## 9. Scope 与审批

| 数据 | Scope | 当前裁定 |
|---|---|---|
| GetUser 基础事实、DailyUsage 聚合 | platform.users.read | 复用会扩大既有 scope 数据面，实施前需产品+安全显式批准 |
| Key metadata 全列表 | platform.user_keys.read | 新 scope，需产品+安全批准并更新角色映射；未批不得注册 |
| reqlog 元数据 | request.read | 保持 |
| reqlog 正文 | request.content.read | 保持；成功披露审计 fail closed |
| payment 充值 | payment.read | M3 设计时审批 |
| invoice 记录 | invoice.read | CR-0002/CR-0003 冻结时审批 |

本设计全为 Query，不需要 Action。新 scope、默认角色映射、开发默认身份都不是本文
自动授权的内容。AI 不参与审批。

## 10. 真实样本硬门

real 代码开始前必须把脱敏证据回写 docs/evidence，并提供可提交的 synthetic/redacted
fixture；不得把真实用户、邮箱、token、订单号或对话带入仓库。

### Sub2API

- 实例版本与兼容矩阵；
- /api/v1/admin/users 的精确字段、分页、状态、金额单位、created/last-active；
- 真正查询存储的 daily usage 证据；
- Key metadata 与 user ID 的原生关联；
- 人工完成 AdminComplianceGuard 确认，平台不得发 POST；
- /api/v1/admin/users/:id/usage 已由源码普查证明是写死 mock，永久加入禁用测试，
  即使它返回 HTTP 200 也不得作为数据源。

### NewAPI

- /api/user/ 的真实脱敏响应与软删除形状；
- topup、log/data、token metadata 是否携带同一稳定 user_id；
- quota_per_unit、金额 scale、provider 分桶；
- GET 写端点黑名单继续生效；
- 管理员 token 只经 CredentialRef，错误正文零透传。

### reqlog

- 真实 API/源码；
- token 映射能否给出 platform + source user ID，而非只给 username；
- retention、cursor、watermark、partial 与统计语义；
- 未关联记录的计数和处置。

### invoice/payment

- invoice 等 CR-0002/3 双方确认与只读投影实施；
- payment 等 M3 Connector、UserRef 字段和 scope 设计；
- 两者未过门时 UI 保持 unavailable，不用其他域的记录顶替。

## 11. 默认不建本地 link 表

最小 v2 不新增数据库表、不落用户明细、不缓存跨域流水。若后续证明确实无法由来源
给出 UserRef，须另立设计，至少包含：

- environment/platform/user/domain/external_subject 复合唯一键；
- association method、evidence hash、observed_at、valid_from/valid_to；
- 冲突/失效/漂移状态；
- 所有写入经 Action 或获批 Platform Lifecycle Operation；
- 迁移、回滚、人工纠错与审计。

没有上述批准时，任何本地 join 表都属于越界。

## 12. 八个 worktree / PR 分片

| Task | 分支 | 基线依赖 | 独立门 | real 阻断时 |
|---|---|---|---|---|
| 1 | ai/codex/XM-C-USER1-userref-codec | 含 B001/C002 的最新 release | CORE_APPROVAL | 可实施 |
| 2 | ai/codex/XM-C-USER2-user-v2-fake | Task 1 已合入 release | CORE_APPROVAL | 可实施 |
| 3 | ai/codex/XM-C-USER3-get-user-query | Task 2 已合入 release | CORE_APPROVAL | 可实施，保持 Fake/real unavailable |
| 4 | ai/codex/XM-C-USER4-sub2-user-real | Task 3 已合入 release | SUB2_REAL_APPROVAL + Sub2 证据 | 阻断，不影响 1~3/5~8 |
| 5 | ai/codex/XM-C-USER5-newapi-user-real | Task 3 已合入 release | NEWAPI_REAL_APPROVAL + NewAPI 证据 | 阻断，不影响 1~4/6~8 |
| 6 | ai/codex/XM-C-USER6-daily-usage | Task 3 已合入 release | DAILY_USAGE_APPROVAL | Fake/core 可实施；不引用 Task 4/5 文件 |
| 7 | ai/codex/XM-C-USER7-key-metadata | Task 3 已合入 release | KEY_SCOPE_APPROVAL | Fake/core 可实施；不引用 Task 4/5 文件 |
| 8 | ai/codex/XM-C-USER8-reqlog-userref | Task 3 与 C002 已合入 release | REQLOG_USERREF_APPROVAL + reqlog 证据 | 阻断，不影响 1~7 |

每个 Task 必须从表中指定的已合入 release 新建独立 worktree/PR，不在未合并前序分支
上堆叠。Task 4 与 5 彼此独立；Task 6/7 只实现 contract/Fake/HTTP/UI，real
DailyUsage/Key reader 必须在对应平台后续独立授权 PR 中实现和声明 capability。

payment/recharge 与 invoice 是独立领域，另立后续规格与计划；它们不是第 8 片中
可以顺手实现的附属功能。

## 13. TDD 总矩阵

- TS/Go codec 对同一 golden 全量往返和拒绝；
- GetUser exact/fuzzy 冲突、第二页命中、分页完整耗尽、页上限仍有 cursor、
  cursor 循环、context cancel/deadline；并逐字断言 404/NOT_REGISTERED 与
  502/EXECUTION_FAILED 映射；
- 同邮箱/username/prefix 跨平台绝不关联；
- UserRef 在过滤、cursor、统计前生效；
- DailyUsage 已知零、未知、缺日、跨月、CST、窗口上限；
- Key prefix/code-point 上限、无 full key、未知状态、分页稳定；
- 每个 section 的 retention/freshness/coverage 独立；
- 金额十进制整数字符串、未知与零、跨币种拒绝；
- PII/secret 反射与 JSON 键扫描；
- capability 差异：NewAPI 不继承 Sub2API；
- scope 403；正文审计 fail closed；
- mock route 即使返回 200 也被拒；
- real redacted fixtures 通过共享 contracttest。

## 14. 验收与非目标

审批规格验收：

- 本文与实施计划覆盖上述边界、硬门、八分片和文件；
- 没有 real 实现授权；
- 没有用户名/邮箱/prefix join；
- 没有本地 link 表默认；
- 没有 migration、Action、生产联调或外部写入。

实现后的最终验收另行执行，不属于 XM-C-USER0：

- contracttest、Go/TS 全量门禁、Storybook、治理检查；
- Sub2API/NewAPI 脱敏真实 fixture 和兼容矩阵；
- reqlog/invoice/payment 各自审批与契约门；
- 浏览器 1440/1024/800/390，逐 section 来源、权限、retention、coverage 与 Fake 证据。

## 15. 待审批清单

1. CORE_APPROVAL：是否允许 platform.users.read 扩大到 GetUser 基础事实，并批准
   Task 1~3；该事件明确不授权 real；
2. SUB2_REAL_APPROVAL：是否在审阅对应 Sub2 真实样本证据后批准 Task 4；
3. NEWAPI_REAL_APPROVAL：是否在审阅对应 NewAPI 真实样本证据后批准 Task 5；
4. DAILY_USAGE_APPROVAL：是否允许 platform.users.read 扩大到 DailyUsage Fake/core；
5. KEY_SCOPE_APPROVAL：是否批准 platform.user_keys.read 及角色映射；
6. REQLOG_USERREF_APPROVAL：是否批准 reqlog stable UserRef，且真实证据是否过门；
7. invoice 关联字段由 CR-0002/3 单独确认；
8. payment/recharge 随 M3 单独确认。

未取得 CORE_APPROVAL 前只允许继续文档和脱敏证据采集；取得 CORE_APPROVAL 后也只能
实施 Task 1~3。其余 Task 各自等待表中独立事件，任何批准不得被推定或继承。
