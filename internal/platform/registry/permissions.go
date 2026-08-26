package registry

// ScopeRead 是读取 Registry 元数据所需的权限。
//
// 与写权限（registry.service.manage 等，见 actions.go）同源同命名空间：
// 「谁能看见有哪些服务」和「谁能改它们」是同一套授权模型下的两个问题。
//
// 细粒度权限不进 Keycloak（ADR-016）——Keycloak 只给 staff 这类粗粒度角色，
// 平台自己解析成这些 scope。
const ScopeRead = "registry.read"
