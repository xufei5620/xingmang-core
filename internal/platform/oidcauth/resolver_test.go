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
	// staff 默认还拿到**仅能读写自己**的个人 SavedView；它不扩大到任何业务数据。
	// offline_access / default-roles-* 仍静默忽略。
	if want := []string{"ops.read", "registry.read", "ui.saved_view.manage"}; !slices.Equal(p.Scopes, want) {
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
	if want := []string{"ops.read", "registry.read", "ui.saved_view.manage"}; !slices.Equal(p.Scopes, want) {
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
	admin, ok := m["admin"]
	if !ok {
		t.Fatal("默认表应保留 admin 位（Realm 里今天还没有这个角色）")
	}
	if !slices.Contains(staff, "registry.read") || !slices.Contains(staff, "ops.read") {
		t.Fatalf("staff 至少应有两个读权限, got %v", staff)
	}
	if slices.Contains(staff, "audit.read") {
		t.Fatal("staff 默认不该含 audit.read：PERMISSIONS.md 论证过它要单独授予")
	}
	// XM-0046：逐用户资金明细与 request.read 同一档，staff 同样不该默认拿到。
	// ops.read 看到的是聚合数字，这是逐条——「看板角色不应顺带看见全平台客户的账」
	if slices.Contains(staff, "platform.users.read") {
		t.Fatal("staff 默认不该含 platform.users.read：它是逐用户的资金明细，不是看板数字")
	}
	if slices.Contains(staff, "platform.user_keys.read") || slices.Contains(admin, "platform.user_keys.read") {
		t.Fatal("staff/admin 默认不该含 platform.user_keys.read：凭据库存必须独立授予")
	}
	if got := m["key-metadata-reader"]; !slices.Contains(got, "platform.user_keys.read") {
		t.Fatalf("专门的 key-metadata-reader 角色应显式映射 platform.user_keys.read, got %v", got)
	}
	// XM-CRED0：写明文凭据与切换真实上游默认仍不进 staff（credential-admin
	// 角色继续独立授予）。XM-LOGIN（2026-08-30）把这两项加进了 admin——
	// 见 DefaultRoleScopeMap 第 6 条：本地登录的 bootstrap 管理员需要一上线
	// 就能管凭据/连接器，不必先登录再手动申请第二个角色。credential-admin
	// 的最小面语义不受影响，两条路径并存。
	for _, sc := range []string{"credential.manage", "connector.manage"} {
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s：凭据写入与连接器切换必须独立授予", sc)
		}
		if got := m["credential-admin"]; !slices.Contains(got, sc) {
			t.Fatalf("专门的 credential-admin 角色应显式映射 %s, got %v", sc, got)
		}
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应含 %s（XM-LOGIN：bootstrap 管理员需要一上线就能用）, got %v", sc, admin)
		}
	}
	// XM-CARD0（2026-09-04）：卡片四权限。admin 全给，staff 一个都不给。
	//
	// 给 admin 的理由与上面 credential.manage 同一条：本地登录的 bootstrap
	// 管理员就是运营负责人，不给就等于功能对唯一能用它的人 403，而平台今天
	// 没有第二个角色可申请。card.reveal（明文卡面）一并给——它与 card.issue
	// 的分档意义在**对外开放时不下放**，那时外部用户走的是另一个角色，
	// 不是今天这张表。
	for _, sc := range []string{"card.read", "card.issue", "card.manage", "card.reveal"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应含 %s（否则卡片管理对唯一的管理员也是 403）, got %v", sc, admin)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s：开卡花真钱、卡面是明文，必须独立授予", sc)
		}
	}
	// XM-CARD6（2026-09-05）：提现权限**不给 admin**。
	//
	// 与卡片四权限的处理刻意不同。卡片那四个给 admin 的理由是「不给就等于
	// 功能对唯一能用它的人 403」；提现不适用同一条理由——它把钱转出平台、
	// 不可逆，而 admin 是日常操作账号。一个被盗用的 admin 会话不该能把
	// 资金池搬空。所以它由专门的 fund-operator 角色持有，需要人显式授予。
	//
	// 这与「定义了权限却没挂到任何角色」（那是 bug，功能对所有人 403）
	// 是两回事：这里挂在一个真实存在、可以被指派的角色上。
	for _, sc := range []string{"fund.withdraw", "fund.address.manage"} {
		if slices.Contains(admin, sc) {
			t.Fatalf("admin 不该含 %s：提现不可逆，日常操作账号不该持有它", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 更不该含 %s", sc)
		}
		if got := m["fund-operator"]; !slices.Contains(got, sc) {
			t.Fatalf("fund-operator 角色应持有 %s，否则这个功能没有任何角色能用, got %v", sc, got)
		}
	}

	// XM-SMS0（2026-09-05）：接码中心的四个权限。
	//
	// **sms.purchase 不给 admin**，与 fund.withdraw 同一条理由：它花真钱
	// 且不可退（买到的号就是买到了），而 admin 是日常操作账号。由专门的
	// sms-operator 角色持有。
	//
	// 其余三个给 admin：看清单、看号码、连接测试与人工核对都是日常运营
	// 要做的事，不给就等于这个功能对唯一能用它的人 403——这正是卡片上线
	// 当天撞上的那件事。
	for _, sc := range []string{"sms.read", "sms.reveal", "sms.manage"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应持有 %s，否则接码页对唯一能用它的人是 403", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 不该持有 %s", sc)
		}
	}
	if slices.Contains(admin, "sms.purchase") {
		t.Fatal("admin 不该持有 sms.purchase：买号花真钱，日常操作账号不该带它")
	}
	if got := m["sms-operator"]; !slices.Contains(got, "sms.purchase") {
		t.Fatalf("sms-operator 应持有 sms.purchase，否则这个功能没有任何角色能用, got %v", got)
	}

	// XM-EXT-PUBLISHING（2026-09-08）：内容发布的三个权限。
	//
	// **publishing.publish 不给 admin**，与 fund.withdraw / sms.purchase 同一条
	// 理由：对外发布不可逆、而且是公开的（删了也已经被抓取、被截图）。
	// 「谁能以公司的名义说话」是一次显式的组织授予，不该由「他是管理员」
	// 顺带获得。读与编辑给 admin，否则这一页对唯一能用它的人也是 403。
	for _, sc := range []string{"publishing.read", "publishing.manage"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应持有 %s，否则内容发布页对唯一能用它的人是 403", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 不该持有 %s：草稿正文与渠道登记不是看板数据", sc)
		}
	}
	if slices.Contains(admin, "publishing.publish") {
		t.Fatal("admin 不该持有 publishing.publish：对外发布不可逆，日常操作账号不该带它")
	}
	if slices.Contains(staff, "publishing.publish") {
		t.Fatal("staff 更不该持有 publishing.publish")
	}
	// 挂在一个真实存在、可以被指派的角色上——否则「不给 admin」就成了
	// 「谁都用不了」（那是 bug，不是最小权限）。
	if got := m["content-publisher"]; !slices.Contains(got, "publishing.publish") {
		t.Fatalf("content-publisher 应持有 publishing.publish，否则这个功能没有任何角色能用, got %v", got)
	}
	// 发布人也要看得见自己要发的东西，否则他必须同时被授予 admin 才用得起来。
	if got := m["content-publisher"]; !slices.Contains(got, "publishing.read") {
		t.Fatalf("content-publisher 应持有 publishing.read, got %v", got)
	}
	// 但发布人**不该**顺带拿到编辑权：改稿与发稿是两件事。
	if slices.Contains(m["content-publisher"], "publishing.manage") {
		t.Fatal("content-publisher 不该持有 publishing.manage：改稿与发稿刻意分开")
	}

	// XM-CARD6（2026-09-05 改）：额度从环境变量搬进数据库、由后台调整之后，
	// 「改不了」这道物理屏障没有了。替代它的是**两把钥匙**：
	//   fund.limit.manage（改额度）给 admin，
	//   fund.withdraw（发起提现）给 fund-operator。
	// 被盗用的 fund-operator 抬不高自己的天花板；被盗用的 admin 抬得高
	// 天花板却提不了现。合成一个权限就再也拆不开了。
	if !slices.Contains(admin, "fund.limit.manage") {
		t.Fatal("admin 应持有 fund.limit.manage：额度要有人能调，而调它的不该是提现的那个角色")
	}
	if slices.Contains(m["fund-operator"], "fund.limit.manage") {
		t.Fatal("fund-operator 不该持有 fund.limit.manage：那等于让提现的人自己抬高自己的上限")
	}
	if slices.Contains(staff, "fund.limit.manage") {
		t.Fatal("staff 不该持有 fund.limit.manage")
	}

	// XM-LOGIN：admin 管理本地登录账号，staff 不该有这个能力。
	if !slices.Contains(admin, "staff.manage") {
		t.Fatalf("admin 应含 staff.manage（XM-LOGIN 账号管理）, got %v", admin)
	}
	if slices.Contains(staff, "staff.manage") {
		t.Fatal("staff 默认不该含 staff.manage：账号管理是管理员能力")
	}
	for _, sc := range staff {
		if strings.Contains(sc, ".manage") && sc != "ui.saved_view.manage" {
			t.Fatalf("staff 默认不该含写权限: %q", sc)
		}
	}
	// 改这张表意味着改「登录进来的人默认能做什么」——不是重构，是授权决定
	// XM-0039：请求元数据与请求正文分两档，**admin 只拿前者**。
	//
	// 这条断言钉的是一次产品裁定（2026-08-28 验收）：看全平台用户对话正文的
	// 应该是显式授权的客诉/风控岗，不是每个管理员顺带获得的能力。
	// admin 角色今天在 Realm 里还不存在，所以这一行不会命中——正因为不会命中，
	// 才更需要一条测试拦住「以后顺手加回去」：没有人会回过头质疑一张已经跑了
	// 半年的默认表。要授予就用 XM_OIDC_ROLE_SCOPES 显式配一个专门的角色。
	if !slices.Contains(admin, "platform.users.read") {
		t.Fatalf("admin 应含 platform.users.read（用户管理页签的读权限）, got %v", admin)
	}
	if !slices.Contains(admin, "request.read") {
		t.Fatalf("admin 应含 request.read（请求元数据列表）, got %v", admin)
	}
	if !slices.Contains(admin, "ui.saved_view.manage") {
		t.Fatalf("admin 应含仅限自己的个人 SavedView 权限, got %v", admin)
	}
	if slices.Contains(staff, "finance.platform_channel_binding.manage") || slices.Contains(admin, "finance.platform_channel_binding.manage") {
		t.Fatal("渠道绑定 L1 写权限必须由人工显式授予，不能进入默认角色")
	}
	// XM-SERVER0：服务器登记簿的写权限**不属于**渠道绑定那一类必须人工显式
	// 授予的高风险面——它写的是主机名/规格/供应商/到期日这类纯记录字段
	// （拍板「服务器只做记录」），不触碰第三方系统、不影响成本或收入归属，
	// 与已经默认给 admin 的 finance.upstream_account.manage 同一档（登记簿，
	// 不是执行）。所以这里断言的方向与上面渠道绑定那条**相反**：admin 应该
	// 含它，staff 不该含它。
	if !slices.Contains(admin, "server.manage") {
		t.Fatal("admin 应含 server.manage：服务器登记簿是纯记录字段，" +
			"不触碰第三方系统，与 finance.upstream_account.manage 同一档，" +
			"不属于渠道绑定那类必须人工显式授予的高风险面")
	}
	if slices.Contains(staff, "server.manage") {
		t.Fatal("staff 默认不该含 server.manage：写权限只给 admin 与显式授权角色")
	}
	if slices.Contains(admin, "request.content.read") {
		t.Fatal("admin 默认**不该**含 request.content.read：" +
			"用户与模型的完整对话要显式授权给客诉/风控岗，" +
			"「admin 是全权角色」不构成理由（见 DefaultRoleScopeMap 注释第 4 条）")
	}
	// staff 更不该有这两项里的任何一项
	for _, sc := range []string{"request.read", "request.content.read"} {
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s", sc)
		}
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
