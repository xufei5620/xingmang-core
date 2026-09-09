package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// PrincipalResolver 从请求中解析调用者身份。
//
// 两个实现：
//   - DevHeaderResolver（本文件，仅非生产）——Foundation-A 的过渡手段；
//   - oidcauth.Resolver（internal/platform/oidcauth，XM-0008）——Keycloak 令牌。
//
// 后者不在本包，也不反向依赖本包：Go 的接口是结构化满足的，装配点在
// cmd/platform-api/auth.go，由 XM_AUTH_MODE 选择。换实现时本接口与所有
// handler 一行不动——切换步骤见 docs/modules/httpapi/AUTH-SWITCH.md。
type PrincipalResolver interface {
	Resolve(r *http.Request) (principal.Principal, error)
}

const (
	devPrincipalIDHeader   = "X-Dev-Principal-ID"
	devPrincipalTypeHeader = "X-Dev-Principal-Type"
	devScopesHeader        = "X-Dev-Scopes"
)

// devHeaderResolver 从请求头读取身份，**仅供开发与验收环境**。
type devHeaderResolver struct {
	environment string
}

// NewDevHeaderResolver 创建开发期身份注入器。
//
// 生产环境一律拒绝：允许用请求头自称身份等于没有鉴权。这个拒绝是硬编码的，
// 不提供任何开关——需要生产可用时，用 XM_AUTH_MODE=oidc 换成 oidcauth.Resolver。
//
// XM-0008 之后 cmd/platform-api 又在读配置时加了一道同样的拒绝，两道都留着：
// 那一道能在报错里直接告诉人该改哪个环境变量，这一道保证即便有人绕过配置
// 解析、直接调用本函数，生产也依然起不来。
func NewDevHeaderResolver(environment string) (PrincipalResolver, error) {
	if environment == "production" {
		return nil, fmt.Errorf("开发期 Principal 注入器不允许在生产环境使用；请接入 OIDC（XM-0008）")
	}
	return &devHeaderResolver{environment: environment}, nil
}

func (d *devHeaderResolver) Resolve(r *http.Request) (principal.Principal, error) {
	id := strings.TrimSpace(r.Header.Get(devPrincipalIDHeader))
	if id == "" {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	pt, err := principal.ParseType(strings.TrimSpace(r.Header.Get(devPrincipalTypeHeader)))
	if err != nil {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "身份类型非法", err)
	}
	var scopes []string
	for _, s := range strings.Split(r.Header.Get(devScopesHeader), ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	p := principal.Principal{
		ID:                  id,
		Type:                pt,
		IdentityZone:        "staff",
		Issuer:              "dev://header-resolver",
		AuthenticationLevel: "dev",
		// Environment 取服务自身配置，绝不由调用方指定——否则调用方可自称生产
		Environment: d.environment,
		Scopes:      scopes,
	}
	if err := p.Validate(); err != nil {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "身份不合法", err)
	}
	return p, nil
}

// RequirePrincipal 解析身份并注入上下文；解析失败一律 403。
func RequirePrincipal(resolver PrincipalResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := resolver.Resolve(r)
			if err != nil {
				// 解析失败时不记 principal_id：调用方**声称**的身份不是身份，
				// 把它写进访问日志等于让伪造者往可归责记录里塞任意值。
				WriteError(w, r, err)
				return
			}
			// 告知访问日志（XM-0031）。放在这里而不是各 handler 里：身份在这
			// 一处解析，归责记录就该在这一处生成，漏不掉也伪造不了。
			recordPrincipalID(r.Context(), p.ID)
			next.ServeHTTP(w, r.WithContext(principal.WithPrincipal(r.Context(), p)))
		})
	}
}
