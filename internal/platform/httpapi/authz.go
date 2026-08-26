package httpapi

import (
	"net/http"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// RequireScope 要求调用者持有某权限，否则 403。
//
// 权限声明放在路由上而不是 handler 里：路由表因此成为「哪个端点要什么权限」的
// 单一可读清单。散在 handler 里的 if 谁也审计不了，还会随手长出第二套规则——
// 而写路径的权限判定已经在 Action 内核里了（规格 §2.4 要求 Query 同样受控）。
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := principal.FromContext(r.Context())
			if !ok {
				// 正常情况下 RequirePrincipal 已经拦下了；这里是纵深防御——
				// 万一有人把本中间件挂在了 RequirePrincipal 之外
				WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
				return
			}
			if !p.HasScope(scope) {
				WriteError(w, r, action.NewError(action.CodePermissionDenied,
					"缺少权限 "+scope, nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resolveEnvironment 决定本次查询作用于哪个环境。
//
// 规则：不传 environment 用调用者自己的；传了必须与调用者一致。
//
// 「传了必须一致」这条是关键——之前的实现允许任意 environment 参数，
// 于是一个 staging 身份可以读生产数据。规格 §20.5 说生产权限不继承，
// 那么跨环境读取就必须是显式授予的能力，而不是一个查询参数。
// Foundation-A 阶段没有这种能力，所以一律拒绝（Fail Closed）。
func resolveEnvironment(r *http.Request, p principal.Principal) (registry.Environment, error) {
	envParam := r.URL.Query().Get("environment")
	if envParam == "" {
		envParam = p.Environment
	}
	// 先判参数形态再判权限，与 Action 内核的固定顺序一致：
	// 「参数就不合法」和「不许你读」是两类问题，混在一起会让 400 变成 403，
	// 调用方拿着 403 去查权限配置，其实只是拼错了环境名
	env, err := registry.ParseEnvironment(envParam)
	if err != nil {
		return "", action.NewError(action.CodeInvalidParams,
			"environment 必须是 development / staging / production 之一", err)
	}
	if string(env) != p.Environment {
		return "", action.NewError(action.CodePermissionDenied,
			"不允许跨环境读取：调用者身份属于 "+p.Environment, nil)
	}
	return env, nil
}
