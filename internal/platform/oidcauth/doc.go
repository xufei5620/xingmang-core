// Package oidcauth 用 Keycloak 的 OIDC 令牌解析平台身份（XM-0008）。
//
// 它实现 httpapi.PrincipalResolver（结构化满足，不反向依赖 httpapi），
// 把 `solov-staff` Realm 签发的 Access Token 翻译成 principal.Principal。
//
// 铁律：
//   - **细粒度权限不进 Keycloak（ADR-016 / CR-0001 §5）**：Realm 只发 `staff`
//     这类粗粒度角色，`registry.read` / `ops.read` / `registry.service.manage`
//     由本包按 RoleScopeMap 翻译。令牌里**任何位置**出现平台形态的权限串，
//     都按配置漂移处理——忽略并记 warn，绝不采纳；
//   - **Environment 取服务自身配置**，绝不从令牌读：否则调用方可自称生产
//     （规格 §20.5 生产权限不继承）；
//   - **错误不做区分披露**：签名错、过期、aud 不符对外都是同一句话。把失败
//     原因分开告诉调用方，等于白送一个探测 issuer/audience 配置的接口。根因
//     只进服务端日志；
//   - **令牌内容绝不进错误与日志**（宪法 7 条）——包括根因链。
//
// 依赖：只用标准库。RS256 + JWKS 的手工校验不到 300 行，换一个第三方 OIDC
// 库要多养一条供应链，对本仓库的依赖纪律（VERSIONS.lock）不划算。
//
// 本包**不连接任何真实 Keycloak**：Issuer 由配置注入，发现与 JWKS 拉取都是
// 惰性的（首次校验令牌时才发生）。CR-0001 尚未执行时进程照常启动。
package oidcauth
