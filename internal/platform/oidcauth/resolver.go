package oidcauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	algRS256 = "RS256"

	// identityZoneStaff：本 Resolver 只服务员工身份域（ADR-005）。
	//
	// 这不是「默认值」而是绑定关系：终端用户 Realm（solov）的令牌绝不能在这里
	// 通过——挡住它的是下面 iss 的**精确相等**校验，不是这个常量。两者一起读：
	// 一个 Resolver 实例 = 一个 Realm = 一个身份域。
	identityZoneStaff = "staff"

	// maxTokenBytes：Bearer 令牌的长度上限。
	// Keycloak 的 Access Token 通常 1~4 KB；给到 16 KB 已经很宽松，
	// 再大的东西没必要走 base64 解码与 JSON 解析
	maxTokenBytes = 16 << 10

	// 默认值。都能被 Config 覆盖，测试会用到。
	defaultClockSkew           = 60 * time.Second
	defaultHTTPTimeout         = 5 * time.Second
	defaultJWKSTTL             = 10 * time.Minute
	defaultJWKSRefreshCooldown = 15 * time.Second
	defaultJWKSMaxAge          = time.Hour
)

// 对外文案。**只有两种**：有没有带令牌，是调用方自己知道的事实，可以直说；
// 令牌为什么不合格则一律同一句——签名错、过期、aud 不符如果能分辨，
// 调用方就有了一个免费探测 issuer/audience 配置的接口。
const (
	msgMissingToken = "缺少身份"
	msgInvalidToken = "身份令牌无效"
)

// Config 是 OIDC Resolver 的装配参数。
type Config struct {
	// IssuerURL 是 Realm 的 issuer，必须与令牌 iss **完全相等**。
	// 例：https://auth.solov.cc/realms/solov-staff
	IssuerURL string

	// JWKSURL 可留空：留空则从 IssuerURL 的 /.well-known/openid-configuration 发现。
	// 显式配置用于「发现端点不可达但 JWKS 可达」的隔离网络。
	JWKSURL string

	// Audience 是本平台的受众标识（CR-0001 的 Client ID：xingmang-admin-web）。
	Audience string

	// Environment 取服务自身配置，绝不从令牌读（规格 §20.5）。
	Environment string

	// RoleScopeMap 是粗粒度 Realm 角色到平台细粒度 scope 的翻译表。
	// 留空用 DefaultRoleScopeMap()。
	RoleScopeMap map[string][]string

	// ClockSkew 是 exp/nbf/iat 的时钟偏移容忍，默认 60s。
	ClockSkew time.Duration

	// HTTPClient 用于发现与 JWKS 拉取，默认 5s 超时。
	HTTPClient *http.Client

	// Logger 记录配置漂移与 JWKS 异常；留空用 slog.Default()。
	Logger *slog.Logger

	// JWKS 缓存策略。留空取默认值，一般不需要动（语义见 keyCache 注释）。
	JWKSTTL             time.Duration
	JWKSRefreshCooldown time.Duration
	JWKSMaxAge          time.Duration
}

// Resolver 从 Bearer 令牌解析 Principal，实现 httpapi.PrincipalResolver。
type Resolver struct {
	cfg    Config
	keys   *keyCache
	logger *slog.Logger
	now    func() time.Time
}

// NewOIDCResolver 校验配置并构造 Resolver。
//
// **不发起任何网络请求**：发现与 JWKS 拉取都推迟到第一次校验令牌时。
// 这是刻意的——把启动挂在身份服务的可用性上，意味着 Keycloak 抖一下平台连
// /healthz 都起不来；而配置本身写错（issuer 不是绝对 URL、生产用了 http）
// 是不需要网络就能发现的，那部分在这里就炸掉。
func NewOIDCResolver(cfg Config) (*Resolver, error) {
	if strings.TrimSpace(cfg.IssuerURL) == "" {
		return nil, errors.New("IssuerURL 不能为空")
	}
	// 末尾斜杠会让 iss 的精确比较无声失败（Keycloak 发的 iss 不带斜杠），
	// 与其在运行时表现为「所有令牌都不合法」，不如在启动时说清楚
	if strings.HasSuffix(cfg.IssuerURL, "/") {
		return nil, fmt.Errorf("IssuerURL 不应以 / 结尾：令牌里的 iss 不带尾斜杠，会导致精确比较失败")
	}
	issuerURL, err := url.Parse(cfg.IssuerURL)
	if err != nil || !issuerURL.IsAbs() || issuerURL.Host == "" {
		return nil, fmt.Errorf("IssuerURL 必须是绝对 URL，例如 https://auth.solov.cc/realms/solov-staff")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("Audience 不能为空：不校验受众等于接受任何 Client 拿到的令牌")
	}
	if strings.TrimSpace(cfg.Environment) == "" {
		return nil, errors.New("Environment 不能为空（Principal 必须显式携带环境）")
	}
	// 生产必须 https：明文传的令牌等于没有令牌。非生产放行 http 是为了让
	// httptest 与本地 Keycloak 能跑，这条例外不能溜进生产
	if cfg.Environment == "production" && !strings.EqualFold(issuerURL.Scheme, "https") {
		return nil, fmt.Errorf("生产环境的 IssuerURL 必须是 https")
	}
	if cfg.JWKSURL != "" {
		if err := sameOriginAs(cfg.IssuerURL, cfg.JWKSURL); err != nil {
			return nil, fmt.Errorf("JWKSURL 不可用: %w", err)
		}
	}

	if cfg.RoleScopeMap == nil {
		cfg.RoleScopeMap = DefaultRoleScopeMap()
	}
	if len(cfg.RoleScopeMap) == 0 {
		return nil, errors.New("RoleScopeMap 为空：没有任何角色能翻译成权限，登录进来也什么都做不了")
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = defaultClockSkew
	}
	if cfg.JWKSTTL <= 0 {
		cfg.JWKSTTL = defaultJWKSTTL
	}
	// 注意是 <= 0 而不是 < 0：零值是「调用方没填」的常态（cmd/platform-api
	// 就不填），落成「无冷却」等于把 JWKS 刷新的放大攻击面默认打开。
	// 需要近似无冷却的测试显式传一个极小正值
	if cfg.JWKSRefreshCooldown <= 0 {
		cfg.JWKSRefreshCooldown = defaultJWKSRefreshCooldown
	}
	if cfg.JWKSMaxAge <= 0 {
		cfg.JWKSMaxAge = defaultJWKSMaxAge
	}
	if cfg.JWKSMaxAge < cfg.JWKSTTL {
		return nil, errors.New("JWKSMaxAge 不应小于 JWKSTTL")
	}
	client := cfg.HTTPClient
	if client == nil {
		// 规格 §18.1-4：所有外部 I/O 必须有超时。没有超时的 JWKS 拉取会把
		// 鉴权路径挂在上游的连接状态上
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := &Resolver{
		cfg:    cfg,
		logger: logger,
		now:    time.Now,
	}
	r.keys = newKeyCache(cfg, client, logger)

	// 映射表进启动日志：它是授权策略，出问题时第一个要看的就是「当时到底配的什么」
	logger.Info("oidc_resolver_ready",
		slog.String("module", "oidcauth"),
		slog.String("issuer", cfg.IssuerURL),
		slog.String("audience", cfg.Audience),
		slog.String("environment", cfg.Environment),
		slog.Any("role_scope_map", cfg.RoleScopeMap))
	return r, nil
}

// Resolve 校验 Bearer 令牌并映射成 Principal。
//
// 顺序是**先签名后声明**：任何在验签之前就相信的字段，都是攻击者可以随手改的。
func (r *Resolver) Resolve(req *http.Request) (principal.Principal, error) {
	raw, err := bearerToken(req)
	if err != nil {
		return principal.Principal{}, err
	}

	signingInput, sig, payload, kid, err := splitAndCheckHeader(raw)
	if err != nil {
		return principal.Principal{}, err
	}

	key, err := r.keys.keyFor(req.Context(), kid)
	if err != nil {
		// kid 找不到既可能是轮换未及时刷到，也可能是伪造。对外不区分
		return principal.Principal{}, authFailure("kid 未在 JWKS 中找到或 JWKS 不可用", err)
	}
	if err := verifyRS256(key, signingInput, sig); err != nil {
		return principal.Principal{}, authFailure("RS256 签名校验失败", nil)
	}

	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return principal.Principal{}, authFailure("载荷不是合法 JSON 或字段类型不符", nil)
	}
	if err := r.checkClaims(c); err != nil {
		return principal.Principal{}, err
	}
	return r.toPrincipal(req, c)
}

// checkClaims 做签名之后的声明校验，全部 Fail Closed。
func (r *Resolver) checkClaims(c claims) error {
	// iss 精确相等——这是「只接受 solov-staff Realm」的唯一执行点。
	// 前缀匹配会让 .../realms/solov-staff-test 之类的 Realm 混进来
	if c.Issuer != r.cfg.IssuerURL {
		return authFailure("iss 与配置的 issuer 不一致", nil)
	}

	// 受众绑定：aud 含本平台，或 azp 就是本平台。
	//
	// 为什么接受 azp：CR-0001 的 xingmang-admin-web 是 public client，Keycloak
	// 默认不会把 Client ID 写进 Access Token 的 aud（那里通常是 "account"），
	// 而是写进 azp——CR-0001 §验证第 4 条列出的正是 `azp = xingmang-admin-web`。
	// 只认 aud 的话，CR-0001 执行完当天所有令牌都会被拒，还得再开一张变更单去
	// 加 audience mapper。azp 表达的是「这张令牌发给了谁」，用于受众绑定是成立的。
	if !slices.Contains(c.Audience, r.cfg.Audience) && c.AuthorizedParty != r.cfg.Audience {
		return authFailure("aud 不含本平台且 azp 不是本平台", nil)
	}

	// 令牌类型：ID Token / Refresh Token 不是访问令牌。
	// 前端同时握着 ID Token 与 Access Token，发错的成本是「静默地用错误的
	// 受众绑定放行」，值得显式挡一道。typ 缺失不拒——不是所有实现都发它
	switch strings.ToLower(string(c.TokenType)) {
	case "id", "refresh":
		return authFailure("令牌类型不是访问令牌", nil)
	}

	now := r.now()
	skew := r.cfg.ClockSkew

	// exp 必填：没有 exp 的令牌永不过期，这种东西不该被接受
	if c.ExpiresAt == nil {
		return authFailure("缺少 exp", nil)
	}
	if !now.Add(-skew).Before(time.Unix(*c.ExpiresAt, 0)) {
		return authFailure("令牌已过期", nil)
	}
	if c.NotBefore != nil && now.Add(skew).Before(time.Unix(*c.NotBefore, 0)) {
		return authFailure("令牌尚未生效（nbf 在未来）", nil)
	}
	// iat 在未来说明签发方与平台的时钟差得离谱，或者令牌是编的
	if c.IssuedAt != nil && now.Add(skew).Before(time.Unix(*c.IssuedAt, 0)) {
		return authFailure("iat 在未来", nil)
	}

	if strings.TrimSpace(string(c.Subject)) == "" {
		return authFailure("缺少 sub", nil)
	}
	return nil
}

// toPrincipal 把已校验的声明映射成平台身份。
func (r *Resolver) toPrincipal(req *http.Request, c claims) (principal.Principal, error) {
	sub := strings.TrimSpace(string(c.Subject))
	username := strings.TrimSpace(c.PreferredUsername)

	// sub 也要过一遍字符检查。它来自我们信任的 Realm，正常是个 UUID——但它随后
	// 会进 Principal.ID、审计事件与下面几条日志，而审计链的价值就在于不能被
	// 写入方摆布。这里没有回落对象，所以是拒绝而不是降级
	if !safeIdentifier(sub) {
		return principal.Principal{}, authFailure("sub 含非法字符或过长", nil)
	}

	// Keycloak 的服务账号用户名固定是 service-account-<clientId>。
	// CR-0001 明确关掉了 Service accounts，这里出现就是配置漂移；而且 ADR-005
	// 说机器身份不复用人类账号，把它映射成 HUMAN 会让审计记录说谎
	if strings.HasPrefix(strings.ToLower(username), "service-account-") {
		r.logger.WarnContext(req.Context(), "oidc_service_account_rejected",
			slog.String("module", "oidcauth"),
			slog.String("error_code", "service_account_not_allowed"),
			slog.String("subject", sub))
		return principal.Principal{}, authFailure("员工 Realm 不接受服务账号令牌", nil)
	}

	// ID 用 preferred_username，Subject 保留 sub。
	//
	// CR-0001 把 "Email as username" 关成 OFF，理由写的就是「用显式用户名，
	// 便于审计对齐」；审计事件里的 principal_id 是给人读的，一串 UUID 让
	// 「谁改了这个服务」这个问题需要再查一次 Keycloak 才能回答。
	// 不可变标识没有丢——它在 Subject 里（代价见 AUTH-SWITCH.md 的已知取舍）。
	id := sub
	if username != "" && safeIdentifier(username) {
		id = username
	} else if username != "" {
		// 用户名带控制字符：不是正常账号。回落到 sub 而不是整体拒绝——
		// 拒绝会让一个畸形用户名变成「谁也登不进来」的故障
		r.logger.WarnContext(req.Context(), "oidc_username_unsafe_fallback_to_sub",
			slog.String("module", "oidcauth"),
			slog.String("error_code", "username_unsafe"),
			slog.String("subject", sub))
	}

	tr := translateRoles(c.RealmAccess.Roles, r.cfg.RoleScopeMap)

	// 漂移巡检覆盖令牌里三处可能塞权限的位置。realm_access 之外的两处平台从不
	// 采纳，扫它们纯粹是为了**尽早看见**有人在 Keycloak 里加了不该加的东西
	drift := append([]string(nil), tr.DriftRoles...)
	drift = append(drift, driftInScopeClaim(c.Scope)...)
	drift = append(drift, driftInResourceAccess(c.ResourceAccess)...)
	if len(drift) > 0 {
		slices.Sort(drift)
		drift = slices.Compact(drift)
		// 这条 warn 是 ADR-016 的探针：细粒度权限进了 Keycloak 就是治理事故，
		// 平台侧忽略它只是止血，真正的修复在 Realm 那边（需要一张变更单）
		r.logger.WarnContext(req.Context(), "oidc_fine_grained_role_ignored",
			slog.String("module", "oidcauth"),
			slog.String("error_code", "keycloak_scope_drift"),
			slog.String("subject", sub),
			slog.Any("ignored", drift),
			slog.String("adr", "ADR-016"),
			slog.String("hint", "Realm 中出现平台细粒度权限，属配置漂移；平台已忽略，请按 CR 流程移除"))
	}

	p := principal.Principal{
		ID:           id,
		Type:         principal.TypeHuman,
		IdentityZone: identityZoneStaff,
		Issuer:       c.Issuer,
		Subject:      sub,
		ClientID:     c.AuthorizedParty,
		// 认证强度：acr/amr 有 MFA 痕迹算 mfa，否则算 password。
		// 目前没有任何判定读它（Step-up 是 XM-0030），先把事实如实记下来；
		// 到 XM-0030 要拿它做闸时，这里应该换成一个显式枚举而不是自由字符串
		AuthenticationLevel: authenticationLevel(c),
		// 绝不从令牌读环境（规格 §20.5）
		Environment: r.cfg.Environment,
		Scopes:      tr.Scopes,
	}
	if err := p.Validate(); err != nil {
		return principal.Principal{}, authFailure("映射出的身份不合法", err)
	}
	return p, nil
}

// authenticationLevel 从 acr/amr 推断认证强度。
//
// 刻意做得简单：只回答「这次登录有没有第二因素的痕迹」。Keycloak 的 acr 默认是
// 数字 LoA（1 = 单因素，>=2 一般表示做过 Step-up），amr 则列出实际用过的方法。
// 任一有 MFA 痕迹就算 mfa —— 宁可把「其实是 mfa」的判成 password（保守），
// 也不要反过来。
func authenticationLevel(c claims) string {
	for _, m := range c.AMR {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "mfa", "otp", "totp", "hwk", "swk", "sc", "pop":
			return "mfa"
		}
	}
	acr := strings.ToLower(strings.TrimSpace(string(c.ACR)))
	if acr == "" {
		return "password"
	}
	if strings.Contains(acr, "mfa") {
		return "mfa"
	}
	// 数字 LoA：>=2 视为多因素。非数字的自定义 acr 不猜，按 password 记
	if n, err := parseUint(acr); err == nil && n >= 2 {
		return "mfa"
	}
	return "password"
}

// parseUint 只接受纯十进制正整数，避免把 "2fa" 这类串当成 2。
func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("空串")
	}
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("非十进制数字")
		}
		n = n*10 + uint64(r-'0')
		if n > 1<<32 {
			return 0, errors.New("过大")
		}
	}
	return n, nil
}

// driftInScopeClaim 扫描 OAuth 的 scope 声明。
//
// 平台**从不**采纳这里的值：OAuth scope 与平台权限是两套东西，让令牌里的
// `scope` 直接变成平台权限，就等于把授权决定权交回 Keycloak（ADR-016 反对）。
// 扫它只是为了发现有人往里塞平台权限。
func driftInScopeClaim(scope string) []string {
	var out []string
	for _, s := range strings.Fields(scope) {
		if looksLikePlatformScope(s) {
			out = append(out, "scope:"+s)
		}
	}
	return out
}

// driftInResourceAccess 扫描 Client 角色。平台只读 realm_access，这里同样只巡检。
func driftInResourceAccess(ra map[string]roleList) []string {
	var out []string
	for client, rl := range ra {
		for _, role := range rl.Roles {
			if looksLikePlatformScope(role) {
				out = append(out, "resource_access."+client+":"+role)
			}
		}
	}
	return out
}

// safeIdentifier 拒绝含控制字符或过长的用户名。
//
// 这个值会进 Principal.ID，进而进审计事件与日志。换行符能在文本日志里伪造出
// 一整行「另一个人的操作」——审计链的价值就是不能被写入方摆布。
func safeIdentifier(s string) bool {
	// 上限按字节算，256 足够容下非 ASCII 用户名（一个汉字 3 字节）。
	// principal_id 在库里是 text 没有长度限制，这里限长只为拦住
	// 「用一个超长用户名把日志撑爆」这类玩法
	if len(s) > 256 {
		return false
	}
	for _, r := range s {
		// U+FEFF（零宽不换行空格）单独列出来：它不是控制字符，但在日志与
		// 界面里完全不可见，足以造出两个「看起来一模一样」的用户名
		if unicode.IsControl(r) || r == '\ufeff' {
			return false
		}
	}
	return true
}

// bearerToken 从 Authorization 头取出令牌。
func bearerToken(req *http.Request) (string, error) {
	h := req.Header.Get("Authorization")
	if strings.TrimSpace(h) == "" {
		// 「没带令牌」是调用方自己知道的事实，直说不构成信息泄漏；
		// 前端据此跳登录，而不是显示一个「令牌无效」把人吓住
		return "", action.NewError(action.CodePermissionDenied, msgMissingToken, errors.New("缺少 Authorization 头"))
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", authFailure("Authorization 不是 Bearer 形态", nil)
	}
	token := strings.TrimSpace(rest)
	if token == "" {
		return "", action.NewError(action.CodePermissionDenied, msgMissingToken, errors.New("Bearer 令牌为空"))
	}
	if len(token) > maxTokenBytes {
		return "", authFailure("令牌超长", nil)
	}
	return token, nil
}

// splitAndCheckHeader 拆分 JWT 三段并校验头部，返回验签所需的输入。
func splitAndCheckHeader(raw string) (signingInput, sig, payload []byte, kid string, err error) {
	first := strings.IndexByte(raw, '.')
	last := strings.LastIndexByte(raw, '.')
	if first <= 0 || last <= first || last == len(raw)-1 || strings.Count(raw, ".") != 2 {
		return nil, nil, nil, "", authFailure("不是三段式 JWS", nil)
	}
	headerB64, payloadB64, sigB64 := raw[:first], raw[first+1:last], raw[last+1:]

	headerBytes, e := base64.RawURLEncoding.DecodeString(headerB64)
	if e != nil {
		return nil, nil, nil, "", authFailure("头部不是合法 base64url", nil)
	}
	var h jwtHeader
	if e := json.Unmarshal(headerBytes, &h); e != nil {
		return nil, nil, nil, "", authFailure("头部不是合法 JSON", nil)
	}
	// alg 白名单，不是黑名单：只认 RS256。"none" 与 HMAC 家族的算法混淆攻击
	// 全靠这一行挡住——用黑名单排除 "none" 的实现每隔几年就要再被绕过一次
	if !strings.EqualFold(h.Alg, algRS256) {
		return nil, nil, nil, "", authFailure("alg 不是 RS256", nil)
	}
	// crit 声明「这些扩展头必须被理解」，我们一个都不理解，只能拒
	if len(h.Crit) > 0 {
		return nil, nil, nil, "", authFailure("包含无法处理的 crit 头", nil)
	}
	if strings.TrimSpace(h.Kid) == "" {
		return nil, nil, nil, "", authFailure("缺少 kid", nil)
	}

	payload, e = base64.RawURLEncoding.DecodeString(payloadB64)
	if e != nil {
		return nil, nil, nil, "", authFailure("载荷不是合法 base64url", nil)
	}
	sig, e = base64.RawURLEncoding.DecodeString(sigB64)
	if e != nil {
		return nil, nil, nil, "", authFailure("签名不是合法 base64url", nil)
	}
	// 验签的输入是**原始的** header.payload 文本，不是重新编码的结果：
	// base64url 有多种等价写法，重新编码会让签名对不上
	return []byte(raw[:last]), sig, payload, h.Kid, nil
}

// authFailure 把内部原因收敛成对外统一的错误。
//
// reason 必须是**固定字符串**，绝不拼进令牌的任何片段——它会进服务端日志
// （httpapi.WriteError 记 err），而日志里不该出现令牌内容（宪法 7 条）。
// cause 可为 nil；传进来的 cause 同样不得携带令牌。
func authFailure(reason string, cause error) error {
	var wrapped error
	if cause != nil {
		wrapped = fmt.Errorf("%s: %w", reason, cause)
	} else {
		wrapped = errors.New(reason)
	}
	return action.NewError(action.CodePermissionDenied, msgInvalidToken, wrapped)
}
