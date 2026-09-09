// Package action 实现 ADR-003 的 Action 内核（Foundation-A 级 Core Lite）。
//
// 铁律：
//   - 所有业务与平台配置写操作必须经本包执行；读取走 Query，不经本包；
//   - 本包不认识任何具体业务：业务通过 Register 反向注册 Definition 与 Handler，
//     因此本包不 import 任何 modules/registry 包（避免循环依赖）；
//   - Foundation-A 只提供 L0/L1 快速路径；L2~L4 显式拒绝并要求 Foundation-B
//     （XM-0030），不得静默放行；
//   - 对外错误只暴露稳定错误码与安全文案，不透传底层或供应商原始错误
//     （规格 §18.4）；根因保留在 Unwrap 链中仅供服务端日志使用。
package action
