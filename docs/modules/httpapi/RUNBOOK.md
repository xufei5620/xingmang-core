# Runbook：platform-api

## 探针语义

| 端点 | 含义 | 失败后果 |
|---|---|---|
| `/healthz` | 进程存活 | 编排器会重启进程 |
| `/readyz` | 数据库可达 | 反代摘掉本实例，不重启 |

数据库抖动应该只影响 `/readyz`。若 `/healthz` 也失败，是进程本身的问题。

## 常见故障

| 现象 | 处置 |
|---|---|
| 启动即退出，`error_code=no_principal_resolver` | `ENVIRONMENT=production` 但还没接 OIDC（XM-0008）。这是**设计如此**：不允许无鉴权的生产实例 |
| 启动即退出，`error_code=config_invalid` | 缺 `ENVIRONMENT` 或 `XM_DATABASE_URL`，或 ENVIRONMENT 不是三值之一 |
| 所有 `/api/v1/*` 返回 403 | 缺 `X-Dev-*` 头（开发期）或 Token 无效（XM-0008 之后） |
| 某 Action 返回 501 | 该 Action 是 L2+，需 Foundation-B（XM-0030）。**不要**通过降低风险等级绕过 |
| 响应里看不到具体错误原因 | 这是设计如此。根因在服务端日志里，按 `request_id` 检索 |
| 本机 curl 连不上已发布端口 | Docker 端口转发被代理 TUN 劫持；用 compose 网络内的容器 curl（见 `cmd/platform-api/README.md`） |

## 按 request_id 排查

响应头与错误体都带 `request_id`。在日志中检索同一个 id 可以拿到访问日志、
错误根因与 ActionRun 记录（`action.action_run.request_id` 同值）。

```sql
SELECT action_id, status, error_code, started_at
FROM action.action_run WHERE request_id = '<req-id>';
```
