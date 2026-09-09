package main

import (
	"fmt"
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/oidcauth"
)

// newPrincipalResolver 按 XM_AUTH_MODE 装配身份解析器。
//
// 这是 XM-0008 的全部「切换」动作：CR-0001 执行完的那天，把 XM_AUTH_MODE 从
// dev-header 改成 oidc（issuer / audience 可以提前配好），重启即可。handler、
// 路由与权限表一行不动——PrincipalResolver 这个接口存在的意义就在这儿。
//
// 构造不发起网络请求：OIDC 的发现与 JWKS 拉取都是惰性的。身份服务抖动时平台
// 仍能启动并回答 /healthz，只是鉴权会失败——这比「Keycloak 没起来所以平台起
// 不来」好得多。
func newPrincipalResolver(cfg config, logger *slog.Logger) (httpapi.PrincipalResolver, error) {
	switch cfg.Auth.Mode {
	case authModeOIDC:
		r, err := oidcauth.NewOIDCResolver(oidcauth.Config{
			IssuerURL:    cfg.Auth.OIDCIssuer,
			JWKSURL:      cfg.Auth.OIDCJWKSURL,
			Audience:     cfg.Auth.OIDCAudience,
			Environment:  cfg.Environment,
			RoleScopeMap: cfg.Auth.OIDCRoleScopes, // nil 时用 oidcauth 的默认表
			ClockSkew:    cfg.Auth.OIDCClockSkew,  // 0 时用默认 60s
			Logger:       logger,
		})
		if err != nil {
			return nil, err
		}
		return r, nil

	case authModeDevHeader:
		// 生产在 authConfigFromEnv 就被拦掉了；这里的硬拒绝是第二道
		// （NewDevHeaderResolver 自己那道），两道都留着
		r, err := httpapi.NewDevHeaderResolver(cfg.Environment)
		if err != nil {
			return nil, err
		}
		// 这条 warn 是给看日志的人的：栈跑起来了，但它现在**没有真正的鉴权**。
		// 非生产是允许的，可它不该安静地发生
		logger.Warn("auth_mode_dev_header",
			slog.String("module", "platform.api"),
			slog.String("environment", cfg.Environment),
			slog.String("hint", "身份来自 X-Dev-* 请求头，仅供开发与验收；"+
				"生产必须 XM_AUTH_MODE=oidc（前置：CR-0001）"))
		return r, nil

	default:
		return nil, fmt.Errorf("未知的 XM_AUTH_MODE: %q", cfg.Auth.Mode)
	}
}
