package oidcauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// jwksBodyLimit 限制 JWKS / 发现文档的读取大小。
	// 上游被劫持或配错地址时，一个无限大的响应体不该把进程的内存吃光。
	jwksBodyLimit = 1 << 20 // 1 MiB

	// minRSABits 是可接受的最小 RSA 模数长度。
	// 512/1024 位的 RSA 今天已经不构成签名保证；宁可拒绝也不要「验过了」。
	minRSABits = 2048
)

// errKeyNotFound：JWKS 里没有这个 kid（可能是轮换，也可能是伪造的 kid）。
var errKeyNotFound = errors.New("jwks: kid not found")

// jwk 是 JWKS 里的一把键。只关心 RSA 签名键，其余字段忽略。
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// discoveryDocument 只取本包需要的两个字段。
type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// keyCache 是带软 TTL 的 JWKS 缓存。
//
// 三条时间线，各解决一个具体问题：
//
//	ttl      —— 到期后主动刷一次。解决「Keycloak 删掉一把键，平台还在认它」；
//	cooldown —— 两次网络拉取之间的最小间隔。没有它，攻击者拿随机 kid 打一轮
//	            就能让平台对 Keycloak 发起等量请求（放大攻击）；
//	maxAge   —— 刷新失败时还能拿旧键顶多久。Keycloak 抖一下不该让整个管理后台
//	            立刻掉线（令牌本身只有 5 分钟寿命，风险窗口有界），但也不能
//	            无限期用一份不知道还算不算数的键。
type keyCache struct {
	cfg    Config
	client *http.Client
	logger *slog.Logger
	now    func() time.Time

	// fetchMu 保证同一时刻只有一次网络拉取在途。
	// 拿不到锁的请求**不排队**直接判失败：排队会在 Keycloak 抖动时把整条鉴权
	// 路径的延迟拉到超时上限，而调用方重试一次的代价小得多。
	fetchMu sync.Mutex

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	lastTry   time.Time
	jwksURL   string // 发现结果缓存；显式配置时直接就是它
}

func newKeyCache(cfg Config, client *http.Client, logger *slog.Logger) *keyCache {
	return &keyCache{
		cfg:     cfg,
		client:  client,
		logger:  logger,
		now:     time.Now,
		jwksURL: cfg.JWKSURL,
	}
}

// lookup 从缓存取键，同时报告这份缓存是否已过软 TTL。
func (c *keyCache) lookup(kid string) (key *rsa.PublicKey, ok, stale bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k, found := c.keys[kid]
	if !found {
		return nil, false, false
	}
	age := c.now().Sub(c.fetchedAt)
	if age >= c.cfg.JWKSMaxAge {
		// 超过硬上限：这份键旧到不该再拿它签发通行证
		return nil, false, true
	}
	return k, true, age >= c.cfg.JWKSTTL
}

// keyFor 返回 kid 对应的公钥，必要时刷新 JWKS。
func (c *keyCache) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok, stale := c.lookup(kid); ok && !stale {
		return key, nil
	}

	if !c.fetchMu.TryLock() {
		// 已有一次刷新在途。手上有可用的旧键就先用着，没有就判失败——
		// 这里排队等待只会把并发请求全都压在同一个网络超时上
		if key, ok, _ := c.lookup(kid); ok {
			return key, nil
		}
		return nil, errKeyNotFound
	}
	defer c.fetchMu.Unlock()

	// 拿到锁后再看一眼：可能刚被上一个持锁者刷进来了
	if key, ok, stale := c.lookup(kid); ok && !stale {
		return key, nil
	}

	if c.now().Sub(c.lastTry) >= c.cfg.JWKSRefreshCooldown {
		if err := c.refresh(ctx); err != nil {
			// 刷新失败但手上还有未超硬上限的旧键 —— 先顶住，把失败记成 warn。
			// 这是刻意的可用性取舍：令牌寿命 5 分钟（CR-0001），旧键的风险窗口
			// 有界，而让管理后台在身份服务抖动时整体掉线的代价更大
			if key, ok, _ := c.lookup(kid); ok {
				c.logger.Warn("oidc_jwks_refresh_failed_serving_stale",
					slog.String("module", "oidcauth"),
					slog.String("error_code", "jwks_refresh_failed"),
					slog.Any("err", err))
				return key, nil
			}
			return nil, err
		}
	}

	if key, ok, _ := c.lookup(kid); ok {
		return key, nil
	}
	return nil, errKeyNotFound
}

// refresh 拉取 JWKS 并整体替换缓存。
//
// 整体替换而不是合并：Keycloak 撤下一把键就是要它立刻失效，合并会让被撤下的键
// 在缓存里活到进程重启。
func (c *keyCache) refresh(ctx context.Context) error {
	c.mu.Lock()
	c.lastTry = c.now()
	c.mu.Unlock()

	jwksURL, err := c.resolveJWKSURL(ctx)
	if err != nil {
		return err
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := c.getJSON(ctx, jwksURL, &doc); err != nil {
		return fmt.Errorf("拉取 JWKS 失败: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		// 只接受 RS256 签名键。加密键（use=enc）与 EC/OKP 键即便在同一份 JWKS 里
		// 也不该被拿来验签——alg 与 use 的用途分离正是 JWK 设计的意义
		if !strings.EqualFold(k.Kty, "RSA") {
			continue
		}
		if k.Use != "" && !strings.EqualFold(k.Use, "sig") {
			continue
		}
		if k.Alg != "" && !strings.EqualFold(k.Alg, algRS256) {
			continue
		}
		if k.Kid == "" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			// 单把键坏掉不该让整份 JWKS 作废：轮换期间混进一把新算法的键很常见
			c.logger.Warn("oidc_jwks_key_skipped",
				slog.String("module", "oidcauth"),
				slog.String("error_code", "jwks_key_invalid"),
				slog.String("kid", k.Kid),
				slog.Any("err", err))
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("JWKS 中没有可用的 RS256 公钥")
	}

	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = c.now()
	c.mu.Unlock()
	return nil
}

// resolveJWKSURL 返回 JWKS 地址：显式配置优先，否则走 issuer 的发现文档。
func (c *keyCache) resolveJWKSURL(ctx context.Context) (string, error) {
	c.mu.RLock()
	cached := c.jwksURL
	c.mu.RUnlock()
	if cached != "" {
		return cached, nil
	}

	discoveryURL := strings.TrimSuffix(c.cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	var doc discoveryDocument
	if err := c.getJSON(ctx, discoveryURL, &doc); err != nil {
		return "", fmt.Errorf("读取 OIDC 发现文档失败: %w", err)
	}
	// 发现文档里的 issuer 必须和我们配置的完全一致（OIDC Discovery §4.3）。
	// 不校验的话，一个被劫持的发现端点可以把我们指到它自己的 JWKS 上，
	// 后面所有的「签名验过了」就都是它说了算
	if doc.Issuer != c.cfg.IssuerURL {
		return "", fmt.Errorf("发现文档的 issuer 与配置不一致：配置 %q", c.cfg.IssuerURL)
	}
	if err := sameOriginAs(c.cfg.IssuerURL, doc.JWKSURI); err != nil {
		return "", fmt.Errorf("发现文档的 jwks_uri 不可信: %w", err)
	}

	c.mu.Lock()
	c.jwksURL = doc.JWKSURI
	c.mu.Unlock()
	return doc.JWKSURI, nil
}

// sameOriginAs 要求 candidate 与 issuer 同源（scheme + host + 端口）。
//
// 这是一道 SSRF 闸：发现文档是从网络上读来的，它说 jwks_uri 在哪就去哪，等于把
// 「平台会主动访问哪个地址」的决定权交给了上游。Keycloak 的 JWKS 本来就和
// Realm 同源，这条限制不会误伤真实部署。
func sameOriginAs(issuer, candidate string) error {
	iu, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("issuer 不是合法 URL: %w", err)
	}
	cu, err := url.Parse(candidate)
	if err != nil {
		return fmt.Errorf("不是合法 URL: %w", err)
	}
	if !cu.IsAbs() {
		return errors.New("必须是绝对 URL")
	}
	if !strings.EqualFold(iu.Scheme, cu.Scheme) || !strings.EqualFold(iu.Host, cu.Host) {
		return fmt.Errorf("与 issuer 不同源（issuer 为 %s://%s）", iu.Scheme, iu.Host)
	}
	return nil
}

// getJSON 发一次 GET 并解析 JSON，响应体大小受限。
func (c *keyCache) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksBodyLimit))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// publicKey 把 JWK 的 n/e 还原成 RSA 公钥。
func (k jwk) publicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("模数 n 不是合法 base64url: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("指数 e 不是合法 base64url: %w", err)
	}
	if len(eBytes) == 0 || len(eBytes) > 8 {
		return nil, errors.New("指数 e 长度非法")
	}
	// e 是变长大端整数（RFC 7518 §6.3.1.2），左侧补零到 8 字节再按 uint64 读
	padded := make([]byte, 8)
	copy(padded[8-len(eBytes):], eBytes)
	e := binary.BigEndian.Uint64(padded)
	if e < 3 || e > 1<<31 {
		return nil, fmt.Errorf("指数 e 超出合理范围")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e)}
	if pub.N.Sign() <= 0 {
		return nil, errors.New("模数 n 非正")
	}
	if pub.N.BitLen() < minRSABits {
		return nil, fmt.Errorf("RSA 模数只有 %d 位，低于下限 %d 位", pub.N.BitLen(), minRSABits)
	}
	return pub, nil
}

// verifyRS256 校验 signingInput 上的 RS256 签名。
func verifyRS256(pub *rsa.PublicKey, signingInput, signature []byte) error {
	sum := sha256.Sum256(signingInput)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], signature)
}
