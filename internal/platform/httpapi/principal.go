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
// Foundation-A 用 DevHeaderResolver（仅非生产）；XM-0008 接入 Keycloak 后
// 新增 OIDC 实现，本接口与所有 handler 不需要改动。
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
// 不提供任何开关——需要生产可用时，接 XM-0008 的 OIDC 实现。
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
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(principal.WithPrincipal(r.Context(), p)))
		})
	}
}
