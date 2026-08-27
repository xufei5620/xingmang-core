package requestlog

// ScopeRead 是读取请求**元数据列表**所需的权限。
//
// 单独授予，不复用 ops.read：ops.read 能看到的是聚合数字（今天多少次调用、
// 错误率多少），而这批数据是**逐条**的——谁、什么时候、用哪个模型问的、
// 花了多少 token。即便不含正文，一份逐条清单也足以还原一个人的使用轨迹，
// 敏感度比看板数字高一档（与 audit.ScopeRead 相对 ops.read 是同一条论证）。
const ScopeRead = "request.read"

// ScopeContentRead 是读取请求**正文**所需的权限。
//
// 与 ScopeRead 分开是本切片最重要的一条授权决定，理由不是「更谨慎一点」，
// 而是两者泄漏面差着量级：元数据回答「这个人用得多不多」，正文回答
// 「这个人问了什么」——后者是用户与模型之间的完整对话，包含用户自己粘进去的
// 合同、简历、身份信息、源码。交接文档 §9.4 把它明确列为高敏数据，
// 要求「需要明确权限」「查看动作进入审计」「默认禁止导出」。
//
// 三条纪律在代码里的落点：
//
//	明确权限 → 本常量，由路由上的 RequireScope 判定；
//	进审计   → Service.Content 每次成功读取写一条 request.content.viewed，
//	          写不进去就**不返回内容**（见 service.go 的 fail closed 说明）；
//	禁止导出 → 前端不提供导出按钮，且内容不落平台库、不进缓存。
//
// 细粒度权限不进 Keycloak（ADR-016）：Realm 只发 staff 这类粗粒度角色，
// 平台自己解析成这些 scope。DefaultRoleScopeMap 里**两个都不默认授予**——
// 默认能看全平台用户对话内容的角色不该由代码里的一张默认表来创造。
const ScopeContentRead = "request.content.read"
