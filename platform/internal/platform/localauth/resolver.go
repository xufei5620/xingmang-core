package localauth

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	// SessionCookieName 是本地登录会话 Cookie 的名字。
	SessionCookieName = "xm_session"

	// CSRF：本地登录用 Cookie 装身份，浏览器会在跨站请求上自动附带它，传统的
	// "提交一个隐藏表单"攻击就成立了（OIDC/dev-header 的身份走 Authorization
	// 头，浏览器不会跨站自动附带，天然不受这类攻击影响）。挡它的办法是要求
	// 一个自定义头——跨站 <form> 提交加不出自定义头，只有同源的 fetch/XHR
	// 才能加，这是业界对"仅 Cookie 鉴权"场景的标准 CSRF 缓解手段之一。
	csrfHeaderName  = "X-Requested-With"
	csrfHeaderValue = "xingmang"

	// 对外文案：与 oidcauth 的纪律一致——"有没有带身份"是调用方自己知道的
	// 事实可以直说，身份为什么不合格一律同一句，不做区分披露。
	msgMissingSession = "缺少身份"
	msgInvalidSession = "会话无效或已过期"
	msgMissingCSRF    = "缺少 CSRF 头 X-Requested-With: xingmang"

	identityZoneStaff = "staff"
	issuerLocalAuth   = "xingmang://localauth"
	authLevelPassword = "password"
	// authLevelMFA 与 oidcauth 令牌解析出的 "mfa" 取值同一形态（AUTH-SWITCH.md
	// §7：amr 含 otp/mfa/hwk 时映射为 mfa）——两种登录模式的 Principal
	// 消费方（如任何按 AuthenticationLevel 判断的下游代码）不需要关心
	// 身份具体来自 OIDC 还是本地登录。
	authLevelMFA = "mfa"
)

// sessionLookup 是 Resolver 依赖的最小面（*Store 满足），便于测试注入假实现。
type sessionLookup interface {
	LookupSession(ctx context.Context, rawToken string) (Account, error)
}

// Resolver 从 xm_session Cookie 解析 Principal，实现 httpapi.PrincipalResolver。
type Resolver struct {
	store       sessionLookup
	environment string
	roleScopes  func([]string) []string
}

// NewResolver 装配 Resolver。
//
// roleScopes 把账号的角色名翻译成平台细粒度 scope——调用方通常传
// RoleScopesFrom(roleMap)，roleMap 复用 oidcauth.DefaultRoleScopeMap() 或
// XM_OIDC_ROLE_SCOPES 解析出的表，让本地登录与 OIDC 共享同一份"角色能做
// 什么"的定义，运维只需要维护一张表。
func NewResolver(store sessionLookup, environment string, roleScopes func([]string) []string) *Resolver {
	return &Resolver{store: store, environment: environment, roleScopes: roleScopes}
}

// RoleScopesFrom 把一张"角色 -> scope 列表"的表封装成 Resolver 需要的翻译
// 函数：并集去重后排序。
//
// 未知角色静默忽略——本地账号的角色是管理员从已知角色名单里选的
// （staff.account.create / set_roles 的 Schema 在写入时就校验过），这里不
// 需要复刻 oidcauth 内部 translateRoles 的"外部令牌声明漂移"巡检那部分：
// 漂移巡检针对的是"Keycloak 里可能被人加了不该加的角色"，本地账号的角色
// 来源是平台自己的写路径，不存在这个威胁模型。
func RoleScopesFrom(roleMap map[string][]string) func([]string) []string {
	return func(roles []string) []string {
		seen := make(map[string]struct{})
		var out []string
		for _, role := range roles {
			for _, sc := range roleMap[strings.TrimSpace(role)] {
				if _, dup := seen[sc]; dup {
					continue
				}
				seen[sc] = struct{}{}
				out = append(out, sc)
			}
		}
		sort.Strings(out)
		return out
	}
}

// Resolve 校验会话 Cookie 并映射成 Principal；对非只读方法额外强制 CSRF 头。
func (r *Resolver) Resolve(req *http.Request) (principal.Principal, error) {
	cookie, err := req.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, msgMissingSession,
			errors.New("缺少会话 cookie"))
	}
	acc, err := r.store.LookupSession(req.Context(), cookie.Value)
	if err != nil {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, msgInvalidSession, err)
	}
	if err := requireCSRFHeader(req); err != nil {
		return principal.Principal{}, err
	}

	authLevel := authLevelPassword
	if acc.SessionMFAAt != nil {
		// 与 oidcauth 的 amr/acr 推导同一惯例（AUTH-SWITCH.md §7）：
		// 会话验证过 TOTP/恢复码即视为 mfa 等级，不要求"此刻"仍在
		// 任何步进窗口内——那是 RequireFreshOTP 的判断范畴，
		// AuthenticationLevel 只是粗粒度展示，MFAAt 才是精确依据。
		authLevel = authLevelMFA
	}
	p := principal.Principal{
		ID:                  "staff:" + acc.Username,
		Type:                principal.TypeHuman,
		IdentityZone:        identityZoneStaff,
		Issuer:              issuerLocalAuth,
		Subject:             acc.ID.String(),
		AuthenticationLevel: authLevel,
		// 绝不从请求读环境（与 oidcauth/dev-header 同一条纪律，规格 §20.5）
		Environment: r.environment,
		Scopes:      r.roleScopes(acc.Roles),
		MFAAt:       acc.SessionMFAAt,
	}
	if err := p.Validate(); err != nil {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "身份不合法", err)
	}
	return p, nil
}

// RequireFreshOTP 判断 p.MFAAt 是否在 maxAge 时限内（步进/step-up 校验的
// 标准判定 helper，供日后需要"最近验证过 TOTP"这类前置检查的场景使用
// ——如 XM-INVCON1 的断言签发端点，签发前要求 mfa_at 在 StepUpMaxAge
// 内，否则回 ADMIN_STEP_UP_REQUIRED）。MFAAt 为 nil（从未过 TOTP，或
// 当前身份来源不适用这个概念）一律视为不新鲜。
func RequireFreshOTP(p principal.Principal, maxAge time.Duration, now time.Time) bool {
	if p.MFAAt == nil {
		return false
	}
	age := now.Sub(*p.MFAAt)
	return age >= 0 && age <= maxAge
}

// requireCSRFHeader 对非只读方法要求 X-Requested-With: xingmang。
func requireCSRFHeader(req *http.Request) error {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if req.Header.Get(csrfHeaderName) != csrfHeaderValue {
		return action.NewError(action.CodePermissionDenied, msgMissingCSRF,
			errors.New("missing or wrong X-Requested-With header"))
	}
	return nil
}
