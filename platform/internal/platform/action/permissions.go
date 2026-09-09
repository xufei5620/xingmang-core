package action

// ScopeRead 是读取跨 Action 执行记录（操作与审批页「执行记录」子页签）
// 所需的权限。
//
// 不复用 ops.read：执行记录逐条暴露 principal_id、action_id、风险等级、
// 错误码与耗时——谁在什么时候试图做什么、结果如何，这比运营指标的聚合数字
// 更接近「操作明细」。也不并入 audit.read：执行记录本身不含 before/after
// 内容摘要（那部分数据只存在于审计事件里），能看「谁做了什么、结果如何」的
// 人未必需要看到「具体改了什么内容」，两件事应该能分开授予（见 audit.ScopeRead
// 的注释——这里是同一个敏感度分级思路的延伸）。执行记录详情端点如果要带出
// 关联的审计前后摘要，会在路由上再叠加一个 audit.ScopeRead（同 ops.read +
// finance.read 叠加要求的先例，见 httpapi/router.go 的 platform channels 端点）。
//
// 细粒度权限不进 Keycloak（ADR-016）——Keycloak 只发 staff 这类粗粒度角色，
// 平台自己解析成这些 scope。
const ScopeRead = "action.read"
