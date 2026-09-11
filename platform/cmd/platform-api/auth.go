package main

import (
	"fmt"
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
)

// newPrincipalResolver 仅装配非生产开发头；本地会话需要数据库，由 main 装配。
func newPrincipalResolver(cfg config, logger *slog.Logger) (httpapi.PrincipalResolver, error) {
	switch cfg.Auth.Mode {
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
				"生产必须 XM_AUTH_MODE=local"))
		return r, nil

	default:
		return nil, fmt.Errorf("未知的 XM_AUTH_MODE: %q", cfg.Auth.Mode)
	}
}
