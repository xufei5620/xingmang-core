package oidcauth

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 假 IdP：httptest 起的发现端点 + JWKS + 自签 RSA 键
//
// 全程不连任何真实 Keycloak（CR-0001 是人执行的，代码侧碰它是红线）。
// 这里模仿的是 Keycloak 的**形状**：/.well-known/openid-configuration 指向
// /protocol/openid-connect/certs，令牌的 azp/realm_access/acr/amr 布局逐字对齐
// CR-0001「验证」第 4 条列出的样子。
// ---------------------------------------------------------------------------

// 生成 2048 位 RSA 键要几百毫秒，整个包共用一个池子，别让每个用例各生成一遍。
var (
	keyPoolOnce sync.Once
	keyPool     []*rsa.PrivateKey
)

func testKey(t *testing.T, i int) *rsa.PrivateKey {
	t.Helper()
	keyPoolOnce.Do(func() {
		for range 4 {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			keyPool = append(keyPool, k)
		}
	})
	return keyPool[i]
}

type fakeIDP struct {
	t      *testing.T
	server *httptest.Server

	mu            sync.Mutex
	keys          map[string]*rsa.PrivateKey
	jwksHits      int
	discoveryHits int

	// 下面几个钩子用于构造「发现文档被做过手脚」的场景
	issuerOverride  string
	jwksURIOverride string
	jwksStatus      int
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	f := &fakeIDP{t: t, keys: map[string]*rsa.PrivateKey{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.discoveryHits++
		iss := f.issuerOverride
		jwks := f.jwksURIOverride
		f.mu.Unlock()
		if iss == "" {
			iss = f.issuer()
		}
		if jwks == "" {
			jwks = f.issuer() + "/protocol/openid-connect/certs"
		}
		writeJSON(w, map[string]any{"issuer": iss, "jwks_uri": jwks})
	})
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.jwksHits++
		status := f.jwksStatus
		keys := make([]any, 0, len(f.keys))
		for kid, k := range f.keys {
			keys = append(keys, jwkFor(kid, &k.PublicKey))
		}
		f.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, map[string]any{"keys": keys})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.addKey("kid-1", testKey(t, 0))
	return f
}

func (f *fakeIDP) issuer() string { return f.server.URL }

func (f *fakeIDP) addKey(kid string, k *rsa.PrivateKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[kid] = k
}

func (f *fakeIDP) removeKey(kid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, kid)
}

// 下面三个 setter 都加锁：handler 在另一个 goroutine 里读这些字段，
// 测试直接赋值将来跑 -race 会被点名。

func (f *fakeIDP) setIssuerOverride(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issuerOverride = v
}

func (f *fakeIDP) setJWKSURIOverride(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jwksURIOverride = v
}

func (f *fakeIDP) setJWKSStatus(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jwksStatus = code
}

func (f *fakeIDP) hits() (discovery, jwks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoveryHits, f.jwksHits
}

// baseClaims 是一张「一切正常」的员工令牌，对齐 CR-0001 的验证清单。
func (f *fakeIDP) baseClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": f.issuer(),
		"sub": "9f1c0e7a-1111-4222-8333-444455556666",
		// Keycloak 的 public client 默认不会把 Client ID 写进 aud，
		// 而是写进 azp——这里刻意复现那个形状
		"aud":                "account",
		"azp":                "xingmang-admin-web",
		"exp":                now.Add(5 * time.Minute).Unix(),
		"iat":                now.Unix(),
		"nbf":                now.Add(-time.Minute).Unix(),
		"typ":                "Bearer",
		"preferred_username": "staff_alice",
		"acr":                "1",
		"amr":                []string{"pwd", "otp"},
		"scope":              "openid profile email",
		"realm_access": map[string]any{
			"roles": []string{"staff", "offline_access", "default-roles-solov-staff"},
		},
	}
}

// sign 用指定 kid 的私钥签一张令牌。headerOverride 为 nil 时用标准 RS256 头。
func (f *fakeIDP) sign(kid string, claims map[string]any, headerOverride map[string]any) string {
	f.t.Helper()
	f.mu.Lock()
	key := f.keys[kid]
	f.mu.Unlock()
	if key == nil {
		f.t.Fatalf("测试用例引用了不存在的 kid %q", kid)
	}
	return signWith(f.t, key, kid, claims, headerOverride)
}

// signWith 允许用**不在 JWKS 里**的私钥签名，用于伪造签名的用例。
func signWith(t *testing.T, key *rsa.PrivateKey, kid string, claims, headerOverride map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": algRS256, "typ": "JWT", "kid": kid}
	for k, v := range headerOverride {
		if v == nil {
			delete(header, k)
			continue
		}
		header[k] = v
	}
	signingInput := b64(t, header) + "." + b64(t, claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func b64(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func jwkFor(kid string, pub *rsa.PublicKey) map[string]any {
	eb := make([]byte, 8)
	binary.BigEndian.PutUint64(eb, uint64(pub.E))
	// 去掉前导零，还原成 RFC 7518 的变长大端形态
	i := 0
	for i < len(eb)-1 && eb[i] == 0 {
		i++
	}
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": algRS256,
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(new(big.Int).Set(pub.N).Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(eb[i:]),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Resolver 装配助手
// ---------------------------------------------------------------------------

// logCapture 把 Resolver 的日志收进内存，供「必须记 warn」的断言使用。
type logCapture struct {
	buf *bytes.Buffer
}

func newLogCapture() *logCapture { return &logCapture{buf: &bytes.Buffer{}} }

func (l *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logCapture) text() string { return l.buf.String() }

// records 解析出每一条日志，便于按字段断言而不是靠子串碰运气。
func (l *logCapture) records() []map[string]any {
	var out []map[string]any
	for _, line := range bytes.Split(l.buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func (l *logCapture) find(msg string) (map[string]any, bool) {
	for _, r := range l.records() {
		if r["msg"] == msg {
			return r, true
		}
	}
	return nil, false
}

// newTestResolver 造一个指向假 IdP 的 Resolver。
//
// JWKSRefreshCooldown 设成 1ns 而不是 0：0 在 Config 里表示「没填，用默认值」，
// 传 0 会拿到 15 秒的真实冷却，轮换类用例就永远刷不到新键。
func newTestResolver(t *testing.T, idp *fakeIDP, mutate func(*Config)) (*Resolver, *logCapture) {
	t.Helper()
	capture := newLogCapture()
	cfg := Config{
		IssuerURL:           idp.issuer(),
		Audience:            "xingmang-admin-web",
		Environment:         "staging",
		Logger:              capture.logger(),
		HTTPClient:          idp.server.Client(),
		JWKSRefreshCooldown: time.Nanosecond,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := NewOIDCResolver(cfg)
	if err != nil {
		t.Fatalf("NewOIDCResolver: %v", err)
	}
	return r, capture
}

func requestWithToken(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// errorChain 把错误链拍平成一个字符串，用于「不得泄漏令牌」的断言。
func errorChain(err error) string {
	var b bytes.Buffer
	for e := err; e != nil; {
		b.WriteString(e.Error())
		b.WriteByte('\n')
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return b.String()
}
