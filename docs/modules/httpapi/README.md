# HTTP API 模块

Platform API 的 HTTP 层（规格 §5.5）。

## 铁律

- **不做授权判定**：权限、环境、风险等级全部由 Action 内核裁决（规格 §4.2）。
  HTTP 层只做协议转换与错误映射——在这里加权限短路等于制造第二套规则，
  两套规则迟早会分叉。
- **不透传底层错误**：只有 Action 错误的 Message 会出现在响应里；其他错误一律
  兜底为 `INTERNAL`，避免内网地址、SQL 约束名、供应商错误泄漏（规格 §18.4）。
- **不记录 Header**：Authorization / Cookie 带 Token（宪法 7 条）。
- 平台不参与用户实时请求路径（ADR-011）。

## 路由

| 方法 | 路径 | 需身份 |
|---|---|---|
| GET | `/healthz` | 否 |
| GET | `/readyz` | 否 |
| GET | `/api/v1/actions` | 是 |
| POST | `/api/v1/actions/{id}/versions/{version}/execute` | 是 |
| GET | `/api/v1/services?environment=` | 是 |
| GET | `/api/v1/metrics?environment=` | 是 |
| GET | `/api/v1/metrics/history?metric_key=&hours=` | 是 |
| GET | `/api/v1/audit/events?limit=&before_seq=` | 是 |

执行路径用 `/versions/{version}/execute` 而非 `:execute` 后缀——chi 对路径参数后
紧跟冒号字面量的解析存在歧义，显式分段更稳。

## `Cache-Control: no-store`（XM-0031）

`/api/v1` **整组**响应带 `Cache-Control: no-store`；`/healthz`、`/readyz` 不带。

这些响应带 `principal_id`、`resource_id`、操作前后镜像、收入与余额，全部按
Principal 与环境裁剪过，没有一条可以被共享缓存或浏览器落盘。403 与 404 也要覆盖
——一个「你没有权限看审计」的响应同样不该被中间缓存留下，而且缓存过的 403 会在
授权修好之后继续挡人。

为什么是**路由中间件**而不是塞进 `WriteJSON`：

1. 这是策略，不是编码细节。`WriteJSON` 回答「怎么把值变成字节」，缓存语义回答
   「谁可以把这些字节存下来」。后者藏进序列化 helper，路由表就不再是「哪个端点
   受什么约束」的完整清单——而 `RequireScope` 已经立了「约束写在路由上」的规矩。
2. 作用范围要能被看见。装在整组上，新加的端点自动继承，不靠作者记得。
3. 探针保持可缓存：反代与外部看门狗对 `/healthz` 做几秒微缓存是合理的运维手段，
   不该被一条为审计数据设的策略顺手波及。

指令只写 `no-store`，不写 `private, no-store`：`no-store` 禁止**任何**缓存（共享
的与私有的）把响应落到存储，严格强于只约束共享缓存的 `private`。两个并列会让人
以为它们互补，将来有人「优化」成只留 `private` 时也看不出退化。

## 访问日志记 principal_id（XM-0031）

`AccessLog` 除 method / path / status / duration / request_id 外，还记
`principal_id`；身份未解析时是空串（字段恒在，检索不必区分「没有这个字段」与
「值为空」）。

`/api/v1/audit/events` 返回操作前后镜像，`/api/v1/metrics` 返回收入与余额；这类
读取出了事必须能回答「是谁在什么时候拉走的」。只记 method/path/status 时，日志
能证明「有人拉过 100 条审计」，却证明不了是谁。

**解析失败时不记调用方声称的 ID**——那不是身份，记它等于让伪造者往可归责记录里
塞任意值。也**只记 principal_id，不记 scopes / issuer / 身份类型**：ID 足以归责，
其余是授权决策的输入，进日志只会扩大留存面；需要复原授权判定时看审计链。

实现上，`RequirePrincipal` 通过 `AccessLog` 预先放进 context 的可变槽回传 ID：
`RequirePrincipal` 用 `r.WithContext` 派生的 context 只对内层可见，包在外面的
`AccessLog` 读不到。槽带锁，因为 `Timeout` 用 `http.TimeoutHandler`，它在另一个
goroutine 里跑内层链，超时后先行返回——没有锁那是一个只在超时路径上偶发的数据竞争。

## `GET /api/v1/metrics/history` 响应契约

权限 `ops.read`。`metric_key` 必填且必须是**已注册**的指标（`ops.KnownMetricKey`；
形态合法但不存在的键返回 400，错误文案列出全部已注册指标）。
`hours` 默认 24、上限 168；非法值一律 400 而不是静默取默认。

```json
{"items":[{"observed_at":"2026-08-27T03:00:00Z","synced_at":"2026-08-27T03:00:00Z",
  "source":"sub2api-staging","status":"ok","is_partial":false,"watermark":"wm-1",
  "last_error_code":"","value":{"amount_minor":9007199254740995,"currency":"CNY"}}],
 "truncated":false,"limit":1000}
```

四处前端需要知道的约定：

- **每个点都带 `source`**。查询只按 `(environment, metric_key)` 过滤，一条曲线上
  可能混着不同来源（Fake 切 real、换实例）。前端据此在换源处断线或加标记。
- **`truncated` / `limit` 是顶层的截断事实**。`hours=168` 在 5 分钟粒度下约 2016
  个点，而单次最多返回 1000。没有这两个字段时，前端无从区分「前 3.5 天真的没有
  数据」与「服务端把它裁掉了」。两个字段恒在，未截断时 `truncated:false`。
- **历史点不带 `freshness`**：新鲜度是相对「现在」算的派生量，对一个过去的时刻算
  它没有意义。但每个点都带 `observed_at` / `synced_at` / `status` / `is_partial` /
  `last_error_code`，也就是新鲜度的全部原料——它不是裸数字。
- **金额是 JSON number 字面量，且大整数不丢精度**。仓储层用
  `json.Decoder.UseNumber()` 解 jsonb，`> 2^53` 的 minor units 逐字原样输出，
  既不变成 float64 也不变成带引号的字符串。

## `GET /api/v1/audit/events` 响应契约

权限 `audit.read`；环境不可指定，恒为调用者 Principal 的环境。
`limit` 默认 50、上限 100（超出静默夹到上限）；`before_seq` 是**开区间**游标。

```json
{"items":[{"sequence":123,"occurred_at":"2026-08-26T10:00:00.123456Z",
  "principal_id":"staff_alice","principal_type":"HUMAN",
  "action_id":"registry.service.create","action_version":"1",
  "action_run_id":"<uuid>","resource_type":"core.service","resource_id":"sub2api-prod",
  "environment":"staging","request_id":"req-1","result":"succeeded","error_code":"",
  "before_summary":null,"after_summary":{"exists":true},
  "event_hash":"<64hex>","prev_hash":"<64hex>"}],
 "next_before":122}
```

三处前端需要知道的约定：

- `error_code` 映射自 `Event.CompensationResult`（失败事件里记的是补偿动作的结果码）。
  `compensation_result` 是内核内部的说法，不进契约。
  **这一条 Codex 冷审已判为契约不诚实（PR #47 第 7 条），XM-0031 未修**——修法要
  在事件与数据库契约里加一列独立的 `error_code`，那是一条会动审计表的迁移，
  应有自己的 Task Spec 与冷审。
- **空摘要序列化为 `null` 而不是 `{}`**。库里 jsonb 列 `NOT NULL DEFAULT '{}'`，
  读回来是非 nil 空 map，由 handler 转成 `null`。`{}` 和 `null` 是两种事实：
  前者「记录了摘要但内容为空」，后者「这个动作没有前后镜像」（读类动作、被拒绝的执行）。
  都渲染成 `{}` 的话前端得自己写 `Object.keys().length` 去猜。
- `next_before = 0` 表示**确定**没有下一页；非 0 时把它当作下一次请求的 `before_seq`。
  本页条数少于请求的 `limit` 即判定到底。页面正好被填满而后面恰好没有数据时
  `next_before` 仍非 0，下一次请求返回空页——刻意如此：精确判断「还有没有」
  要多查一行或多打一次 COUNT，代价换来的只是省掉一次空请求。

响应**不是** `audit.Event` 的直接序列化：`reason` / `approval_id` / `trace_id` /
`source_ip` 与两个 connector 摘要不外露（内部排障字段，或可能带上游返回的敏感片段）。
新增字段必须是显式动作——响应体是契约，不是结构体的倒影。
两个 connector 摘要现在**在 SQL 层就不取**（XM-0031），不只是不序列化。

`before_summary` / `after_summary` 在入链口已被 `RedactDefault` 强制脱敏
（XM-0031，见 `docs/modules/audit/README.md`）：这里返回的是链上的值，而链上
不该有明文凭据。读 API 不做二次脱敏——脱敏发生在写入侧才有意义，链不可篡改。

## 错误码 → HTTP

见 `response.go` 的 `StatusForCode`。其中 `ADVANCED_CONTROLS_REQUIRED → 501`
是刻意的：不是调用方的错，是平台尚未实现该风险等级的控制（Foundation-B）。
前端据此显示「功能待上线」而非「无权限」。

## 身份

`PrincipalResolver` 接口有两个实现路径：Foundation-A 的 `DevHeaderResolver`
（生产硬拒绝）与 XM-0008 的 OIDC 实现。换实现时 handler 不需要改动。

## 端到端验证记录（2026-08-26）

首次全链路冒烟（HTTP → Action 内核 → Registry → PostgreSQL → ActionRun）：

| 场景 | 结果 |
|---|---|
| `/healthz`、`/readyz`（真实库） | 200 / `ready` |
| 无身份访问 `/api/v1/services` | 403 |
| 列 Action | 5 个，其中 3 个 `executable:false` 且带 `blocked_reason` |
| 缺权限执行 L1 | 403，`action_run` 留 `PERMISSION_DENIED` |
| 有权限执行 L1 | 200 + `action_run_id`，Service 落库，审计 `succeeded` |
| 执行 L2 | 501，`action_run` 留 `ADVANCED_CONTROLS_REQUIRED` |
| `X-Request-ID` 串联 | 传入 `smoke-req-001`，在 `action_run.request_id` 中原样可见 |
| 未采集 Service 的新鲜度 | `observed_at:null`、`stale_seconds:null` |
