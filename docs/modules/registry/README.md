# Registry 模块

登记平台管理的四类注册对象（规格 §2.2）：Environment、Service、Connector、Connection。

## 职责

- 提供被管理系统实例（Service）与其连接方式（Connector/Connection）的唯一真相；
- 为 Connector 层提供 CredentialRef、目标地址 allowlist、能力集合与 Kill Switch 状态；
- 承载数据新鲜度回写（`source_watermark` / `observed_at`，规格 §9.1）。

## 不做

- 不保存任何明文凭据（只存 CredentialRef，ADR-014）——**包括 endpoint 里的**，
  见下节；
- 不实现 Connector 的具体协议逻辑（那在 `connectors/` 下）；
- 不提供写 API：所有写入必须经 Action 层（ADR-003），本模块只暴露仓储方法。

## endpoint 不得带凭据（XM-0031）

`Service.Validate()` 拒绝 `endpoint` / `internal_endpoint` 里的两种凭据形态：

- **userinfo**：`https://user:pass@host/…`；
- **凭据类查询参数**：参数名含 `token` / `secret` / `password`，或切出的词段
  等于 `key` / `apikey` / `passwd` / `pwd` / `credential`。

挡的是一整条链（Codex 冷审 PR #47 第 2 条、PR #43 head `419ecf8`）：

```
表单 URL → registry.service.create → serviceSummary 把 endpoint 原样写进审计摘要
        → 不可篡改的审计链 → /api/v1/audit/events → 审计页原样回显
```

链的终点是一条**改不掉**的记录，所以唯一能真正关闭它的位置是入口。审计摘要的强制
脱敏（`audit.ActionSink`）按**键名**匹配，而这里凭据藏在一个叫 `endpoint` 的字段
的**值**里，键名脱敏看不见它——两道防线各管一段，缺一不可。

判据只看参数**名**，不做长度或熵的启发式（那会既误伤又漏判）；`key` 只按词段精确
匹配，因为 `?monkey=1`、`?keyword=abc` 必须放行——误拒的代价是运维直接登记不了
服务。错误信息只回显参数名，绝不回显值：错误会进日志与 API 响应，原样回显等于把
刚拦下的凭据又抄了一份出去。

这不是「URL 里没有凭据」的证明，只是把两种最常见的形态挡在门外。真正的凭据通道
是 `Connection.CredentialRef`。

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
