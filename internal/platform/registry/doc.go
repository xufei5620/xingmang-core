// Package registry 实现规格 §2.2 的注册中心数据模型：Environment、Service、
// Connector、Connection。
//
// 铁律：
//   - 本包只提供数据模型与仓储；所有写入必须经 Action 内核执行（ADR-003）。
//     actions.go 已把 5 个写操作注册为 Action；业务模块与 HTTP Handler 必须
//     调用 action.Kernel.Execute，不得直接使用 Store 的 Create*/Update*/Set*
//     方法——那些方法只对本包的 Action Handler 与集成测试可见；
//   - 每个对象显式绑定 Environment，生产权限不从测试继承（规格 §20.5）；
//   - Connection 只保存 CredentialRef 文本，绝不保存明文凭据（宪法 7 条 / ADR-014）；
//   - Connector 必须声明目标地址 allowlist；具写能力的 Connection 必须有
//     Kill Switch（ADR-004）。
//
// 领域类型（environment.go / service.go / connector.go）零 I/O、不依赖数据库，
// 可在任何层复用；store.go 是本包唯一接触 PostgreSQL 的文件。
package registry
