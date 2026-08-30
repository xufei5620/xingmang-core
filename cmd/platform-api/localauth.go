package main

import (
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/oidcauth"
)

// localAuthRoleMap 返回本地登录（XM-LOGIN）用的"角色 -> scope"翻译表：
// 与 OIDC 路径共用同一份配置（XM_OIDC_ROLE_SCOPES 留空则用 oidcauth 的默认
// 表）。角色能做什么只维护一张表，不必区分当前是哪种登录模式。
func localAuthRoleMap(cfg config) map[string][]string {
	if cfg.Auth.OIDCRoleScopes != nil {
		return cfg.Auth.OIDCRoleScopes
	}
	return oidcauth.DefaultRoleScopeMap()
}

// registerLocalAuthActions 注册员工账号管理的四个 Action（staff.manage）。
// 注册失败即拒绝启动——同其它模块的纪律：不能让「账号管理页有按钮但后端
// 没注册动作」的进程跑起来。
func registerLocalAuthActions(reg *action.Registry, cfg config, store *localauth.Store) error {
	return localauth.RegisterActions(reg, store, localAuthRoleMap(cfg))
}

// localAuthHandlersOrNil 让「未运行在 local 模式」在 httpapi.Deps 里表现成
// 一个真正的 nil 接口值，而不是一个包着 nil 指针的非 nil 接口值——后者会让
// 路由层 `if d.LocalAuth != nil` 判断失真，把端点错误地挂载出来（同
// requestLogsOrNil / platformUsersOrNil 的纪律，见 reqlog.go / platformusers.go）。
func localAuthHandlersOrNil(h *localauth.Handlers) httpapi.LocalAuthHandlers {
	if h == nil {
		return nil
	}
	return h
}
