package httpapi

import "net/http"

// localAuthScopeManage 必须与 localauth.ScopeManage 逐字相同。
//
// httpapi 不能 import localauth：localauth 的 handlers.go 反过来依赖 httpapi
// 的响应/限流类型（WriteJSON、RateLimiter……），import 会成环。字符串常量
// 在这里复制一份，与 oidcauth/rolemap.go 硬编码 "credential.manage" /
// "connector.manage"（而不 import credentials 包）是同一种处理方式——
// 改动 localauth.ScopeManage 的值时必须同步改这里，两处都有测试覆盖。
const localAuthScopeManage = "staff.manage"

// LocalAuthHandlers 是本地登录（XM-LOGIN）HTTP 端点的最小接口
// （*localauth.Handlers 满足）。
//
// httpapi 不反向依赖 localauth 包的具体类型——与 PrincipalResolver 同一条
// 装配纪律（见 principal.go 顶部注释）：三种身份模式（dev-header / oidc /
// local）都不需要 httpapi import 具体实现，装配点在 cmd/platform-api，由
// XM_AUTH_MODE 选择。
//
// 为什么不像 Credentials/SavedViews 那样只放一个只读 Query 接口、再由
// httpapi 自己包一层 handler：本地登录的五个端点本身就是完整的 HTTP 处理器
// ——要读 Cookie、写 Set-Cookie、按 IP 限流——localauth 包比 httpapi 更清楚
// 这些协议细节，没有必要在这里重新包一层。
type LocalAuthHandlers interface {
	Login(w http.ResponseWriter, r *http.Request)
	Logout(w http.ResponseWriter, r *http.Request)
	Me(w http.ResponseWriter, r *http.Request)
	ChangePassword(w http.ResponseWriter, r *http.Request)
	ListAccounts(w http.ResponseWriter, r *http.Request)
}
