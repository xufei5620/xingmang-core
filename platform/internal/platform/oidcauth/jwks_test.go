package oidcauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// kid 轮换
// ---------------------------------------------------------------------------

// Keycloak 轮换签名键时会先把新键加进 JWKS 再开始用它签。平台必须能在遇到
// 陌生 kid 时重新拉一次，否则每次轮换都等于一次全员掉线。
func TestKidRotationTriggersRefresh(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, nil)

	// 先用旧键跑通一次，把 JWKS 缓存起来
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("旧键应通过: %v", err)
	}
	_, jwksBefore := idp.hits()
	if jwksBefore != 1 {
		t.Fatalf("首次校验应拉一次 JWKS, got %d", jwksBefore)
	}

	// 上游轮换：加一把新键
	idp.addKey("kid-2", testKey(t, 1))
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-2", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("新 kid 应触发刷新后通过: %v", err)
	}
	if _, jwksAfter := idp.hits(); jwksAfter != 2 {
		t.Fatalf("陌生 kid 应触发一次刷新, JWKS 命中 %d 次", jwksAfter)
	}

	// 刷新之后旧键仍在 JWKS 里，应继续可用（轮换期两把键并存）
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("轮换期旧键应仍可用: %v", err)
	}
	if _, jwksAfter := idp.hits(); jwksAfter != 2 {
		t.Fatalf("命中缓存不该再拉 JWKS, got %d", jwksAfter)
	}
}

// 上游撤下一把键，缓存到期后平台必须跟着不再认它——整体替换而不是合并。
func TestRetiredKeyStopsWorkingAfterTTL(t *testing.T) {
	idp := newFakeIDP(t)
	idp.addKey("kid-2", testKey(t, 1))
	r, _ := newTestResolver(t, idp, func(c *Config) {
		c.JWKSTTL = time.Minute
		c.JWKSMaxAge = time.Hour
	})

	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatal(err)
	}

	// 上游撤下 kid-1，并把时间推过 TTL
	idp.removeKey("kid-1")
	r.keys.now = func() time.Time { return time.Now().Add(2 * time.Minute) }

	if _, err := r.Resolve(requestWithToken(idp.sign("kid-2", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("仍在 JWKS 里的键应可用: %v", err)
	}
	// kid-1 的私钥还在测试手里，但公钥已被上游撤下
	tok := signWith(t, testKey(t, 0), "kid-1", idp.baseClaims(), nil)
	if _, err := r.Resolve(requestWithToken(tok)); err == nil {
		t.Fatal("被撤下的键必须失效——缓存是整体替换，不是合并")
	}
}

// ---------------------------------------------------------------------------
// 刷新冷却：防止「随机 kid 打一轮 = 对 Keycloak 发起等量请求」的放大攻击
// ---------------------------------------------------------------------------

func TestRefreshCooldownSuppressesFetchStorm(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, func(c *Config) { c.JWKSRefreshCooldown = 30 * time.Second })

	// 先建立缓存
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatal(err)
	}
	_, base := idp.hits()

	// 连打 20 个陌生 kid
	for range 20 {
		tok := idp.sign("kid-1", idp.baseClaims(), map[string]any{"kid": "kid-伪造"})
		if _, err := r.Resolve(requestWithToken(tok)); err == nil {
			t.Fatal("伪造 kid 应被拒")
		}
	}
	_, after := idp.hits()
	if after-base > 1 {
		t.Fatalf("冷却期内最多再拉一次 JWKS，实际拉了 %d 次——这是个放大攻击面", after-base)
	}
}

// ---------------------------------------------------------------------------
// 上游抖动：短暂不可用先用旧键顶住，超过硬上限就不再认
// ---------------------------------------------------------------------------

func TestServesStaleKeysWhenUpstreamDownThenGivesUp(t *testing.T) {
	idp := newFakeIDP(t)
	r, capture := newTestResolver(t, idp, func(c *Config) {
		c.JWKSTTL = time.Minute
		c.JWKSMaxAge = 30 * time.Minute
	})
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatal(err)
	}

	// 上游开始 500，同时时间越过软 TTL
	idp.setJWKSStatus(http.StatusInternalServerError)
	r.keys.now = func() time.Time { return time.Now().Add(5 * time.Minute) }

	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("软 TTL 内上游抖动应先用旧键顶住: %v", err)
	}
	if _, ok := capture.find("oidc_jwks_refresh_failed_serving_stale"); !ok {
		t.Fatalf("用旧键顶住必须留 warn:\n%s", capture.text())
	}

	// 越过硬上限：不再拿一份不知道还算不算数的键放行
	r.keys.now = func() time.Time { return time.Now().Add(45 * time.Minute) }
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err == nil {
		t.Fatal("超过 JWKSMaxAge 后必须拒绝，不能无限期用旧键")
	}
}

// ---------------------------------------------------------------------------
// 发现文档：不可信的上游不能决定我们去哪儿取键
// ---------------------------------------------------------------------------

func TestDiscoveryRejectsIssuerMismatch(t *testing.T) {
	idp := newFakeIDP(t)
	idp.setIssuerOverride("https://auth.example.com/realms/别的")
	r, _ := newTestResolver(t, idp, nil)

	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err == nil {
		t.Fatal("发现文档的 issuer 与配置不一致时必须拒绝（OIDC Discovery §4.3）")
	}
}

func TestDiscoveryRejectsCrossOriginJWKSURI(t *testing.T) {
	idp := newFakeIDP(t)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{}})
	}))
	t.Cleanup(evil.Close)
	idp.setJWKSURIOverride(evil.URL + "/certs")

	r, _ := newTestResolver(t, idp, nil)
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err == nil {
		t.Fatal("jwks_uri 指向别的源时必须拒绝：否则发现端点被劫持就等于换掉了信任根")
	}
}

// 显式配置 JWKSURL 时不走发现，省一次往返，也让隔离网络能用。
func TestExplicitJWKSURLSkipsDiscovery(t *testing.T) {
	idp := newFakeIDP(t)
	r, _ := newTestResolver(t, idp, func(c *Config) {
		c.JWKSURL = idp.issuer() + "/protocol/openid-connect/certs"
	})
	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatal(err)
	}
	if discovery, jwks := idp.hits(); discovery != 0 || jwks != 1 {
		t.Fatalf("显式配置 JWKS 时不该访问发现端点: discovery=%d jwks=%d", discovery, jwks)
	}
}

func TestSameOriginAs(t *testing.T) {
	const issuer = "https://auth.solov.cc/realms/solov-staff"
	for name, tc := range map[string]struct {
		candidate string
		ok        bool
	}{
		"同源":       {"https://auth.solov.cc/realms/solov-staff/protocol/openid-connect/certs", true},
		"大小写主机名":   {"https://AUTH.solov.cc/x", true},
		"换主机":      {"https://evil.example.com/certs", false},
		"降级到 http": {"http://auth.solov.cc/certs", false},
		"相对路径":     {"/certs", false},
		"空串":       {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := sameOriginAs(issuer, tc.candidate)
			if tc.ok != (err == nil) {
				t.Fatalf("sameOriginAs(%q) err = %v", tc.candidate, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// JWK 解析
// ---------------------------------------------------------------------------

func TestJWKRejectsWeakOrMalformedKeys(t *testing.T) {
	good := jwkFor("kid-1", &testKey(t, 0).PublicKey)
	for name, mutate := range map[string]func(map[string]any){
		"模数不是 base64url": func(m map[string]any) { m["n"] = "@@@" },
		"指数不是 base64url": func(m map[string]any) { m["e"] = "@@@" },
		"模数为空":           func(m map[string]any) { m["n"] = "" },
		"指数为空":           func(m map[string]any) { m["e"] = "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range good {
				m[k] = v
			}
			mutate(m)
			k := jwk{Kty: "RSA", Kid: "kid-1", N: m["n"].(string), E: m["e"].(string)}
			if _, err := k.publicKey(); err == nil {
				t.Fatal("畸形 JWK 应被拒")
			}
		})
	}

	t.Run("1024 位 RSA 被拒", func(t *testing.T) {
		// 直接构造一个短模数：512 位的 n
		short := jwk{Kty: "RSA", Kid: "weak", N: strings.Repeat("A", 86), E: "AQAB"}
		if _, err := short.publicKey(); err == nil {
			t.Fatal("低于 2048 位的 RSA 公钥必须被拒")
		}
	})
}

// JWKS 里混着非 RS256 的键不该让整份作废——轮换期这很常见。
func TestJWKSIgnoresNonSigningKeys(t *testing.T) {
	idp := newFakeIDP(t)
	// 单独起一个 JWKS 端点，混入一把 EC 键与一把 use=enc 的 RSA 键
	enc := jwkFor("kid-enc", &testKey(t, 2).PublicKey)
	enc["use"] = "enc"
	sig := jwkFor("kid-1", &testKey(t, 0).PublicKey)
	ecKey := map[string]any{"kty": "EC", "kid": "kid-ec", "crv": "P-256", "x": "AA", "y": "BB"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/certs") {
			writeJSON(w, map[string]any{"keys": []any{ecKey, enc, sig}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	r, _ := newTestResolver(t, idp, func(c *Config) {
		c.IssuerURL = idp.issuer()
		c.JWKSURL = idp.issuer() + "/protocol/openid-connect/certs"
	})
	// 换掉 keyCache 的 JWKS 地址指向混合键集（同源限制在这里不适用：
	// 我们直接构造 keyCache，绕过配置校验只为验证筛选逻辑）
	r.keys.jwksURL = srv.URL + "/certs"

	if _, err := r.Resolve(requestWithToken(idp.sign("kid-1", idp.baseClaims(), nil))); err != nil {
		t.Fatalf("混合键集里的 RS256 签名键应可用: %v", err)
	}
	if _, ok := r.keys.keys["kid-enc"]; ok {
		t.Fatal("use=enc 的键不该被拿来验签")
	}
	if _, ok := r.keys.keys["kid-ec"]; ok {
		t.Fatal("非 RSA 键不该进签名键集")
	}
}
