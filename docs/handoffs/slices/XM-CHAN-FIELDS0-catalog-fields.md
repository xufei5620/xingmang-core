# XM-CHAN-FIELDS0 · 渠道目录字段扩展

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-CHAN-FIELDS0-catalog-fields`
- worktree: `K:/星芒统一控制平台/wt-xmCHANFIELDS0`
- base: `04e5adf`（`release/v0.1-launch`）
- implementation head: `174b6e9`

## summary

产品负责人（2026-09-02）要求合并渠道管理表按 Sub2API 账号 / NewAPI 渠道给出：ID、名称、
平台/类型、容量/并发、状态、调度、今日统计、用量窗口、代理、倍率、上游倍率、最近使用、
创建时间、过期时间。本片只做后端 + 契约：扩展两个连接器的渠道目录类型
（`sub2api.ManagedChannel` / `newapi.ChannelStatus`）、真实与 Fake 客户端、契约测试，以及
`/api/v1/platforms/{platform}/channels` 的响应 DTO，供并行片 XM-CHAN-MERGE0（agent
chanmerge）接入合并表。前端一个字节都没碰。

**核心方法**：每个新字段先在 `K:/sub2api-src`/`K:/newapi-src` 里逐行核对来源（结构体字段 +
文件行号），能追到就实现，追不到就在契约文档里写明原因、永远返回 `null`，不做近似。字段
经由既有的 Connector→Observation→httpapi 三层管道传递：连接器把新字段编码进
`ToChannelDirectoryObservation`/`channelsObservation` 已有的逐渠道嵌套 map（与旧字段
`channel_id`/`name`/`balance_minor_units` 同一条管道，不是新通道），httpapi 侧新增
`catalogRowsForService`/`applyCatalog` 解码同一份 observation 并按任务指定的确切 JSON
键名拼进 `platformChannelRow`。

### 两个平台的字段覆盖差异（详见两份契约文档）

Sub2API 几乎全部字段都能从已有的 `/api/v1/admin/accounts` 列表响应
（`AccountWithConcurrency`）里拿到，无需额外请求；只有 `today`（今日统计）需要预算内逐账号
再打一次 GET `/api/v1/admin/accounts/:id/today-stats`——**不是**上游自己提供的批量端点
`POST .../today-stats/batch`，因为只读传输层（`connector.ReadOnlyTransport`）在方法层面
硬性只放行 GET/HEAD（ADR-018 闸 2/4），任何 POST 在发出前就被拒绝，这条批量端点在当前只读
连接器架构下**天然走不通**，不是漏接。预算 `maxTodayStatsAccounts = 40`。

NewAPI 这一侧真实能落地的新字段少得多：`vendor`（从 `Type` 查表得出，表原样抄自上游自己的
`ChannelTypeNames`）、`status`（`Status` 枚举转字符串标签）、`scheduling.priority`、
`today`（预算内 GET `/api/log/stat`，业务日窗口，与既有错误率的滚动 24 小时窗口是两个不同
的问题不能共用一次调用）、`proxy`（**脱敏**——上游把它存成完整代理 URL 且从不剥离内嵌凭据，
本片解析后只保留 `scheme://host:port`）、`created_at`。`kind`/`capacity`/`usage_window`/
`rate_multiplier`/`upstream_multiplier`/`last_used_at`/`expires_at` 在 NewAPI 侧**恒为
null**——逐项核对过源码，确实没有可达字段支撑，每一项契约文档里都写了具体原因（比如 `kind`
唯一的线索是 Codex 渠道 OAuth 凭据，编码在被列表/详情端点排除的 `key` 字段里，读不到）。

### 比率/倍率的数值编码

`success_rate`、`used_ratio`、`rate_multiplier`、`upstream_multiplier` 对外都是普通 JSON
小数（如 `0.42`），但内部一律按 **ppm（百万分之一）整数**计算与传递，只在写进 observation
map 那一步做唯一一次、不参与后续任何累加/比较的终值换算（`ppmToFraction`）——与本仓库既有
的 `ErrorRatePPM` 同一条纪律（宪法 13 条"比例使用 Decimal"）。这不是我随口选的：NewAPI 侧
的 `contracttest` 已经有一条反射测试（`assertNoFloatFields`）专门挡"以后有人图省事加一个
`float64` 字段"，我最初按纯 `float64` 实现时**这条测试竟然没拦住**——因为它只检查
`reflect.Kind()`，`*float64` 的 Kind 是 `Ptr` 不是 `Float64`，会漏网。本片顺手把这个真实
漏洞也修了（先解引用指针再判 Kind），修完之后重新按 ppm 设计并验证了新字段不会被这条已修复
的检查拦下。`today.cost_minor` 沿用既有的金额十进制字符串惯例（`amountString`），不是裸
JSON 数字。

## files_changed

### Sub2API

- `connectors/sub2api/channel_directory.go`：`ManagedChannel` 新增 18 个可空字段
  （Kind/Vendor/CapacityUsed/CapacityLimit/SchedulingEnabled/SchedulingPriority/
  TodayRequests/TodaySuccessRatePPM/TodayCostMinorUnits/TodayCurrency/TodayScale/
  UsageWindowUsedRatioPPM/UsageWindowResetsAt/ProxyLabel/RateMultiplierPPM/
  UpstreamMultiplierPPM/LastUsedAt/CreatedAt/ExpiresAt）+ `classifyAccountKind` +
  `catalogFields()`/`ppmToFraction` 编码
- `connectors/sub2api/upstream.go`：`accountItem` 解码扩展、容量/调度/倍率/代理/时间戳
  映射、`fetchAccountTodayStats`/`fetchOneAccountTodayStats`（预算内 GET）、
  `parseScaledAmount`/`costRatioPPM`/`upstreamMultiplierPPMFromExtra`/`optionalString` 辅助函数
- `connectors/sub2api/fake.go`：`applyFakeCatalogFields`（3 条固定渠道覆盖
  subscription/upstream 两种 kind、有/无 proxy、有/无 upstream_multiplier 等组合）
- `connectors/sub2api/channel_directory_test.go`：`classifyAccountKind`/`catalogFields`
  白盒测试
- `connectors/sub2api/client_contract_test.go`：`fakeAccountItems` 扩展三条覆盖全部新
  字段 + 边界、新增 `/api/v1/admin/accounts/{id}/today-stats` 假端点、
  `TestRealClientMapsChannelCatalogFields`（逐字段核对）、
  `TestRealClientDegradesTodayStatsWhenUnsupported`、
  `TestFakeChannelCatalogFieldsSatisfyInvariants`
- `connectors/sub2api/contracttest/suite.go`：`AssertManagedChannelCatalogInvariants`

### NewAPI

- `connectors/newapi/contract.go`：`ChannelStatus` 新增 17 个可空字段（含上述提到的 7 个
  恒为 nil 的维度，仍声明字段以保持契约形状一致并防止未来实现悄悄开浮点后门）+
  `newapiVendorNames`（抄自上游 `ChannelTypeNames`）+ `channelStatusEnumNames` +
  `catalogFields()`/`ppmToFraction`
- `connectors/newapi/upstream.go`：`channelItem` 扩展（Priority/CreatedTime/Setting）、
  `parseChannelProxyLabel`（代理 URL 脱敏）、vendor/status/priority/proxy/created_at 映射、
  `fetchChannelTodayStats`/`logStat`/`businessDayTodayWindow`（预算内 GET /api/log/stat）
- `connectors/newapi/fake.go`：`applyFakeCatalogFields`（6 条固定渠道全部覆盖 vendor/
  status/priority/today，1 条额外带 proxy）
- `connectors/newapi/client_contract_test.go`：`fakeChannelItems` 追加 created_time/
  setting（含"有代理但需脱敏"与"proxy 是空字符串"两种边界）、新增
  `/api/log/stat` 假端点（`upstreamLogsStat`）、`TestRealClientMapsChannelCatalogFields`、
  `TestRealClientDegradesTodayStatsWhenLogStatUnsupported`、
  `TestFakeChannelCatalogFieldsSatisfyInvariants`
- `connectors/newapi/contracttest/suite.go`：`assertNoFloatFields` 指针解引用修复 +
  `AssertChannelStatusCatalogInvariants`

### httpapi

- `internal/platform/httpapi/channel_bindings.go`：抽出 `findChannelsObservation`
  共享辅助函数（`inventoryForService` 的行为不变，只是重构，供本片新函数复用同一次查找）
- `internal/platform/httpapi/platform_channels.go`：新增
  `platformChannelCapacityResponse`/`SchedulingResponse`/`TodayResponse`/
  `UsageWindowResponse`、`platformChannelRow` 新增 `id`/`kind`/`vendor`/`capacity`/
  `status`/`scheduling`/`today`/`usage_window`/`proxy`/`rate_multiplier`/
  `upstream_multiplier`/`last_used_at`/`created_at`/`expires_at`、
  `catalogRowsForService`/`applyCatalog`/`toInt64FromAny` 等类型安全解码辅助
- `internal/platform/httpapi/platform_channels_test.go`（新增）：字段级往返测试（含
  `cost_minor` 十进制字符串断言）、"完全没有目录字段时应全部为 null"、int64/float64
  双形态防御测试

### 契约文档

- `contracts/connectors/sub2api.channel-catalog.v3.md`（新增）
- `contracts/connectors/newapi.channel-catalog.v3.md`（新增）

## 字段 → 上游来源速查（完整版见两份契约文档）

| 字段 | Sub2API | NewAPI |
|---|---|---|
| `kind` | 由 `Type` 分桶（oauth/setup-token→subscription；apikey/upstream/bedrock/service_account→upstream） | 恒 null（唯一线索在被排除的 `key` 字段里） |
| `vendor` | `Platform` 原样透传 | 由 `Type` 查表（抄自上游 `ChannelTypeNames`） |
| `capacity` | `current_concurrency`（used）/ `concurrency` 或 `load_factor`（limit） | 恒 null（无渠道级并发上限字段） |
| `status` | `Status`（active/disabled/error） | 由 `Status` 枚举转字符串（enabled/manually_disabled/auto_disabled/unknown） |
| `scheduling` | `Schedulable`/`Priority`（数值越小优先级越高） | `Enabled`/`Priority`（数值越大优先级越高，方向相反） |
| `today` | 预算内 GET `/accounts/:id/today-stats`（WindowStats.Cost，账号口径已含自身倍率） | 预算内 2×GET `/api/log/stat`（业务日窗口） |
| `usage_window` | 仅 subscription 账号：当前窗口费用/费用上限（不是 Anthropic 原生 5 小时用量%） | 恒 null |
| `proxy` | `Proxy.Name`（人工标签，上游本就不含凭据） | 从 `Setting.proxy` 解析后脱敏（剥离 userinfo） |
| `rate_multiplier` | `RateMultiplier` | 恒 null |
| `upstream_multiplier` | `extra.upstream_billing_probe.data.resolved_rate_multiplier`（多数账号为 null，仅开启过该功能的账号有值） | 恒 null |
| `last_used_at` | `LastUsedAt` | 恒 null |
| `created_at` | `CreatedAt` | `CreatedTime` |
| `expires_at` | `ExpiresAt`（上游是 unix 秒，已换算） | 恒 null |

## tests_run

- `go build ./...`：PASS（全仓，无输出）
- `go vet ./...`：PASS（全仓，无输出）
- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy
  -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...`：PASS，全仓 66 个包（含 `[no test
  files]` 的包）全部 `ok`，0 `FAIL`，无 skip（本片未涉及数据库，未新建迁移，不需要
  `XM_TEST_DATABASE_URL`）
- `go fmt ./connectors/sub2api/... ./connectors/newapi/... ./internal/platform/httpapi/...`：
  已应用（scoped，未跑裸 `gofmt`）
- `bash scripts/check-governance.sh`：PASS（exit 0，无输出）
- `gitleaks detect --no-git`：对本片实际改动的每个目录
  （`connectors/sub2api`、`connectors/newapi`、`internal/platform/httpapi`、
  `contracts/connectors`）分别扫描，均 `no leaks found`
- 额外针对新增测试单独跑过（`-run` 精确匹配）确认非陈旧结果：
  `TestRealClientMapsChannelCatalogFields`、`TestRealClientDegradesTodayStatsWhenUnsupported`
  / `...WhenLogStatUnsupported`、`TestFakeChannelCatalogFieldsSatisfyInvariants`、
  `TestPlatformChannelsQueryExposesV3CatalogFields`、
  `TestPlatformChannelsQueryLeavesCatalogFieldsNilWhenAbsent`、
  `TestCatalogRowsForServiceCoercesBothNumericForms`（逐条 PASS）

## tests_not_run

- 未对真实 Sub2API/NewAPI 实例验证过——与两个连接器既有的免责声明同一条：只有本地假上游的
  httptest 契约测试覆盖，真实凭据到位后需按两份契约文档"Follow-ups"一节逐项核对。
- 未做前端/浏览器验收——本片不改 `web/`，交由 XM-CHAN-MERGE0 消费。
- 未跑数据库集成测试——本片未新增表/迁移/查询，`XM_TEST_DATABASE_URL` 相关测试路径未触碰
  （既有集成测试仍按各自既定条件 skip，不是本片改动的结果）。
- 未验证 `today-stats` 预算（40 账号/渠道）在真实大规模账号数下的实际表现——本地假上游只有
  个位数账号，预算触发路径（`maxTodayStatsAccounts`/`maxTodayStatsChannels` 生效后
  `CoveragePartial=true`）靠代码走查与"路由缺失时同样走这条降级分支"的测试间接验证，未构造
  真正超过 40 条的假账号列表。

## risks / follow_ups

- **Sub2API today-stats 预算限制**：只读传输层禁止 POST，批量端点用不了，只能预算内
  （40 账号/次读取）逐个打 GET；账号数超过预算时大多数行的 `today` 会是 `null`。真实账号
  规模摸清楚之前无法判断这个限制的实际影响面，两份契约文档"Follow-ups"都记了这条。
- **`usage_window.used_ratio`（Sub2API）是费用上限占用率，不是 Anthropic 原生 5 小时用量
  百分比**——后者需要对每个订阅账号再打一次实时代理 Anthropic 官方接口的调用，本片刻意不做
  这种未设预算的按账号扇出。如果产品侧确认需要原生用量数字，需要单独立项评估预算与调用
  频率。
- **NewAPI `vendor` 查表是本包抄的静态副本**，上游新增渠道类型后需要人工同步这张表，否则
  新类型渠道的 `vendor` 会悄悄变成 `null`（不是错误值，但会显得"这个字段没接上"）。
- **`upstream_multiplier`（Sub2API）多数账号会是 `null`**——只有开启过"上游计费探测"功能的
  账号才有值，这是能力覆盖范围本身的限制，不是缺陷，已在契约文档标注。
- 前端 TypeScript API 类型文件本片未触碰（任务允许在必要时只改 api client types 一处并知会
  chanmerge；本片判断由 XM-CHAN-MERGE0 自己维护类型更贴合其并发工作，避免与其改动冲突），
  已把完整字段清单、JSON 键名、每个字段的空值语义写进两份契约文档与本 handoff，供
  chanmerge 直接对照实现。
- 没有新增 Capability 声明——本片扩展的是既有方法（`ChannelDirectory()`）的返回值形状，未
  新增读方法，沿用已声明的 `sub2api.accounts.read`/`sub2api.channels.balance_read`/
  `newapi.channels.read` 等既有能力，符合"能力声明只在实现并测试完的同一个提交里做"的规则
  （因为这里根本没有新能力要声明）。
