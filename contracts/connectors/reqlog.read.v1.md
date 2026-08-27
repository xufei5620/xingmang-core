# reqlog（请求审计系统）只读接入契约 v1 — **DRAFT**

| 项 | 值 |
|---|---|
| 状态 | **DRAFT（XM-0039）**。字段集合按《请求审计系统实现报告》推导，**未对真实控制台 API 核对** |
| Connector Key | `reqlog` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/reqlog`；真实客户端：**只有骨架**（`connectors/reqlog/client.go`，数据方法一律 `not_supported`） |
| 合规判据 | **必须通过 `connectors/reqlog/contracttest` 套件**（Fake 已通过，18 项） |
| 平台侧入口 | `internal/platform/requestlog`（权限 + 审计 + 错误翻译）；HTTP 层 `internal/platform/httpapi/requests.go` |
| 依据 | 交接文档 §9.4 请求详情 · `docs/superpowers/plans/2026-08-28-reqlog-integration-proposal.md`（已批准） |

⚠️ **DRAFT 的含义**：下面的类型是**平台希望拿到的形状**，不是 reqlog 已经承诺
的形状。真实控制台 API（`/root/reqlog`，单文件 Go，1,336 行）的源码尚未到手，
所以：`fake` 是唯一走得通的模式；`real` 的四道只读闸与 Basic Auth 已落地并有
测试，但**路由与响应解析留空**。冻结前不得据此写跨线接口。

---

## 1. 数据源事实（不是平台建的）

reqlog 是**已在生产运行**的外挂系统（2026-08-25 上线）：宝塔 nginx 与
NewAPI(:3000)/Sub2API(:8081) 之间的 Go 透明反向代理（:9301/:9302），零改上游。

- **捕获**：仅 `/v1/*` POST；全量明文 `req_body`/`resp_body`（含完整 SSE 事件流）、
  `token_prefix`、in/out/cache token、`upstream_request_id`
  （`X-Oneapi-Request-Id` / `X-Request-Id`）、status/dur/ttfb、model/stream/ip/ua；
- **存储**：单条 gzip + 当日索引行，按天分目录，**30 天自动清理**，~6.5GB/日；
- **查看**：`127.0.0.1:9300` 只读控制台（Basic Auth，SSH 隧道访问）；
  令牌→用户名映射每 10 分钟从两库只读拉取；
- **规模**：~13k 请求/日。

**平台的角色 = 带权限与审计的只读网关。** 请求正文永不落平台库
（PII + 6.5GB/日，两条理由都成立），不缓存，结构化日志里绝不打印正文。

## 2. 能力清单

```
reqlog.service.version_read     控制台版本
reqlog.requests.read            请求元数据分页
reqlog.request.content_read     单条请求正文
reqlog.health.read              运营健康
```

每一项都必须 `registry.ParseCapability` 可解析且 `IsWrite() == false`。
真实客户端在形状核实前返回**空清单**——声明一项没有代码可以兑现的能力就是说谎。

## 3. 数据形状

所有读取结果内嵌 `Snapshot{ObservedAt, Watermark, IsPartial, Instance}`
（规格 §9.1 禁止裸数字）。`Instance` 是回答本次读取的 reqlog 实例，
前端据此挂演示数据横幅（Fake 为 `reqlog-fake`，与 web 侧
`lib/demoData.ts` 的 `DEFAULT_DEMO_SOURCES` 逐字对应）。

### 3.1 `RequestLogSummary` — 元数据，**不含正文**

| 字段 | 说明 |
|---|---|
| `ID` | reqlog 侧记录标识。平台当**不透明字符串**，但必须 **URL 路径安全**（见 §3.4） |
| `Source` | `newapi` / `sub2api` |
| `OccurredAt` | 请求发生时刻（UTC） |
| `Username` | 令牌映射出的用户名；映射不到为空串，**不用 token_prefix 顶替** |
| `TokenPrefix` | 令牌前缀。不是凭据（不足以复原令牌），映射失败时的追查锚点 |
| `Model` / `Status` / `DurationMS` | 模型、HTTP 状态码（0 = 没记到）、耗时 |
| `TTFBMS` | **指针**：`nil` = 未记录，`0` = 合法观测值（缓存命中）。两者不可混同 |
| `TokensIn/Out/Cache` | 三者分列，缓存命中数**不并进输入**（计费口径不同） |
| `Stream` | 是否 SSE 流式 |
| `UpstreamRequestID` | 站内日志 ↔ 明文抄录的跨系锚点 |
| `ClientIP` | **已按 §3.3 脱敏** |

「不含正文」不是「暂时没放」——列表（`request.read`）与正文
（`request.content.read`）的权限分级就落在这个类型边界上。

`RequestLogPage` 另带 `NextCursor`（不透明游标）与 `RetentionDays`
（保留窗口，让界面说得出「只覆盖最近 N 天」——「那天没有请求」与
「过了保留期」在屏幕上长得一样，处置却完全不同）。

### 3.2 `RequestLogContent` — 正文（高敏）

| 字段 | 说明 |
|---|---|
| `Summary` | 同一条记录的元数据，随内容一并返回（详情页可深链直达） |
| `Messages` | 分角色消息数组（`system`/`user`/`assistant`/`tool`/…）。**未知角色原样透传**，不归到「其他」 |
| `MessagesParsed` | 区分「请求体里就没有消息」与「我们没解析出来」 |
| `FinalReply` | SSE 事件流**装配后**的最终回复；非流式请求同样填这里 |
| `RawRequest` / `RawResponse` | 原始载荷。响应侧对流式请求是**完整 SSE 事件流**，不是装配结果 |

分角色消息是**解析结果**，原始载荷是**事实**：两者都要，因为解析可能出错
（上游协议变化、非对话类调用），而排查那种问题只能看原文。

### 3.3 IP 脱敏口径（唯一实现：`reqlog.MaskIP`）

```
IPv4    保留前三段，末段抹掉        203.0.113.42        → 203.0.113.x
IPv6    保留前 48 位（前三组）       2001:db8:1234:5678::1 → 2001:db8:1234:x
带端口  先剥端口再按上面处理        203.0.113.42:51820  → 203.0.113.x
空串    返回空串（「没记」≠「记了但看不清」）
其他    返回 invalid-ip（**绝不回退到透传**）
```

**脱敏在契约层做，不留给前端**：一个原样返回明文 IP 的列表端点，只要有人抓一遍
就等于导出了全量用户 IP，而列表只要 `request.read` 这一级权限。

保留到 /24 而不是抹更狠：运营要回答「是不是同一个网段在刷」，/24 刚好够；
再抹一段这个问题就只能靠翻 reqlog 原始控制台解决——而那条路绕开平台的权限与审计。

### 3.4 记录标识必须 URL 路径安全

`ID` 只允许 RFC 3986 的 unreserved 字符（字母、数字、`-._~`），
由 `reqlog.IsPathSafeID` 判定，contracttest 会断言。

reqlog 按天分目录存放，真实标识很可能长成 `20260828/000123`。带斜杠的 id 塞进
`/requests/{id}` 会被路由切成两段，表现为「记录在列表里、点进去 404」。
**映射责任在连接器**：真实实现负责把上游标识变成安全形态（日期路径把 `/` 换成
`-`；实在不行退到 base64url），并在 `RequestContent` 里做反向映射。

### 3.5 截断规则（契约的一部分）

| 项 | 上限 |
|---|---|
| 单条消息 | 64 KiB（`MaxMessageBytes`） |
| 装配后的最终回复 | 256 KiB（`MaxFinalReplyBytes`） |
| 单侧原始载荷 | 512 KiB（`MaxRawPayloadBytes`） |
| 消息条数 | 200（`MaxMessages`） |

- 截断**按 UTF-8 字符边界**回退，绝不切开一个字符（切开会产出 U+FFFD，
  看起来像上游返回了乱码）；
- 截断必须被**标记**（`Truncated`）并给出**原始字节数**（`OriginalBytes`），
  界面据此显示「只显示了 X / 共 Y」——一个不声张的截断比不显示更危险；
- **在契约层截断而不是让前端少渲染**：一个 40 MB 的 SSE 流完整取回来再让前端裁掉，
  平台已经把它读进内存、也已经承担了那份泄漏面。真正的边界必须在取数这一侧。

## 4. 权限与审计

| 端点 | Scope | 审计 |
|---|---|---|
| `GET /api/v1/platforms/{platform}/requests` | `request.read` | 不写审计事件 |
| `GET /api/v1/platforms/{platform}/requests/{id}` | `request.content.read` | **每次成功读取写一条 `request.content.viewed`** |

- **两个 scope 分开授予**是这条能力成立的前提：元数据回答「这个人用得多不多」，
  正文回答「这个人问了什么」（交接文档 §9.4 列为高敏数据）；
- **审计写不进去就不返回内容**（fail closed）。§9.4 把「查看动作进入审计」列为
  前提，前提不成立就不该有能力。代价是数据库抖动期间详情页打不开——那是一次
  会被报障的可见故障，而静默的无审计披露没有任何人会发现；
- **失败的读取不写审计事件**：一次没有发生的披露不该出现在「谁看过什么」的清单里
  （访问日志已记下尝试）。若将来要连尝试也记，改的是
  `internal/platform/requestlog/service.go` 与本表；
- 审计摘要记 `source / record_id / model / username / occurred_at /
  upstream_request_id / message_count / *_bytes`——**足以说清披露了什么，
  但没有一个字的对话内容**，也**不含 token_prefix**（审计事件会经
  `/api/v1/audit/events` 回显给持 `audit.read` 的人，那是另一个独立授予的权限）；
- `reason` 是可选查询参数，原样写进审计的 `reason` 列。**是否必填是产品决定**
  （每看一条都要打字会改变日常操作手感），当前未强制。

`RoleScopeMap`：两个 scope 都**不给 `staff`**；`admin`（Realm 里今天并不存在）
预留了两项，其中 `request.content.read` 标注为最该被审定推翻的一项。

## 5. 四道只读闸（ADR-018）

| 闸 | 落地位置 | 状态 |
|---|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()` | ✅ 有测试 |
| 2 强制只读 | `connector.NewReadOnlyClient`（只放行 GET/HEAD） | ✅ 有测试 |
| 3 复核服务端只读 | 主机精确 allowlist + 拒绝重定向 | ⚠️ 部分（HTTP 通道只能做到这些） |
| 4 包内无写路径 | `ReadClient` 接口只有读方法；本包只发 GET | ✅ 有测试 |

上游响应体**一个字都不进错误链**。这条纪律在本连接器上比在别处更硬——
别处泄漏的是上游的内部细节，这里泄漏的是用户对话明文。

### ⚠️ 未决冲突：控制台是回环明文，而闸 1 要求 https

reqlog 控制台是 `http://127.0.0.1:9300`（回环 + 明文 + Basic Auth，靠 SSH 隧道
访问），而 `connector.Config.Validate()` 硬性要求 endpoint 是 **https**。
照现状配 `real`，构造客户端这一步就会失败（错误链里带
`reqlog.ErrLoopbackEndpointNotAllowed`，有测试钉住）。

三条出路，**都需要人来定**，XM-0039 不擅自选：

1. 平台与 reqlog 同机部署，控制台前挂一层本地 TLS（改部署，不改代码）；
2. 隧道那一端终结 TLS，平台连的是 https（改运维流程）；
3. 放宽 `Validate` 允许回环走 http（**改的是全体连接器共用的安全闸**，
   代价最大，需要单独的 ADR）。

## 6. 错误映射

| ErrorKind | 触发场景 | HTTP |
|---|---|---|
| `unavailable` | 网络不可达、超时、控制台 5xx | 502 |
| `auth` | Basic Auth 无效或权限不足 | 502 |
| `rate_limited` | 429 | 502 |
| `not_supported` | **形状未核实**（当前 real 模式的常态）；控制台 404/405 | 501 |
| `bad_response` | 参数不合法（未知 source、坏游标）、响应格式非法 | 400 |
| `forbidden_target` | 目标不在 allowlist | 500 |
| `write_attempt` | 只读通道上出现写请求 | 500 |

**「记录不存在」的判据是 `errors.Is(err, reqlog.ErrNotFound)`，不是 Kind**：
`connector.ErrorKind` 是全体连接器共用的枚举，里面没有这一档，而为本连接器往
共用枚举里加一项会让所有连接器的错误映射表跟着改。这类错误的 Kind 取
`not_supported`（调用方处置完全同构：重试不会变好、不该告警），HTTP 映射为 404。

平台侧不认识的 `{platform}`（CPA、开票、支付…）同样是 404：从这个端点看，
那个平台的请求资源确实不存在。

## 7. 配置（`cmd/platform-api`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `XM_REQLOG_MODE` | `off` | `off` / `fake` / `real`。**默认 off**，见下 |
| `XM_REQLOG_ENDPOINT` | 空 | real 必填，必须 https |
| `XM_REQLOG_TARGET_ALLOWLIST` | 空 | real 必填，逗号分隔的**精确**主机清单 |
| `XM_REQLOG_CREDENTIAL_REF` | 空 | real 必填，`secret://<scope>/<name>` |
| `XM_REQLOG_TOKEN` | 空 | 上面那个引用在 env Provider 下的落点。形态是 `用户名:口令` **整串** |
| `XM_REQLOG_TIMEOUT` | `30s` | 单次控制台读取超时 |

三条与别的连接器不同的纪律：

- **默认 `off` 而不是 `fake`**。别处 fake 的代价是「看板上几个数字是假的」；
  这里是「有人以为自己在看真实用户问了什么」。默认值要选更难出错的方向；
- **生产禁 `fake`**（启动即拒）：一个标着真实用户名的详情页显示编造的对话，
  会被拿去回复客诉、做风控判断；
- **`real` 配置不全启动即拒**（与 worker 的「写一条 SyncFailed 继续跑」相反）：
  那边是后台采集，一条通道配错不该拖垮心跳；这里是人点击触发的只读端点，
  带着半套配置起来，症状会是「点进去报错，查半天发现 allowlist 是空的」。

`XM_REQLOG_MODE=off` 时两个端点**不挂载**（404）。端点不存在比端点存在却一调
就 500 诚实——前端据此分得清「没接」和「坏了」。

## 8. 等真实 API 核对的字段清单

拿到 `/root/reqlog` 源码或端点清单后，**逐条**核对并把结论写回本表。
这是本契约与真实实现之间唯一的差异台账。

| # | 待核对项 | 平台当前假设 | 核对不上的后果 |
|---|---|---|---|
| 1 | **列表端点路由与查询参数名** | 未定（real 模式路由留空） | real 模式无法实现 |
| 2 | **详情端点路由** | 未定 | 同上 |
| 3 | **记录标识 `id` 的形状** | 不透明字符串，且**必须路径安全** | 带 `/` 的 id → 「列表里有、点进去 404」 |
| 4 | **分页形态** | 不透明游标 `NextCursor`；无总数 | 若上游只有 offset/页码，翻页会重复与漏读 |
| 5 | `source` 的取值写法 | 小写 `newapi` / `sub2api` | 写法不同 → 列表恒空（映射点：`ParseSource`） |
| 6 | `token_prefix` → 用户名的映射是否由控制台返回 | 由 reqlog 返回 `Username` | 若需平台自查，要另接两个上游库（本切片没有这条链路） |
| 7 | `ttfb` 缺失时的表达 | `nil`（区别于 0） | 若上游用 0 表示未测，缓存命中率会被算错 |
| 8 | in/out/cache token 是否恒存在 | 恒存在（非指针） | 若可缺失，要改成指针，否则「没记」会显示成 0 |
| 9 | `status` 为 0 / 缺失的表达 | 0 = 没记到 | 影响 `StatusError` 的划分（当前把 0 归入 error） |
| 10 | 状态过滤是否上游支持 | 平台传 `success`/`error` | 若上游不支持，要在连接器侧过滤（会破坏分页计数） |
| 11 | 时间过滤的参数与时区 | RFC3339 UTC，区间 `[since, until)` | 时区不一致 → 每天首尾两条落到错误的日子 |
| 12 | **SSE 是否由控制台装配** | 平台期望拿到装配后的 `FinalReply` | 若只给原始流，装配逻辑要落到连接器（`upstream.go`） |
| 13 | `resp_body` 对流式请求返回什么 | 完整 SSE 事件流原文 | 影响详情页「原始载荷」的渲染 |
| 14 | 保留期是否真的 30 天、是否可配 | 常量 `RetentionDays = 30` | 界面会说错「只覆盖最近 N 天」 |
| 15 | 控制台是否发布版本号 | 未知（Fake 用占位 `0.1.0`） | 兼容矩阵 `SupportedUpstreamVersions` 目前是占位 |
| 16 | 健康探测端点 | 未定 | `Health()` 目前恒返回 not_supported 的不健康 |
| 17 | Basic Auth 的用户名从哪来 | 凭据整串 `用户名:口令` | 若上游要求分离，`connector.Config` 需加字段（会引入不受 CredentialRef 管辖的一半） |
| 18 | **endpoint 的 TLS 落点** | 必须 https（闸 1） | 见 §5 的未决冲突 |
| 19 | `ip` 字段的实际形态 | 单个地址（可带端口） | 若是 `X-Forwarded-For` 链，整列会变成 `invalid-ip`（这是有意的信号） |
| 20 | 是否存在跨 source 的 id 重号 | 假设可能重号，故 `RequestContent(source, id)` 两参 | 无 |

## 9. 破坏性变更

本契约**尚未冻结**。真实 API 到位后按 §8 逐条核对、修正 v1 内容、
把状态改为「冻结」并注明依据。冻结之后再变形状必须发 v2 并保留 v1 兼容期。
