package main

import (
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/rolepermissions"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// localAuthRoleMap 返回本地登录（XM-LOGIN）用的"角色 -> scope"翻译表：
// 使用通用员工权限配置（XM_AUTH_ROLE_SCOPES 留空则用 rolepermissions 的默认
// 表）。角色能做什么只维护一张表，不必区分当前是哪种登录模式。
func localAuthRoleMap(cfg config) map[string][]string {
	if cfg.Auth.RoleScopes != nil {
		return cfg.Auth.RoleScopes
	}
	return rolepermissions.DefaultRoleScopeMap()
}

// registerLocalAuthActions 注册员工账号管理的七个 Action（staff.manage；
// XM-LOGIN 四个 + XM-AUTH-TOTP0 三个 TOTP Action）。注册失败即拒绝启动——
// 同其它模块的纪律：不能让「账号管理页有按钮但后端没注册动作」的进程跑起来。
//
// credentialStore 是 TOTP 密钥的写路径，与 credential.secret.upsert 共用
// 同一个仓储实例（CR-0006 正文："复用既有 L1 Action，不新增写入口"）；
// secretReader 是读路径，装配见 platformUsersSecretProvider（同一份
// XM_SECRET_ROOT，与用户/支付两条 real 模式读凭据的方式同构）。
func registerLocalAuthActions(
	reg *action.Registry, cfg config, store *localauth.Store,
	credentialStore *credentials.Store, secretReader secrets.SecretProvider,
) error {
	return localauth.RegisterActions(reg, store, localAuthRoleMap(cfg), credentialStore, secretReader, cfg.Environment)
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
