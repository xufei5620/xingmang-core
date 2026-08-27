package platformusers

// ScopeRead 是读取**被管平台终端用户清单**所需的权限。
//
// 单独授予,不复用 ops.read:ops.read 能看到的是聚合数字(平台有多少用户、
// 总余额多少),而这批数据是**逐用户**的——谁、余额多少、消费多少、最后什么
// 时候来过。即便邮箱已经在契约层打了码,一份逐用户的资金明细也足以还原
// 一家客户的经营规模,敏感度比看板数字高一档。
//
// 与 request.read 的关系:两者是同一档。request.read 是逐条调用清单,
// 本 scope 是逐用户资金清单——一个回答「他用了什么」,一个回答「他花了多少」。
// 都属于「看板角色不该顺带获得」的那一类(见 oidcauth.DefaultRoleScopeMap
// 里关于 staff 的三条论证)。
//
// 细粒度权限不进 Keycloak(ADR-016):Realm 只发 staff 这类粗粒度角色,
// 平台自己解析成这些 scope。`platform.` 前缀已经在 oidcauth 的
// platformScopePrefixes 里,令牌里冒出这个串会被当成配置漂移而拒绝。
const ScopeRead = "platform.users.read"
