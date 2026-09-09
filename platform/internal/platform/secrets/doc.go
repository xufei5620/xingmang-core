// Package secrets 实现 ADR-014 Foundation-A：CredentialRef 间接引用与
// SecretProvider 体系。
//
// 铁律（宪法 7 条 / 规格 §4.5、§18.1）：
//   - Connector 与业务代码只接受 CredentialRef，禁止直接读固定环境变量或文件路径；
//   - 明文只存在于 SecretValue 内，禁止序列化、打印、入日志、返回前端；
//   - 所有解析路径显式配置（scope 路由、env 登记映射），禁止静默回退其他数据源；
//   - 每次读取经 Audited 装饰器记录审计（不含明文）。
package secrets
