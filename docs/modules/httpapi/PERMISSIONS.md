# HTTP API 权限

本模块**不定义也不判定**权限。

- 写操作：权限由 Action Definition 声明，内核判定（见 `docs/modules/action/PERMISSIONS.md`）；
- 读操作：当前 `GET /api/v1/services` 依赖调用者 Principal 的 Environment 限定范围。

## ⚠️ 已知缺口（接入真实运营数据前必须补齐）

只读端点目前**尚未校验 `registry.read` 权限**——任何持有合法 Principal 的调用者
都能列出 Service 元数据。

- 现状风险可接受：Foundation-A 阶段这些只是本地登记的服务元数据（类型、实例名、
  端点、负责人），不含任何业务数据或凭据；
- **但规格 §2.4 明确要求 Query 也须权限检查**。在 Sub2API Connector 接入真实运营
  数据（XM-0017）之前必须补上，否则一旦看板有了收入/余额数据，这就是越权读取。

补齐方式：在 Query 层引入与 Action 同源的权限判定（复用 `Principal.HasScope`），
而不是在 handler 里散写 if——避免制造第二套授权规则。
