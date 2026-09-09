# Registry 权限

本模块的读写权限（规格 §4.2 命名风格）。权限点在 XM-0010 Action Core Lite
落地后与 Action 绑定；当前先冻结命名。

| 权限 | 含义 | 风险等级 |
|---|---|---|
| `registry.read` | 读取 Service / Connector / Connection 列表与详情 | — |
| `registry.service.manage` | 新增或修改 Service 登记 | L1 |
| `registry.connector.manage` | 新增或修改 Connector 类型版本 | L2 |
| `registry.connection.manage` | 新增或修改 Connection（含 CredentialRef 绑定） | L3 |
| `registry.connection.kill` | 拉闸 Kill Switch，把连接置为 killed | L2 |

规则：

- `credential_ref` 字段对前端只返回引用文本，永不解析为明文（宪法 7 条）；
- 生产环境的 `registry.connection.manage` 需 Step-up MFA（Foundation-B 后生效）；
- 前端隐藏按钮不构成安全控制，服务端必须再次校验（规格 §4.2）。
