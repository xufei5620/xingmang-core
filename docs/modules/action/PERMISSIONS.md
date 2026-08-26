# Action 权限

Action 本身不定义权限点——每个 Action 在 Definition 中声明它需要的权限，
权限点归属于业务模块（如 Registry 的权限见 `docs/modules/registry/PERMISSIONS.md`）。

## 当前已注册的 Action

| Action ID | 版本 | 风险 | 需要权限 | Foundation-A 可执行 |
|---|---|---|---|---|
| `registry.service.create` | 1 | L1 | `registry.service.manage` | ✅ |
| `registry.service.observe` | 1 | L0 | `registry.service.manage` | ✅ |
| `registry.connector.create` | 1 | L2 | `registry.connector.manage` | ❌ 需 Foundation-B |
| `registry.connection.create` | 1 | L3 | `registry.connection.manage` | ❌ 需 Foundation-B |
| `registry.connection.set_status` | 1 | L2 | `registry.connection.kill` | ❌ 需 Foundation-B |

## 授权规则

- 权限判定基于 `Principal.Scopes` 的精确匹配，无通配、无继承；
- Environment 必须在 Definition 的 `Environments` 列表内——生产权限不从测试继承（规格 §20.5）；
- Principal 类型必须在 `PrincipalTypes` 内：Foundation-A 阶段 Registry 写操作只允许
  `HUMAN`，机器身份接入在 XM-0033 之后；
- 前端隐藏按钮不构成安全控制，Kernel 是最终裁决点。
