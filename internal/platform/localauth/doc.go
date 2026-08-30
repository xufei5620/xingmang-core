// Package localauth 实现 XM-LOGIN：星芒自带的员工用户名/口令登录。
//
// 这是继 dev-header（仅非生产）与 oidc（XM-0008，依赖 Keycloak）之后的第三种
// PrincipalResolver 实现：账号与口令哈希落在平台自己的库里
// （core.staff_account），会话是服务端持有状态的 Cookie
// （core.staff_session），不依赖任何外部身份提供方。
//
// 文件划分：
//   - password.go —— argon2id 哈希与校验，不涉及数据库或 HTTP；
//   - store.go —— 账号与会话的仓储（手写 pgx SQL，不经 sqlc，与
//     internal/platform/credentials 同一条纪律）；
//   - resolver.go —— 从 Cookie 解析 Principal，满足 httpapi.PrincipalResolver，
//     并对非只读方法强制 CSRF 头；
//   - permissions.go —— staff.manage scope 声明；
//   - actions.go —— 账号管理的 L1 HUMAN-only Action（create / set_roles /
//     set_disabled / reset_password）；
//   - handlers.go —— /api/v1/auth/* 与 /api/v1/staff/accounts 的 HTTP 处理器。
//
// 三条红线（宪法 7 条同一精神，只是标的物从「第三方凭据」换成「本地口令」）：
//   - 密码明文只在 HTTP 请求体里出现一次，不落库、不进日志、不进审计摘要；
//   - password_hash 是 Account 的未导出字段：任何跨包响应都必须显式挑字段
//     构造（见 handlers.go 的 accountSummary/toSessionResponse），不给
//     「整个结构体一起序列化」留下漏发的机会；
//   - 会话 token 只在登录成功那一次 Set-Cookie 里出现一次，库里只存它的
//     sha256 摘要——即便 core.staff_session 被拖走，也换不来一个能用的 cookie。
package localauth
