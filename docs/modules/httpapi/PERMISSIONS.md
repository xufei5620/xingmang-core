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
| `GET /api/v1/ui/saved-views`  | `ui.saved_view.manage` | `savedviews.ScopeManage` |

三个 scope **分开授予**，不共用一个「读」权限：指标里将来会有收入、余额这类业务数据
（XM-0017 接入 Sub2API 之后），比「有哪些服务」敏感一个量级。共用一个 scope 意味着
给人看服务清单就顺手给了收入数字。

`audit.read` 又比 `ops.read` 高一档：审计事件带 `before_summary` / `after_summary`，
那是被改动对象的前后镜像——「谁能看运营指标」和「谁能看某人改了什么、改成了什么」
是两个问题。看板角色拿到 `ops.read` 不应顺带看见全平台的操作明细。

权限声明写在路由上而不是 handler 里，路由表因此成为「哪个端点要什么权限」的单一清单；
散在 handler 里的 if 谁也审计不了，还会随手长出第二套授权规则。

`ui.saved_view.manage` 同时保护个人 SavedView 的 Query 与两个 L0 Action。这里不另拆
`ui.saved_view.read`：Query 只能返回当前 HUMAN Principal 在当前 Environment 下自己的
低影响偏好，拆成两份 scope 不增加隔离，只增加授权配置。它也不复用 `registry.read`
或 `ops.read`，避免把“能看业务数据”与“能保存自己的表格布局”绑成一个决定。owner 与
Environment 从 Principal 派生，客户端没有对应参数。

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
`audit.read` / `registry.service.manage` / `ui.saved_view.manage` 由平台自己解析。
CR-0001 明确要求 Realm 里**不要**创建 `registry.*` / `ops.*` / `audit.*` / `ui.*`
角色。

Foundation-A 期间 Principal 由 `NewDevHeaderResolver` 从 `X-Dev-Scopes` 头注入
（该 Resolver 在 `environment == "production"` 时构造即失败）。XM-0008 接入 Keycloak
后换成从 OIDC 令牌解析，本文件的权限表不变。

## XM-0008 后：scope 从哪儿来

权限表**一行没改**——变的只是 Principal.Scopes 的来源。

`XM_AUTH_MODE` 选择身份解析器（切换步骤见 `AUTH-SWITCH.md`）：

| 模式 | scope 来源 | 允许的环境 |
|---|---|---|
| `dev-header` | `X-Dev-Scopes` 请求头 | development / staging |
| `oidc` | `realm_access.roles` 经 **RoleScopeMap** 翻译 | 全部（生产只能用它） |

`oidc` 模式下这条链是：

```text
Keycloak Realm 角色（staff）
  → RoleScopeMap（平台配置，不在 Keycloak 里）
    → Principal.Scopes（registry.read / ops.read / …）
      → 路由上的 RequireScope 与 Action 内核判定（本文件上面那些表）
```

三点必须清楚：

- **RoleScopeMap 是授权策略，不是实现细节。** 它决定「登录进来的员工默认能看到
  什么」，因此 `oidcauth.DefaultRoleScopeMap` 只是代码里的默认值，**上生产前
  必须人工审定**并落到 `XM_OIDC_ROLE_SCOPES`。默认表刻意保守：`staff` 翻译成
  `registry.read` + `ops.read` + 仅限自己的 `ui.saved_view.manage`，**不含
  `audit.read`**——理由就是本文件上面那段
  「audit.read 又比 ops.read 高一档」。CR-0001 里 `staff` 是唯一的 Realm 角色，
  把 audit.read 塞进去等于每个员工默认看见全平台操作明细；
- **令牌里出现细粒度权限 = 配置漂移。** `registry.*` / `ops.*` / `audit.*` /
  `platform.*` / `action.*` / `connector.*` / `ui.*` 无论出现在 `realm_access.roles`、
  OAuth 的 `scope` 还是 `resource_access` 里，平台一律**忽略**并记 WARN
  （`error_code=keycloak_scope_drift`）。这是 ADR-016 的探针：平台侧忽略只是
  止血，修复要回到 Realm 那边走变更单；
- **`Environment` 仍然只来自服务配置。** 令牌里写什么都不作数（规格 §20.5）。
  上面「环境范围」一节的规则因此完全不受影响。

## 未关闭：`dev-header` 模式下 scope 仍是调用方自授

Codex 冷审多次判定 P1（PR #47 第 1 条、PR #48 第 5 条、PR #43 head `ba8e275` 第 1 条
与 `0a0642c` 第 1 条）：**上面那张权限表在 `dev-header` 模式下不构成鉴权**。
`RequireScope` 校验的是调用方自己在 `X-Dev-Scopes` 里填写的字符串，而
`deploy/nginx/launch.conf` 原样透传这些头；任何能到达该栈的人都能自称 `HUMAN`
并带上 `audit.read`。

XM-0008 把**能力**做好了（`XM_AUTH_MODE=oidc` 时 scope 来自 Realm 角色经
RoleScopeMap 翻译，生产更是硬性只能用 oidc）。但这条 P1 要到**部署实际切到 oidc**
那天才算关闭：`deploy/compose/launch.yaml:118-122` 明确写着 staging 仍走
`dev-header`，`XM_AUTH_MODE` / `XM_OIDC_*` 目前没有在那里透传。

XM-0031 没有动这条——它属于 XM-0008/CR-0001 的执行侧，还需要受信反代边界与限流
一起落地。在那之前，这些端点不得在可对外到达的环境启用。本节存在的意义是不让上面
那张表被误读成「已经有鉴权了」。
