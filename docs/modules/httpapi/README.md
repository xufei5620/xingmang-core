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
| GET | `/api/v1/audit/events?limit=&before_seq=` | 是 |

执行路径用 `/versions/{version}/execute` 而非 `:execute` 后缀——chi 对路径参数后
紧跟冒号字面量的解析存在歧义，显式分段更稳。

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

## 错误码 → HTTP

见 `response.go` 的 `StatusForCode`。其中 `ADVANCED_CONTROLS_REQUIRED → 501`
是刻意的：不是调用方的错，是平台尚未实现该风险等级的控制（Foundation-B）。
前端据此显示「功能待上线」而非「无权限」。

## 身份

`PrincipalResolver` 有两个实现，由 `XM_AUTH_MODE` 选择，handler 与路由不需要改动：

| `XM_AUTH_MODE` | 实现 | 身份来源 | 允许的环境 |
|---|---|---|---|
| `dev-header` | `httpapi.DevHeaderResolver` | `X-Dev-*` 请求头 | development / staging |
| `oidc` | `oidcauth.Resolver`（XM-0008） | Keycloak `solov-staff` 的 Access Token | 全部（生产只能用它） |

默认值跟着 `ENVIRONMENT` 走：生产默认 `oidc`，其余默认 `dev-header`——
**XM-0008 不改 development / staging 的现状**。生产 + `dev-header` 拒绝启动，
`oidc` 缺 `XM_OIDC_ISSUER` / `XM_OIDC_AUDIENCE` 也拒绝启动（Fail Closed）。

`oidcauth` 只用标准库手工校验 RS256 + JWKS，没有新增依赖。它不导入本包——
接口靠 Go 的结构化满足，装配点在 `cmd/platform-api/auth.go`。

**切换到真实登录的完整步骤、RoleScopeMap 的人工审定要求、令牌校验清单与已知
取舍：见 [`AUTH-SWITCH.md`](./AUTH-SWITCH.md)。** 前置是 CR-0001 由人执行完毕；
代码侧从未连接过任何真实 Keycloak（测试用 httptest 假 IdP）。

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
