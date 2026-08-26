// Package registry 实现规格 §2.2 的注册中心数据模型：Environment、Service、
// Connector、Connection。
//
// 铁律：
//   - 本包只提供数据模型与仓储；所有写入必须由 Action 层调用（ADR-003），
//     业务模块与 HTTP Handler 不得直接使用 Store 的写方法；
//   - 每个对象显式绑定 Environment，生产权限不从测试继承（规格 §20.5）；
//   - Connection 只保存 CredentialRef 文本，绝不保存明文凭据（宪法 7 条 / ADR-014）；
//   - Connector 必须声明目标地址 allowlist；具写能力的 Connection 必须有
//     Kill Switch（ADR-004）。
//
// 领域类型（environment.go / service.go / connector.go）零 I/O、不依赖数据库，
// 可在任何层复用；store.go 是本包唯一接触 PostgreSQL 的文件。
package registry
