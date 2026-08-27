package oidcauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ---------------------------------------------------------------------------
// 合法令牌：Principal 每个字段都要对
// ---------------------------------------------------------------------------

func TestResolveMapsPrincipalFields(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	p, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil)))
	if err != nil {
		t.Fatalf("合法令牌被拒: %v", err)
	}

	// ID 取 preferred_username（CR-0001 关掉 Email as username，理由是「便于审计对齐」）
	if p.ID != "staff_alice" {
		t.Errorf("ID = %q, want staff_alice", p.ID)
	}
	// 不可变标识仍然留着，在 Subject 里
	if p.Subject != "9f1c0e7a-1111-4222-8333-444455556666" {
		t.Errorf("Subject = %q", p.Subject)
	}
	if p.Type != principal.TypeHuman {
		t.Errorf("Type = %q, want HUMAN", p.Type)
	}
	if p.IdentityZone != "staff" {
		t.Errorf("IdentityZone = %q, want staff", p.IdentityZone)
	}
	if p.Issuer != idp.issuer() {
		t.Errorf("Issuer = %q", p.Issuer)
	}
	if p.ClientID != "xingmang-admin-web" {
		t.Errorf("ClientID = %q（应取 azp）", p.ClientID)
	}
	// amr 里有 otp → mfa
	if p.AuthenticationLevel != "mfa" {
		t.Errorf("AuthenticationLevel = %q, want mfa", p.AuthenticationLevel)
	}
	// Environment 必须来自服务配置，绝不能从令牌读（规格 §20.5）
	if p.Environment != "staging" {
		t.Errorf("Environment = %q, want staging（服务配置）", p.Environment)
	}
	// staff 按默认表翻译成两个读权限；offline_access / default-roles-* 静默忽略
	if want := []string{"ops.read", "registry.read"}; !slices.Equal(p.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", p.Scopes, want)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("映射出的 Principal 不合法: %v", err)
	}
}

// 令牌自称 production 也没用：环境只认服务配置。
func TestResolveIgnoresEnvironmentInToken(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)
	c := idp.baseClaims()
	c["environment"] = "production"
	c["env"] = "production"

	p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Environment != "staging" {
		t.Fatalf("Environment = %q：令牌不得决定环境", p.Environment)
	}
}

// ---------------------------------------------------------------------------
// 拒绝路径
// ---------------------------------------------------------------------------

func TestResolveRejects(t *testing.T) {
	idp := newFakeIDP(t)
	otherIDP := newFakeIDP(t) // 用来造「另一个 Realm 的 issuer」
	r, _ := newTestResolver(t, idp, nil)

	// 一把不在 JWKS 里的私钥：用它签名等于伪造
	rogue := testKey(t, 3)

	cases := map[string]func() string{
		"错误签名（键不在 JWKS 里）": func() string {
			return signWith(t, rogue, "kid-1", idp.baseClaims(), nil)
		},
		"签名被篡改": func() string {
			tok := idp.sign("kid-1", idp.baseClaims(), nil)
			i := strings.LastIndexByte(tok, '.')
			// 改签名段的**第一个**字符而不是最后一个：base64url 的末位只承载
			// 两个有效比特，改它有可能解出同一串字节，用例会假绿
			first := tok[i+1]
			repl := byte('A')
			if first == 'A' {
				repl = 'B'
			}
			return tok[:i+1] + string(repl) + tok[i+2:]
		},
		"载荷被篡改（签名未重算）": func() string {
			c := idp.baseClaims()
			tok := idp.sign("kid-1", c, nil)
			parts := strings.Split(tok, ".")
			c["preferred_username"] = "staff_mallory"
			return parts[0] + "." + b64(t, c) + "." + parts[2]
		},
		"已过期": func() string {
			c := idp.baseClaims()
			c["exp"] = time.Now().Add(-10 * time.Minute).Unix()
			return idp.sign("kid-1", c, nil)
		},
		"缺少 exp": func() string {
			c := idp.baseClaims()
			delete(c, "exp")
			return idp.sign("kid-1", c, nil)
		},
		"nbf 在未来": func() string {
			c := idp.baseClaims()
			c["nbf"] = time.Now().Add(10 * time.Minute).Unix()
			return idp.sign("kid-1", c, nil)
		},
		"iat 在未来": func() string {
			c := idp.baseClaims()
			c["iat"] = time.Now().Add(10 * time.Minute).Unix()
			return idp.sign("kid-1", c, nil)
		},
		"iss 不符（另一个 Realm）": func() string {
			c := idp.baseClaims()
			c["iss"] = otherIDP.issuer()
			return idp.sign("kid-1", c, nil)
		},
		"iss 是前缀（solov-staff-test 之类）": func() string {
			c := idp.baseClaims()
			c["iss"] = idp.issuer() + "-test"
			return idp.sign("kid-1", c, nil)
		},
		"aud 与 azp 都不是本平台": func() string {
			c := idp.baseClaims()
			c["aud"] = []string{"account", "别的系统"}
			c["azp"] = "some-other-client"
			return idp.sign("kid-1", c, nil)
		},
		"缺少 sub": func() string {
			c := idp.baseClaims()
			delete(c, "sub")
			return idp.sign("kid-1", c, nil)
		},
		"sub 为空串": func() string {
			c := idp.baseClaims()
			c["sub"] = "   "
			return idp.sign("kid-1", c, nil)
		},
		"sub 含换行（日志注入）": func() string {
			c := idp.baseClaims()
			c["sub"] = "abc\ndef"
			return idp.sign("kid-1", c, nil)
		},
		"alg=none": func() string {
			c := idp.baseClaims()
			// 签名段给个非空值，好让用例真的走到 alg 白名单那一行
			return b64(t, map[string]any{"alg": "none", "kid": "kid-1"}) + "." + b64(t, c) + ".AAAA"
		},
		"alg=HS256（算法混淆）": func() string {
			return idp.sign("kid-1", idp.baseClaims(), map[string]any{"alg": "HS256"})
		},
		"缺少 kid": func() string {
			return idp.sign("kid-1", idp.baseClaims(), map[string]any{"kid": nil})
		},
		"kid 不存在": func() string {
			return idp.sign("kid-1", idp.baseClaims(), map[string]any{"kid": "kid-不存在"})
		},
		"crit 头无法处理": func() string {
			return idp.sign("kid-1", idp.baseClaims(), map[string]any{"crit": []string{"exp"}})
		},
		"不是三段式": func() string {
			return "abc.def"
		},
		"载荷不是 base64url": func() string {
			tok := idp.sign("kid-1", idp.baseClaims(), nil)
			parts := strings.Split(tok, ".")
			return parts[0] + ".@@@@." + parts[2]
		},
		"ID Token 冒充访问令牌": func() string {
			c := idp.baseClaims()
			c["typ"] = "ID"
			return idp.sign("kid-1", c, nil)
		},
		"刷新令牌冒充访问令牌": func() string {
			c := idp.baseClaims()
			c["typ"] = "Refresh"
			return idp.sign("kid-1", c, nil)
		},
		"服务账号（CR-0001 已关闭 Service accounts）": func() string {
			c := idp.baseClaims()
			c["preferred_username"] = "service-account-xingmang-admin-web"
			return idp.sign("kid-1", c, nil)
		},
	}

	for name, mint := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := r.Resolve(requestWithToken(mint()))
			if err == nil {
				t.Fatal("应被拒绝但通过了")
			}
			assertAuthError(t, err, msgInvalidToken)
		})
	}
}

func TestResolveRejectsMissingOrMalformedAuthorization(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	for name, set := range map[string]func(*http.Request){
		"没有 Authorization 头": func(*http.Request) {},
		"空 Bearer":           func(req *http.Request) { req.Header.Set("Authorization", "Bearer   ") },
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			set(req)
			_, err := r.Resolve(req)
			// 「没带令牌」可以直说：这是调用方自己知道的事实，
			// 前端据此跳登录而不是显示「令牌无效」
			assertAuthError(t, err, msgMissingToken)
		})
	}

	t.Run("不是 Bearer 形态", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		_, err := r.Resolve(req)
		assertAuthError(t, err, msgInvalidToken)
	})

	t.Run("令牌超长", func(t *testing.T) {
		_, err := r.Resolve(requestWithToken(strings.Repeat("a", maxTokenBytes+1)))
		assertAuthError(t, err, msgInvalidToken)
	})
}

// assertAuthError 断言错误是 403 语义的 Action 错误，且对外文案符合预期。
func assertAuthError(t *testing.T, err error, wantMsg string) {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，得到 nil")
	}
	var ae *action.Error
	if !errors.As(err, &ae) {
		t.Fatalf("错误不是 *action.Error（会被 httpapi 兜底成 500）: %T", err)
	}
	if ae.Code != action.CodePermissionDenied {
		t.Fatalf("Code = %q, want PERMISSION_DENIED", ae.Code)
	}
	if ae.Message != wantMsg {
		t.Fatalf("对外文案 = %q, want %q", ae.Message, wantMsg)
	}
}

// 失败原因必须**不可区分**：把「过期」「签名错」「aud 不符」分开告诉调用方，
// 等于白送一个探测 issuer/audience 配置的接口。
func TestResolveFailuresAreIndistinguishable(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	expired := idp.baseClaims()
	expired["exp"] = time.Now().Add(-time.Hour).Unix()
	badAud := idp.baseClaims()
	badAud["aud"] = "别人"
	badAud["azp"] = "别人"

	var msgs []string
	for _, c := range []map[string]any{expired, badAud} {
		_, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
		var ae *action.Error
		if !errors.As(err, &ae) {
			t.Fatalf("%T", err)
		}
		msgs = append(msgs, string(ae.Code)+"/"+ae.Message)
	}
	if msgs[0] != msgs[1] {
		t.Fatalf("不同失败原因对外文案不同，构成信息泄漏: %q vs %q", msgs[0], msgs[1])
	}
}

// 错误链（含只进服务端日志的根因）里绝不能出现令牌片段（宪法 7 条）。
func TestResolveErrorNeverLeaksToken(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	c := idp.baseClaims()
	c["exp"] = time.Now().Add(-time.Hour).Unix()
	token := idp.sign("kid-1", c, nil)

	_, err := r.Resolve(requestWithToken(token))
	chain := errorChain(err)
	for _, part := range strings.Split(token, ".") {
		if len(part) < 8 {
			continue
		}
		if strings.Contains(chain, part) {
			t.Fatalf("错误链里出现了令牌片段：%s", chain)
		}
	}
	// 明文声明也不该出现
	if strings.Contains(chain, "staff_alice") {
		t.Fatalf("错误链里出现了令牌内容：%s", chain)
	}
}

// ---------------------------------------------------------------------------
// 时钟偏移
// ---------------------------------------------------------------------------

func TestClockSkewTolerance(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, func(c *Config) { c.ClockSkew = 60 * time.Second })

	t.Run("刚过期 30 秒仍在容忍内", func(t *testing.T) {
		c := idp.baseClaims()
		c["exp"] = time.Now().Add(-30 * time.Second).Unix()
		if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil))); err != nil {
			t.Fatalf("60s 偏移容忍内应通过: %v", err)
		}
	})
	t.Run("过期 90 秒超出容忍", func(t *testing.T) {
		c := idp.baseClaims()
		c["exp"] = time.Now().Add(-90 * time.Second).Unix()
		if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil))); err == nil {
			t.Fatal("超出偏移容忍应被拒")
		}
	})
	t.Run("nbf 在 30 秒后仍在容忍内", func(t *testing.T) {
		c := idp.baseClaims()
		c["nbf"] = time.Now().Add(30 * time.Second).Unix()
		if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil))); err != nil {
			t.Fatalf("60s 偏移容忍内应通过: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 受众绑定：aud 或 azp
// ---------------------------------------------------------------------------

func TestAudienceBinding(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	for name, tc := range map[string]struct {
		aud    any
		azp    any
		accept bool
	}{
		"aud 数组含本平台":            {[]string{"account", "xingmang-admin-web"}, "别的", true},
		"aud 字符串就是本平台":          {"xingmang-admin-web", nil, true},
		"aud 是 account 但 azp 对": {"account", "xingmang-admin-web", true},
		"aud 与 azp 都不对":         {[]string{"account"}, "别的", false},
		"aud 缺失且 azp 不对":        {nil, "别的", false},
		"aud 缺失但 azp 对":         {nil, "xingmang-admin-web", true},
	} {
		t.Run(name, func(t *testing.T) {
			c := idp.baseClaims()
			if tc.aud == nil {
				delete(c, "aud")
			} else {
				c["aud"] = tc.aud
			}
			if tc.azp == nil {
				delete(c, "azp")
			} else {
				c["azp"] = tc.azp
			}
			_, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
			if tc.accept && err != nil {
				t.Fatalf("应通过: %v", err)
			}
			if !tc.accept && err == nil {
				t.Fatal("应被拒")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ADR-016：细粒度权限出现在令牌里 = 配置漂移，忽略 + warn
// ---------------------------------------------------------------------------

func TestFineGrainedRolesIgnoredAndWarned(t *testing.T) {
	idp := newFakeIDP(t)
	r, capture := newTestResolver(t, idp, nil)

	c := idp.baseClaims()
	c["realm_access"] = map[string]any{"roles": []string{
		"staff",
		"registry.service.manage", // Realm 里不该有的角色
		"ops.read",
		"audit.read",
	}}
	// 另外两处也塞上，确认巡检覆盖到
	c["scope"] = "openid profile registry.read"
	c["resource_access"] = map[string]any{
		"xingmang-admin-web": map[string]any{"roles": []string{"ops.write"}},
	}

	p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
	if err != nil {
		t.Fatalf("漂移不该导致拒绝（只忽略）: %v", err)
	}

	// 关键断言：细粒度角色一个都没被采纳，权限只能来自 staff 的翻译
	if want := []string{"ops.read", "registry.read"}; !slices.Equal(p.Scopes, want) {
		t.Fatalf("Scopes = %v, want %v（细粒度角色必须被忽略，不得叠加）", p.Scopes, want)
	}
	if p.HasScope("registry.service.manage") {
		t.Fatal("令牌里的 registry.service.manage 被当成了授权——ADR-016 红线")
	}
	if p.HasScope("audit.read") {
		t.Fatal("令牌里的 audit.read 被当成了授权——ADR-016 红线")
	}

	rec, ok := capture.find("oidc_fine_grained_role_ignored")
	if !ok {
		t.Fatalf("配置漂移必须记 warn，日志里没有:\n%s", capture.text())
	}
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if rec["error_code"] != "keycloak_scope_drift" {
		t.Errorf("error_code = %v", rec["error_code"])
	}
	if rec["adr"] != "ADR-016" {
		t.Errorf("warn 应指向 ADR-016, got %v", rec["adr"])
	}
	ignored, _ := rec["ignored"].([]any)
	var got []string
	for _, v := range ignored {
		got = append(got, v.(string))
	}
	want := []string{
		"registry.service.manage",
		"resource_access.xingmang-admin-web:ops.write",
		"scope:registry.read",
		"audit.read",
		"ops.read",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("warn 的 ignored 里缺 %q，实际 %v", w, got)
		}
	}
}

// 正常令牌不该刷屏：没有漂移就不记这条 warn。
func TestNoDriftNoWarn(t *testing.T) {
	idp := newFakeIDP(t)
	r, capture := newTestResolver(t, idp, nil)
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatal(err)
	}
	if _, ok := capture.find("oidc_fine_grained_role_ignored"); ok {
		t.Fatalf("无漂移却记了 warn:\n%s", capture.text())
	}
}

// ---------------------------------------------------------------------------
// RoleScopeMap 翻译
// ---------------------------------------------------------------------------

func TestRoleScopeMapTranslation(t *testing.T) {
	idp := newFakeIDP(t)
	custom := map[string][]string{
		"staff":     {"registry.read", "ops.read"},
		"auditor":   {"audit.read"},
		"registrar": {"registry.service.manage", "registry.read"},
	}
	r, _ := newTestResolver(t, idp, func(c *Config) { c.RoleScopeMap = custom })

	for name, tc := range map[string]struct {
		roles []string
		want  []string
	}{
		"单角色":     {[]string{"staff"}, []string{"ops.read", "registry.read"}},
		"多角色并集去重": {[]string{"staff", "registrar"}, []string{"ops.read", "registry.read", "registry.service.manage"}},
		"三角色":     {[]string{"staff", "auditor", "registrar"}, []string{"audit.read", "ops.read", "registry.read", "registry.service.manage"}},
		"未知角色被忽略": {[]string{"offline_access", "uma_authorization"}, nil},
		"角色顺序不影响结果": {
			[]string{"registrar", "auditor", "staff"},
			[]string{"audit.read", "ops.read", "registry.read", "registry.service.manage"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := idp.baseClaims()
			c["realm_access"] = map[string]any{"roles": tc.roles}
			p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !slices.Equal(p.Scopes, tc.want) {
				t.Fatalf("Scopes = %v, want %v", p.Scopes, tc.want)
			}
		})
	}
}

// 默认表刻意保守：staff 不含 audit.read（理由见 DefaultRoleScopeMap 注释）。
func TestDefaultRoleScopeMapIsConservative(t *testing.T) {
	m := DefaultRoleScopeMap()
	staff := m["staff"]
	if !slices.Contains(staff, "registry.read") || !slices.Contains(staff, "ops.read") {
		t.Fatalf("staff 至少应有两个读权限, got %v", staff)
	}
	if slices.Contains(staff, "audit.read") {
		t.Fatal("staff 默认不该含 audit.read：PERMISSIONS.md 论证过它要单独授予")
	}
	for _, sc := range staff {
		if strings.Contains(sc, ".manage") {
			t.Fatalf("staff 默认不该含写权限: %q", sc)
		}
	}
	// 改这张表意味着改「登录进来的人默认能做什么」——不是重构，是授权决定
	if _, ok := m["admin"]; !ok {
		t.Fatal("默认表应保留 admin 位（Realm 里今天还没有这个角色）")
	}
}

// ---------------------------------------------------------------------------
// acr / amr → AuthenticationLevel
// ---------------------------------------------------------------------------

func TestAuthenticationLevelMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		acr  string
		amr  []string
		want string
	}{
		"amr 含 otp":       {"1", []string{"pwd", "otp"}, "mfa"},
		"amr 含 mfa":       {"1", []string{"mfa"}, "mfa"},
		"amr 只有 pwd":      {"1", []string{"pwd"}, "password"},
		"acr 数字 2":        {"2", []string{"pwd"}, "mfa"},
		"acr 数字 1":        {"1", nil, "password"},
		"acr 数字 3":        {"3", nil, "mfa"},
		"acr 含 mfa 字样":    {"urn:solov:acr:mfa", nil, "mfa"},
		"acr 自定义非数字":      {"urn:mace:incommon:iap:silver", nil, "password"},
		"acr 缺失":          {"", nil, "password"},
		"acr 是 2fa 不当成 2": {"2fa", nil, "password"},
	} {
		t.Run(name, func(t *testing.T) {
			c := claims{ACR: flexString(tc.acr), AMR: tc.amr}
			if got := authenticationLevel(c); got != tc.want {
				t.Fatalf("authenticationLevel = %q, want %q", got, tc.want)
			}
		})
	}
}

// acr 被发成数字而不是字符串是个无害的表示差异，不该让所有人登不进来。
func TestACRAcceptsNumberForm(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)
	c := idp.baseClaims()
	c["acr"] = 2 // 数字，不是 "2"
	c["amr"] = []string{"pwd"}

	p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
	if err != nil {
		t.Fatalf("acr 发成数字不该导致拒绝: %v", err)
	}
	if p.AuthenticationLevel != "mfa" {
		t.Fatalf("AuthenticationLevel = %q, want mfa", p.AuthenticationLevel)
	}
}

// ---------------------------------------------------------------------------
// 用户名处理
// ---------------------------------------------------------------------------

func TestUsernameHandling(t *testing.T) {
	idp := newFakeIDP(t)
	r, capture := newTestResolver(t, idp, nil)

	t.Run("缺 preferred_username 回落 sub", func(t *testing.T) {
		c := idp.baseClaims()
		delete(c, "preferred_username")
		p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if p.ID != p.Subject {
			t.Fatalf("ID = %q, 应回落到 sub %q", p.ID, p.Subject)
		}
	})

	t.Run("用户名带换行时回落 sub 并 warn", func(t *testing.T) {
		c := idp.baseClaims()
		// 换行能在文本日志里伪造出一整行「别人的操作」
		c["preferred_username"] = "alice\n2026-08-27 admin deleted everything"
		p, err := r.Resolve(requestWithToken(idp.sign("kid-1", c, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if p.ID != p.Subject {
			t.Fatalf("含控制字符的用户名不该进 Principal.ID, got %q", p.ID)
		}
		if _, ok := capture.find("oidc_username_unsafe_fallback_to_sub"); !ok {
			t.Fatalf("应记 warn:\n%s", capture.text())
		}
	})
}
