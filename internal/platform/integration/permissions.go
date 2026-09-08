package integration

// ScopeRead 是读两张登记簿所需的权限。
//
// **不复用 registry.read**：那个 scope 今天默认发给 `staff`（见
// oidcauth.DefaultRoleScopeMap），而调用方登记簿里是「哪些机器身份该来调
// 我们、期望持有哪些 scope」——那是一张授权面的地图，看板角色不该顺带拿到。
// 与 action.read（跨 Action 执行记录）同一档：列表端点会把 action_run 里
// 观测到的调用方一并返回，泄漏面本就与那一档相当。
const ScopeRead = "integration.read"

// ScopeManage 是写两张登记簿所需的权限（四个 L1 Action）。
//
// 与读侧分开，是因为这两件事的授予理由不同：读是「看清现在登记了谁」，
// 写是「决定登记簿说什么」。对账页给一个只读角色是合理的，改登记簿不是。
const ScopeManage = "integration.manage"
