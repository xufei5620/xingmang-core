package newapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实只读客户端（XM-0038）。
//
// 四道只读闸（ADR-018）在 HTTP 通道上的落点，与 sub2api 的真实客户端同构：
//
//	闸 1 拒绝可写连接配置  → connector.Config.Validate()，构造第一步就跑；
//	闸 2 强制只读          → connector.NewReadOnlyClient：只放行 GET/HEAD，
//	                        其余方法在 RoundTrip 里就被拒，请求根本不发出；
//	闸 3 复核服务端只读     → HTTP 通道上没有「服务端只读设置」可查（契约里的
//	                        闸 2/3 原文针对**数据库通道**）。HTTP 侧能做的是：
//	                        目标主机精确 allowlist + 拒绝重定向（一个 302 就能
//	                        把请求引到 allowlist 之外），以及要求上游发的是
//	                        只读账号——后者平台强制不了。**NewAPI 上游根本没有
//	                        只读角色**，采集凭据必然是管理员 token，见运行手册；
//	闸 4 包内无写路径      → ReadClient 接口只有读方法，本文件也只有 GET。
//
// **第五道闸是 NewAPI 特有的**：上游有一批**用 GET 方法但会写数据库**的端点
// （渠道测试、刷新余额、拉取模型、甚至会静默轮换调用者自己 token 的
// /api/user/token）。前四道闸看的是 HTTP 方法与目标主机，对它们**全部放行**。
// 所以 do() 在发请求前先过一遍 writeDisguisedAsGetRoutes 黑名单，命中即
// write_attempt——见 upstream.go 的 assertReadOnlyRoute。
//
// 上游的路由与响应形状集中在 upstream.go：真实凭据到位后如果发现形状有出入，
// 要改的是那一个文件，本文件的错误纪律、新鲜度与凭据处理都不受影响。

const (
	// defaultRequestTimeout 是单次请求的兜底超时。
	// 调用方漏填超时不该变成「没有超时」（规格 §18.1-4）。
	defaultRequestTimeout = 15 * time.Second

	// maxResponseBytes 限制单次响应体大小。
	//
	// NewAPI 的列表端点把 page_size 硬钳到 100，但 /api/data/ 是**不分页**的
	// 裸数组——一个模型多、时间窗口宽的实例能返回很大的 JSON。超限归
	// bad_response，而不是把 worker 的内存吃光。
	maxResponseBytes = 8 << 20

	// userAgent 让上游的访问日志能分辨这些读取来自哪个平台组件。
	// 出问题时对方能一眼看出是谁在读，不必反查 IP。
	userAgent = "xingmang-platform/connector-newapi-v" + ContractVersion

	// credentialPurpose 进凭据审计（规格 §4.5），不影响解析结果。
	credentialPurpose = "newapi readonly sync"
	// credentialCaller 是审计里的请求方身份。
	credentialCaller = "connector:" + ConnectorKey

	// unknownVersion 是上游没给版本时的占位值。
	//
	// 契约要求「即便不支持也要给出探测值与指纹」，所以这里不能留空；
	// 而 unknown 一定不在兼容矩阵内，于是自然 Supported=false——
	// 「读不到版本」和「版本不支持」在决策上本来就该同样保守。
	unknownVersion = "unknown"
)

// SupportedUpstreamVersions 是兼容矩阵（ADR-004：版本探测 + 版本指纹 + 兼容矩阵）。
//
// 只写到 major.minor：补丁版本升级不该让整条采集链路判为不支持，
// 而 minor 变更在这个上游意味着接口可能动过，值得人看一眼。
//
// 依据是 docs/inventory/managed-systems.yaml 里 newapi-prod 的
// detected_version `v1.0.0-rc.25`（2026-08-26 观测），normalizeVersion 会把它
// 收敛成 1.0.0 → 命中 "1.0"。
//
// ⚠️ **本包的字段形状核对的是源码而不是那个实例。** K:/newapi-src 的 VERSION
// 文件是空的，上游默认版本串是 "v0.0.0"，还能被 VERSION 环境变量、CI 的
// git describe、分支镜像的 "<prefix>-日期-sha" 任意覆盖——换句话说，
// 「源码这一版对应哪个版本号」在上游自己那里就没有定论。
// 凭据到位后第一件事是跑一次 Version()，把探测值补进 managed-systems.yaml；
// 若探测值不在矩阵内，用 WithSupportedVersions 显式声明（不改代码的应急路径），
// 而不是把矩阵放宽成「什么都认」。
var SupportedUpstreamVersions = []string{"1.0"}

// Option 调整客户端的构造。
type Option func(*clientOptions)

type clientOptions struct {
	base              http.RoundTripper
	now               func() time.Time
	supportedVersions []string
	currency          string
	businessDay       *time.Location
	userID            string
	activeWindow      time.Duration
}

// WithBaseTransport 指定底层 RoundTripper（默认 http.DefaultTransport）。
//
// 存在的唯一理由是契约测试要连 httptest.NewTLSServer 的自签证书。
// **护栏一个都不能少**：base 依旧被 connector.ReadOnlyTransport 包在里面，
// 只读方法限制、主机 allowlist、拒绝重定向在测试路径上与生产路径上是同一份
// 代码；写端点黑名单更是在这一层之上，与 base 是谁完全无关。
func WithBaseTransport(rt http.RoundTripper) Option {
	return func(o *clientOptions) { o.base = rt }
}

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(o *clientOptions) { o.now = now }
}

// WithSupportedVersions 覆盖兼容矩阵。
//
// 给运维一条不改代码就能应急的路：上游临时升到一个还没登记的版本时，
// 与其让整条采集链路判为不支持而降级，不如由人显式声明「这个版本我看过了」。
func WithSupportedVersions(versions ...string) Option {
	return func(o *clientOptions) { o.supportedVersions = versions }
}

// WithCurrency 声明本契约的记账币种（默认 defaultCurrency = USD）。
//
// ⚠️ 它只改**标注**，不做汇率换算：NewAPI 内部的钱只有 quota 一种单位，
// 换算成钱的唯一口径是 `美元 = quota / quota_per_unit`。把币种改成 CNY
// 不会让数字变成人民币，只会让一批美元数字**被标成**人民币——那是
// 「看起来完全正常」的错数字。只有在上游确实按别的币种定价、且运维清楚
// 自己在做什么时才该用它。
func WithCurrency(code string) Option {
	return func(o *clientOptions) { o.currency = code }
}

// WithBusinessDayTimezone 声明业务日结时区（默认 UTC，与采集任务算业务日
// 的时区一致）。
//
// 规格 §5.9 要求业务日结时区**显式声明**。它在这里有实打实的后果：上游的
// 充值与用量查询都按 Unix 时间戳过滤，而「2026-08-27 是哪 86400 秒」完全由
// 这个时区决定。配错时区不会报错，只会让每天的第一笔与最后一笔算到隔壁那天
// ——这是凭据到位后必须核对的第一梯队事项（见运行手册验证清单）。
func WithBusinessDayTimezone(loc *time.Location) Option {
	return func(o *clientOptions) { o.businessDay = loc }
}

// WithUserID 配置旧版本 NewAPI 需要的 New-Api-User 头（管理员的用户 id）。
//
// 普查依据的源码里这个头**已经不参与鉴权**，但真实实例跑的补丁版未知，
// 而旧版本上没有它就是 401。配了就发，没配就不发——多发一个会被新版本忽略的
// 头代价是零，少发它则会让一个还没升级的实例表现为「凭据无效」。
//
// 它**不是**凭据（用户 id 不是秘密），所以走普通配置项而不是 CredentialRef。
func WithUserID(id string) Option {
	return func(o *clientOptions) { o.userID = strings.TrimSpace(id) }
}

// WithActiveUserWindow 覆盖「活跃用户」的判据窗口（默认 30 天）。
//
// NewAPI 上游**没有活跃用户这个概念**，这个口径是平台自定的。判据会跟着
// 数字一起写进 UserStats 的 Watermark，改了窗口，看板上的判据说明也跟着变。
func WithActiveUserWindow(d time.Duration) Option {
	return func(o *clientOptions) { o.activeWindow = d }
}

type client struct {
	http         *http.Client
	endpoint     *url.URL
	provider     secrets.SecretProvider
	ref          secrets.CredentialRef
	now          func() time.Time
	supported    []string
	currency     string
	scale        int
	businessDay  *time.Location
	userID       string
	activeWindow time.Duration

	// mu 保护 token 与 quota_per_unit 两个缓存。
	//
	// 客户端的生命周期是「一轮同步」，缓存的意义是一轮里的几次读取不必各打
	// 一次 Provider（每次都会留一条审计记录）、不必各拉一次 /api/status。
	// 下一轮会重新构造客户端，于是 root 改了 quota_per_unit 最多一个周期就跟上。
	mu          sync.Mutex
	token       secrets.SecretValue
	cached      bool
	quotaUnit   int64
	quotaUnitOK bool
}

// NewClient 构造 NewAPI 的真实只读客户端。
//
// 构造期**不做任何 I/O**：凭据在首次读取时才解析，用的是那次请求的 ctx，
// 取消与超时才管得住它；quota 换算基数同理。因此本函数返回的错误只可能是
// 配置错误。
func NewClient(cfg connector.Config, sp secrets.SecretProvider, opts ...Option) (ReadClientV2, error) {
	options := clientOptions{
		now:               time.Now,
		supportedVersions: SupportedUpstreamVersions,
		currency:          defaultCurrency,
		businessDay:       time.UTC,
		activeWindow:      defaultActiveUserWindow,
	}
	for _, o := range opts {
		if o != nil {
			o(&options)
		}
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.businessDay == nil {
		options.businessDay = time.UTC
	}
	if options.activeWindow <= 0 {
		options.activeWindow = defaultActiveUserWindow
	}

	if cfg.Timeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）。
		// 放在 Validate 之前：这是补默认值，不是放宽校验。
		cfg.Timeout = defaultRequestTimeout
	}
	// 闸 1：拒绝可写连接配置（ADR-018）。https、CredentialRef 形态、
	// 非空 allowlist、endpoint 主机自己也在 allowlist 内——全在这里。
	if err := cfg.Validate(); err != nil {
		return nil, connector.NewError(connector.KindInternal, "newapi.client.config", err)
	}
	ref, err := secrets.ParseCredentialRef(cfg.CredentialRef)
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "newapi.client.config", err)
	}
	if sp == nil {
		return nil, connector.NewError(connector.KindInternal, "newapi.client.config",
			errors.New("secret provider 为空：没有 Provider 就解析不出凭据"))
	}
	endpoint, err := url.Parse(strings.TrimSuffix(cfg.Endpoint, "/"))
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "newapi.client.config", err)
	}
	scale, err := currencyScale(options.currency)
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "newapi.client.config", err)
	}

	return &client{
		// 闸 2/4：只读、限目标、拒重定向、带超时的 HTTP 客户端。
		// 连接配置本身不留在客户端里：它已经被 Validate 消化成这几个字段，
		// 留一份完整副本只会多一个可能被日志打印出来的地方。
		http:         connector.NewReadOnlyClientWithBase(options.base, cfg.TargetAllowlist, cfg.Timeout),
		endpoint:     endpoint,
		provider:     sp,
		ref:          ref,
		now:          options.now,
		supported:    append([]string(nil), options.supportedVersions...),
		currency:     strings.ToUpper(strings.TrimSpace(options.currency)),
		scale:        scale,
		businessDay:  options.businessDay,
		userID:       options.userID,
		activeWindow: options.activeWindow,
	}, nil
}

func (c *client) clock() time.Time { return c.now().UTC() }

// credential 解析并缓存凭据。
//
// 明文从这里出来，只走到 authorize() 那一行拼头，再没有第三个去处：
// 不进日志、不进错误、不进任何返回值（宪法 7 条）。
func (c *client) credential(ctx context.Context) (secrets.SecretValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached {
		return c.token, nil
	}
	// 只在成功时缓存：瞬时失败（Provider 抖动、变量还没注入）下一次读取会重试，
	// 而不是把失败也记住，让整轮同步陪着一起死。
	v, err := c.provider.Resolve(secrets.WithCaller(ctx, credentialCaller), c.ref, credentialPurpose)
	if err != nil {
		return secrets.SecretValue{}, err
	}
	if v.IsZero() {
		return secrets.SecretValue{}, secrets.ErrEmptySecret
	}
	c.token, c.cached = v, true
	return v, nil
}

// authorize 是明文唯一出现的地方——**一瞬**：Reveal 的结果直接进请求头，
// 不落任何变量之外的地方，也不参与任何错误信息的拼接（宪法 7 条）。
//
// 解析失败归 auth：拿不到凭据与凭据无效，对调用方是同一件事（读不了），
// 而且都得靠人去看凭据配置，不是靠重试能好的。
func (c *client) authorize(ctx context.Context, op string, req *http.Request) error {
	v, err := c.credential(ctx)
	if err != nil {
		return connector.NewError(connector.KindAuth, op, err)
	}
	applyAuth(req, v, c.userID)
	return nil
}

// respMeta 是一次响应的元信息，用来推导新鲜度。
type respMeta struct {
	// receivedAt 是本地收到响应的时刻（UTC）。
	receivedAt time.Time
	// serverDate 是上游 Date 头解析出的时刻；解析不出则为零值。
	//
	// 它不是「数据的观测时刻」，只是「上游生成这个响应的时刻」——
	// 当上游没在响应体里给数据时间戳时，它是次优的近似值，
	// 至少比用本地时钟好：本地时钟与上游时钟可能差着几分钟。
	serverDate time.Time
	status     int
	latency    time.Duration
}

// observedAt 给出这次响应可用的最佳观测时刻。
//
// 优先级：上游数据时间戳（由各 fetch 函数传入）> 上游 Date 头 > 本地接收时刻。
// 退到本地接收时刻时，语义已经从「数据什么时候被观测」滑向「我们什么时候
// 问的」——NewAPI 的列表端点**一个都不给数据时间戳**，所以这条链路上几乎
// 总是走到 Date 头这一档。运行手册的验证清单里有一条专门核对它。
func (m respMeta) observedAt(upstream time.Time) time.Time {
	if !upstream.IsZero() {
		return upstream.UTC()
	}
	if !m.serverDate.IsZero() {
		return m.serverDate.UTC()
	}
	return m.receivedAt.UTC()
}

// get 发一次只读请求并把 JSON 解进 out。
//
// out 为 nil 时只做状态码检查（能力探测用）。
func (c *client) get(ctx context.Context, op, routePath string, query url.Values, out any) (respMeta, error) {
	return c.do(ctx, http.MethodGet, op, routePath, query, out)
}

func (c *client) do(ctx context.Context, method, op, routePath string, query url.Values, out any) (respMeta, error) {
	// 上下文已经取消就别再发请求了：契约要求取消后立即返回。
	if err := ctx.Err(); err != nil {
		return respMeta{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	// **第五道闸**：伪装成 GET 的写端点在这里被挡下，请求不会发出。
	// 放在最前面而不是最后：一次都不该发出去的请求，连凭据都不该为它解析。
	if err := assertReadOnlyRoute(routePath); err != nil {
		return respMeta{}, connector.NewError(connector.KindWriteAttempt, op, err)
	}

	target := *c.endpoint
	target.Path = strings.TrimSuffix(c.endpoint.Path, "/") + routePath
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return respMeta{}, connector.NewError(connector.KindInternal, op, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if err := c.authorize(ctx, op, req); err != nil {
		return respMeta{}, err
	}

	start := c.clock()
	resp, err := c.http.Do(req)
	meta := respMeta{receivedAt: c.clock()}
	meta.latency = meta.receivedAt.Sub(start)
	if err != nil {
		return meta, classifyTransportError(err, op)
	}
	defer func() {
		// 把剩余响应体读完再关，连接才能复用；读多少有上限，
		// 免得「关个连接」变成「把一个畸形的巨大响应读完」。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	meta.status = resp.StatusCode
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		meta.serverDate = t.UTC()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// 上游的响应体**一个字都不进错误链**：它可能带用户数据，
		// 极端情况下还可能把请求头原样回显出来（宪法 7 条）。
		// 排查需要的是「哪个操作、什么状态码」，那两样都在。
		return meta, connector.NewError(classifyStatus(resp.StatusCode), op,
			fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	if out == nil {
		return meta, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return meta, classifyTransportError(err, op)
	}
	if len(body) > maxResponseBytes {
		return meta, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应体超过 %d 字节上限", maxResponseBytes))
	}
	if err := json.Unmarshal(body, out); err != nil {
		// json 的错误只含偏移量与类型名，不含响应内容，可以安全进错误链。
		return meta, connector.NewError(connector.KindBadResponse, op, err)
	}
	return meta, nil
}

// classifyStatus 把 HTTP 状态码映射成稳定分类（ADR-004）。
//
// ⚠️ NewAPI 的**业务**失败不走状态码：common.ApiError 返回的是 200 +
// `{"success":false}`，只有鉴权失败才是真 401/403。所以本函数只是第一道筛，
// 第二道在 upstreamEnvelope.decode 里——少了那一道，一次「查询失败」会被
// 当成一次成功读取。
func classifyStatus(code int) connector.ErrorKind {
	switch {
	case code == http.StatusUnauthorized:
		// 上游的 authHelper：无凭据、token 过期、会话被撤销、用户被禁用
		// 都是 401。全都要人去看凭据配置，重试不会自己变好。
		return connector.KindAuth
	case code == http.StatusForbidden:
		// 403 在这个上游有两个来源：角色不够（role < 10），或者渠道路由上的
		// casbin 权限缺 ChannelRead。两者都是「这个账号没被授权用这项能力」，
		// 与 401 同一种处置。
		return connector.KindAuth
	case code == http.StatusTooManyRequests:
		return connector.KindRateLimited
	case code == http.StatusNotFound, code == http.StatusMethodNotAllowed, code == http.StatusNotImplemented:
		// 路由不存在 = 这个上游版本没有这项能力，不是「上游挂了」。
		// 分开归类才让调用方做得出正确决策：not_supported 重试没用，
		// unavailable 重试可能有用。
		return connector.KindNotSupported
	case code >= 500:
		return connector.KindUnavailable
	default:
		// 4xx 里剩下的（400/409/422…）都是「我们发的请求上游看不懂」，
		// 归 bad_response：重试同样的请求不会有不同结果。
		//
		// 3xx 也落在这里，但实际上到不了：重定向在传输层就被拒了
		// （CheckRedirect），归 forbidden_target。
		return connector.KindBadResponse
	}
}

// classifyTransportError 把传输层错误映射成稳定分类。
//
// 关键在于**不覆盖已有分类**：allowlist 拒绝（forbidden_target）与重定向
// 拒绝是护栏自己给出的判断，被 http.Client 包进 *url.Error 之后如果在这里
// 一律改判成 unavailable，「配置把目标写错了」就会伪装成「上游挂了」，
// 运维会去查上游而不是去查配置。
//
// 对 NewAPI 这一条尤其要紧：少写一个尾斜杠 → 上游 301 → 护栏拒绝重定向。
// 若被改判成 unavailable，运维会以为上游宕了，而真正的原因是路径少一个字符。
func classifyTransportError(err error, op string) error {
	var ce *connector.Error
	if errors.As(err, &ce) {
		return connector.NewError(ce.Kind, op, err)
	}
	// 超时、连接被拒、DNS 失败、上下文取消都归 unavailable：
	// 对调用方是同一件事——这次读不到，过一会儿可能就好了。
	return connector.NewError(connector.KindUnavailable, op, err)
}

// versionSupported 判断探测到的版本是否在兼容矩阵内。
//
// 矩阵项写 "1.0" 表示整条 minor 线都认，写 "1.0.25" 表示只认那个补丁版本。
// 前者是常态：补丁升级不该让采集链路判为不支持。
func versionSupported(detected string, matrix []string) bool {
	d := normalizeVersion(detected)
	if d == "" || d == unknownVersion {
		return false
	}
	dParts := strings.Split(d, ".")
	for _, entry := range matrix {
		e := normalizeVersion(entry)
		if e == "" {
			continue
		}
		eParts := strings.Split(e, ".")
		if len(eParts) > len(dParts) {
			continue
		}
		match := true
		for i := range eParts {
			if eParts[i] != dParts[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// normalizeVersion 去掉前缀 v 与预发布/构建后缀，只留 x.y.z。
//
// 上游的版本串默认带 v 前缀（common.Version = "v0.0.0"），而 CI 注入的形态
// 可能不带，甚至可能是 "alpha-20260828-abc1234" 这种压根不是语义化版本的串。
// 后者归一化之后不含点号、也不会命中任何矩阵项 → Supported=false，
// 这正是想要的保守结果。
func normalizeVersion(s string) string {
	v := strings.TrimSpace(strings.ToLower(s))
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	return v
}

// fingerprint 是「这个上游是什么」的稳定摘要（ADR-004 要求版本指纹）。
//
// 用摘要而不是把原始串拼起来：指纹会进日志与看板，而上游的版本串可以被
// VERSION 环境变量设成任意值（包括主机名、内部路径这类东西）。
// 摘要一样能比出「变了没有」，但不会顺手把那些内容搬到我们的日志里。
func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// Version 探测上游版本。
//
// 不支持的版本返回 Supported=false 而**不报错**：是否 Fail Closed 由调用方
// 按场景决定（读取可降级，写入必须停）。
func (c *client) Version(ctx context.Context) (connector.VersionInfo, error) {
	version, meta, err := c.fetchVersion(ctx, opVersion)
	if err != nil {
		return connector.VersionInfo{}, err
	}
	detected := strings.TrimSpace(version)
	if detected == "" {
		detected = unknownVersion
	}
	return connector.VersionInfo{
		Detected: detected,
		// 指纹只由端点主机 + 版本串构成：/api/status 那个 data 里再没有别的
		// **稳定**标识可掺——start_time 每次重启都变，主题/开关类字段人人可改，
		// 掺进去只会让指纹在无关变更上抖动，然后所有人学会无视它。
		Fingerprint: fingerprint(c.endpoint.Host, detected),
		Supported:   versionSupported(detected, c.supported),
		DetectedAt:  meta.receivedAt,
	}, nil
}

// Health 返回运营健康状态。
//
// 「不健康」不是「调用失败」：读不通上游是一个**结果**，要能落进看板，
// 所以除上下文取消外一律返回 nil error + 结构化结果。
func (c *client) Health(ctx context.Context) (connector.HealthResult, error) {
	start := c.clock()
	healthy, meta, err := c.fetchHealth(ctx, opHealth)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// 本进程在关机或调用方超时，不是上游的健康状况——
			// 把它记成「上游不健康」是假信号，比没有信号更糟。
			return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, opHealth, ctxErr)
		}
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: c.clock(),
			LatencyMS: c.clock().Sub(start).Milliseconds(),
			ErrorKind: connector.KindOf(err),
			// 安全的简短说明，不含上游原始错误文本（ADR-004）
			Detail: healthDetail(connector.KindOf(err)),
		}, nil
	}
	if !healthy {
		// /api/status 回了 200 但 success=false：服务在，但它自己说状态不对。
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: meta.receivedAt,
			LatencyMS: meta.latency.Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			Detail:    "upstream reported a non-serving state",
		}, nil
	}
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: meta.receivedAt,
		LatencyMS: meta.latency.Milliseconds(),
	}, nil
}

// healthDetail 给出与分类一一对应的安全短语。
//
// 固定短语而不是拼上游文本：Detail 会进日志与看板，
// 上游的错误正文里出现过用户名与订单号（ADR-004 的由来）。
func healthDetail(kind connector.ErrorKind) string {
	switch kind {
	case connector.KindAuth:
		return "upstream rejected the read-only credential"
	case connector.KindRateLimited:
		return "upstream rate limited the status probe"
	case connector.KindNotSupported:
		return "upstream has no status endpoint at the configured path"
	case connector.KindForbiddenTarget:
		return "target host is not in the allowlist, or the upstream redirected"
	case connector.KindBadResponse:
		return "upstream status response was not understood"
	default:
		return "upstream status endpoint unreachable or returned non-2xx"
	}
}

// Capabilities 返回本连接**当前实际可用**的能力。
//
// 做法是对每条支撑路由发一次最小代价的 GET 探测，而不是把 ReadCapabilities
// 原样抄回去：抄回去的清单在上游改版之后依然「全绿」，那种清单没有任何信息量。
// 这个方法不在采集热路径上（同步任务不调它），几次探测换一份说真话的
// 能力清单，划算。
func (c *client) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	items := implementedCapabilities(c.clock())
	available := make([]registry.Capability, 0, len(items))
	probed := make(map[string]bool, len(items))

	for _, item := range items {
		usable := true
		for _, probe := range item.probes {
			ok, seen := probed[probe.key()]
			if !seen {
				_, err := c.get(ctx, opCapabilities, probe.route, probe.query, nil)
				switch {
				case err == nil:
					ok = true
				case connector.KindOf(err) == connector.KindNotSupported:
					// 404/405：这条路由在这个上游版本上不存在 → 该能力不可用。
					ok = false
				default:
					// 认证失败、限流、网络不通：这不是「能力不存在」，
					// 是「现在问不出来」。返回一份猜出来的清单会让调用方
					// 以为上游少了能力，从而做出错误的降级决策——报错更诚实。
					return nil, err
				}
				probed[probe.key()] = ok
			}
			if !ok {
				usable = false
			}
		}
		if usable {
			available = append(available, item.capability)
		}
	}
	return available, nil
}

// UserStats 读取用户数量与余额概览。
func (c *client) UserStats(ctx context.Context) (UserStats, error) {
	return c.fetchUserStats(ctx)
}

// DailyOrders 读取某业务日的充值与订阅摘要。
func (c *client) DailyOrders(ctx context.Context, day string) (OrderSummary, error) {
	parsed, err := c.parseBusinessDay(opOrders, day)
	if err != nil {
		return OrderSummary{}, err
	}
	return c.fetchDailyOrders(ctx, parsed)
}

// Channels 读取全部渠道的状态、错误率与延迟。
func (c *client) Channels(ctx context.Context) ([]ChannelStatus, error) {
	return c.fetchChannels(ctx)
}

func (c *client) ChannelDirectory(ctx context.Context) (ChannelDirectorySnapshot, error) {
	return c.fetchChannelDirectory(ctx)
}

// ModelUsages 读取某业务日的逐模型使用量。
func (c *client) ModelUsages(ctx context.Context, day string) ([]ModelUsage, error) {
	parsed, err := c.parseBusinessDay(opModels, day)
	if err != nil {
		return nil, err
	}
	return c.fetchModelUsages(ctx, parsed)
}

// parseBusinessDay 严格校验业务日并按业务日结时区解析。
//
// 先校验再发请求：非法业务日是**调用方**的错，不该变成一次上游读取。
// 归类为 bad_response 与 Fake 保持一致——两个实现对同一个非法输入必须给出
// 同一个分类，否则「换实现」就会换错误处理。
//
// 两个入口（DailyOrders / ModelUsages）都走这里：只在一个入口上校验，
// 另一个入口就成了绕过它的后门（契约套件对两者都断言）。
func (c *client) parseBusinessDay(op, day string) (time.Time, error) {
	trimmed := strings.TrimSpace(day)
	parsed, err := time.ParseInLocation(newapiBusinessDayLayout, trimmed, c.businessDay)
	if err != nil {
		return time.Time{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("业务日 %q 非法: %w", day, ErrInvalidDay))
	}
	// time.Parse 对 "2026-8-1" 这种少位数的写法是宽容的，但业务日必须是
	// 严格的 YYYY-MM-DD——否则同一天会有两种写法，落库后成为两条记录。
	if parsed.Format(newapiBusinessDayLayout) != trimmed {
		return time.Time{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("业务日必须严格为 %s: %w", newapiBusinessDayLayout, ErrInvalidDay))
	}
	return parsed, nil
}
