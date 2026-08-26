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

执行路径用 `/versions/{version}/execute` 而非 `:execute` 后缀——chi 对路径参数后
紧跟冒号字面量的解析存在歧义，显式分段更稳。

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
