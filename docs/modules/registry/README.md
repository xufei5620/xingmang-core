# Registry 模块

登记平台管理的四类注册对象（规格 §2.2）：Environment、Service、Connector、Connection。

## 职责

- 提供被管理系统实例（Service）与其连接方式（Connector/Connection）的唯一真相；
- 为 Connector 层提供 CredentialRef、目标地址 allowlist、能力集合与 Kill Switch 状态；
- 承载数据新鲜度回写（`source_watermark` / `observed_at`，规格 §9.1）。

## 不做

- 不保存任何明文凭据（只存 CredentialRef，ADR-014）；
- 不实现 Connector 的具体协议逻辑（那在 `connectors/` 下）；
- 不提供写 API：所有写入必须经 Action 层（ADR-003），本模块只暴露仓储方法。

## 代码结构

| 文件 | 职责 |
|---|---|
| `environment.go` | Environment 值类型与解析 |
| `service.go` | Service 领域类型与不变量 |
| `connector.go` | Connector/Connection/Capability 领域类型与 ADR-004 不变量 |
| `store.go` | 唯一接触 PostgreSQL 的文件；生成类型 ↔ 领域类型映射 |
| `gen/` | sqlc 生成物，**人工不得编辑**；改 SQL 后跑 `go tool sqlc generate` |

## 测试

- 领域测试：`go test ./internal/platform/registry/` —— 无需数据库，恒跑；
- 集成测试：设置 `XM_TEST_DATABASE_URL` 后同一命令自动启用；CI 必跑。

## 相关

- ADR-004（Connector 隔离第三方差异）、ADR-014（SecretProvider/CredentialRef）
- 契约：`contracts/connectors/credential-ref.v1.md`
- 后续：XM-0010 Action Core Lite 将成为本模块唯一的写入口
