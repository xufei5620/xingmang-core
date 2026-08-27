package audit

// ScopeRead 是读取审计事件所需的权限。
//
// **单独授予，不复用 ops.read / registry.read**：审计事件带 before_summary 与
// after_summary——那是被改动对象的前后镜像，等于把每次写操作的内容摊开给读者。
// 「谁能看运营指标」和「谁能看某人改了什么、改成了什么」是两个问题，敏感度
// 差一个量级；共用一个 scope 意味着给人看看板就顺手给了全平台的操作明细。
//
// 细粒度权限不进 Keycloak（ADR-016）——Keycloak 只发 staff 这类粗粒度角色，
// 平台自己解析成这些 scope。CR-0001 要求 Realm 里不要创建 audit.* 角色。
const ScopeRead = "audit.read"
