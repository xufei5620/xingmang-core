# HTTP API 权限

本模块**不定义权限，只在路由上声明与判定**。

## 写操作

权限由 Action Definition 声明，Action 内核判定（见 `docs/modules/action/PERMISSIONS.md`）。
HTTP 层不复述、不加码——写路径只有一套授权规则。

## 读操作

规格 §2.4 要求 Query 同样受权限控制。判定在路由上用 `RequireScope` 声明：

| 端点 | 所需权限 | 常量 |
|---|---|---|
| `GET /api/v1/services`        | `registry.read` | `registry.ScopeRead` |
| `GET /api/v1/metrics`         | `ops.read`      | `ops.ScopeRead` |
| `GET /api/v1/metrics/history` | `ops.read`      | `ops.ScopeRead` |
| `GET /api/v1/audit/events`    | `audit.read`    | `audit.ScopeRead` |

三个 scope **分开授予**，不共用一个「读」权限：指标里将来会有收入、余额这类业务数据
（XM-0017 接入 Sub2API 之后），比「有哪些服务」敏感一个量级。共用一个 scope 意味着
给人看服务清单就顺手给了收入数字。

`audit.read` 又比 `ops.read` 高一档：审计事件带 `before_summary` / `after_summary`，
那是被改动对象的前后镜像——「谁能看运营指标」和「谁能看某人改了什么、改成了什么」
是两个问题。看板角色拿到 `ops.read` 不应顺带看见全平台的操作明细。

权限声明写在路由上而不是 handler 里，路由表因此成为「哪个端点要什么权限」的单一清单；
散在 handler 里的 if 谁也审计不了，还会随手长出第二套授权规则。

## 环境范围

`resolveEnvironment` 统一决定查询作用于哪个环境：

- 不传 `environment` → 用调用者 Principal 的环境（**不默认生产**）；
- 传了 → **必须与调用者一致**，否则 403。

`GET /api/v1/audit/events` 比这更严一档：**不接受 `environment` 参数**，一律用
Principal 的环境。`resolveEnvironment` 的「传了必须一致」已经能挡住越权，但审计事件
带前后摘要，多一个可写的入参就多一个将来被放宽成「跨环境审计看板」的口子。传了也
不生效（有测试断言这一点），不报错——它就是个被忽略的参数。

规格 §20.5 说生产权限不继承，那么跨环境读取就必须是显式授予的能力，而不是一个查询
参数。Foundation-A 阶段没有这种能力，所以一律拒绝（Fail Closed）。将来真需要跨环境
看板时，它应当是一个独立 scope（如 `platform.cross_env.read`），而不是放宽这里。

判定顺序是**先参数形态后权限**：`environment=stage` 这种拼错返回 400 而不是 403。
两者混在一起的话，调用方会拿着 403 去查权限配置，其实只是把 `staging` 写错了。

## 细粒度权限不进 Keycloak

ADR-016：Keycloak 只发 `staff` 这类粗粒度角色，`registry.read` / `ops.read` /
`audit.read` / `registry.service.manage` 由平台自己解析。CR-0001 明确要求 Realm 里
**不要**创建 `registry.*` / `ops.*` / `audit.*` 角色。

Foundation-A 期间 Principal 由 `NewDevHeaderResolver` 从 `X-Dev-Scopes` 头注入
（该 Resolver 在 `environment == "production"` 时构造即失败）。XM-0008 接入 Keycloak
后换成从 OIDC 令牌解析，本文件的权限表不变。
