package platformusers

import (
	"context"
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

// Mode 选择 fake / real 客户端。
type Mode string

const (
	// ModeFake 使用样本数据。
	ModeFake Mode = "fake"
	// ModeReal 连真实上游(XM-USERS-REAL)。
	ModeReal Mode = "real"
)

// ParseMode 解析模式;空值按 fake。
func ParseMode(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ModeFake:
		return ModeFake, nil
	case ModeReal:
		return ModeReal, nil
	default:
		return "", fmt.Errorf("platformusers: 未知模式 %q(只支持 fake / real)", raw)
	}
}

// RealConfig 是 real 模式的配置。
//
// 端点与凭据引用都在这里,但**明文凭据不在**:CredentialRef 由
// secrets.SecretProvider 在构造请求头的那一瞬才解析(ADR-014、宪法 7 条)。
type RealConfig struct {
	// Source 是平台标识(sub2api / newapi)。
	Source string
	// Endpoint 是上游只读端点,必须 https(ADR-018 闸 1)。
	Endpoint string
	// CredentialRef 形如 secret://<scope>/<name>。
	CredentialRef string
	// TargetAllowlist 是允许连接的主机精确清单(ADR-004)。
	// **留空不是「放行一切」而是「一个请求都发不出去」**——护栏 fail closed。
	TargetAllowlist []string
	// Secrets 解析 CredentialRef。
	Secrets secrets.SecretProvider
}

// Option 调整 RealClient 的构造。目前只用于测试:契约测试要把请求指向
// httptest 的自签证书服务器、要固定时钟。生产装配不需要传任何 Option。
type Option func(*realClientOptions)

type realClientOptions struct {
	base http.RoundTripper
	now  func() time.Time
}

// WithBaseTransport 指定底层 RoundTripper(默认 http.DefaultTransport)。
//
// 存在的唯一理由是契约测试要连 httptest.NewTLSServer 的自签证书。
// **护栏一个都不能少**:base 依旧被 connector.ReadOnlyTransport 包在里面,
// 只读方法限制、主机 allowlist、拒绝重定向在测试路径上与生产路径上是同一份
// 代码。
func WithBaseTransport(rt http.RoundTripper) Option {
	return func(o *realClientOptions) { o.base = rt }
}

// WithClock 注入时钟(测试用;默认 time.Now)。
func WithClock(now func() time.Time) Option {
	return func(o *realClientOptions) { o.now = now }
}

// RealClient 是真实上游的只读客户端(XM-USERS-REAL)。
//
// 四道只读闸(ADR-018)在 HTTP 通道上的落点,与 connectors/sub2api、
// connectors/newapi 的真实客户端同构:
//
//	闸 1 拒绝可写连接配置  → NewRealClient 的构造期校验(https、CredentialRef
//	                        形态、非空 allowlist、endpoint 主机自己也在
//	                        allowlist 内);
//	闸 2 强制只读          → connector.NewReadOnlyClient:只放行 GET/HEAD,
//	                        其余方法在 RoundTrip 里就被拒,请求根本不发出;
//	闸 3 复核服务端只读     → HTTP 通道上没有「服务端只读设置」可查(契约里的
//	                        闸 2/3 原文针对**数据库通道**)。HTTP 侧能做的是:
//	                        目标主机精确 allowlist + 拒绝重定向,以及要求
//	                        上游发的是只读账号——后者平台强制不了,必须在
//	                        发号那一步保证(交接文档里写明);
//	闸 4 包内无写路径      → ReadClient 接口只有 ListUsers 一个方法,本文件
//	                        与 upstream.go 都只发 GET。
//
// 上游的路由与响应形状集中在 upstream.go:真实凭据到位后如果发现形状有
// 出入,要改的是那一个文件,本文件的错误纪律、新鲜度与凭据处理都不受影响。
//
// **构造期不做任何 I/O**:凭据在首次 ListUsers 时才解析,用的是那次请求的
// ctx,取消与超时才管得住它。因此 NewRealClient 返回的错误只可能是配置错误
// ——这也是本类型不缓存跨请求状态(除凭据本身)的原因:cmd/platform-api 按
// 「配置要每次请求时解析」的纪律,每次 HTTP 请求都会重新构造一个
// RealClient(见 cmd/platform-api/platformusers.go),构造成本必须够低。
type RealClient struct {
	cfg      RealConfig
	http     *http.Client
	endpoint *url.URL
	ref      secrets.CredentialRef
	now      func() time.Time

	// mu 保护凭据缓存。缓存的意义是一次 ListUsers 内部的多次分页请求
	// (Sub2API 最多 20 次、NewAPI 最多 51 次)不必各打一次 Provider——
	// 每次解析都留一条审计记录,20 条与 1 条对同一次读取的意义没有差别,
	// 只是噪声。
	mu     sync.Mutex
	token  secrets.SecretValue
	cached bool
}

const (
	// defaultRequestTimeout 是单次请求的兜底超时(规格 §18.1-4)。
	defaultRequestTimeout = 15 * time.Second
	// maxResponseBytes 限制单次响应体大小,超限归 bad_response 而不是把
	// 调用方进程的内存吃光。
	maxResponseBytes = 8 << 20
	// userAgent 让上游的访问日志能分辨这些读取来自哪个平台组件。
	userAgent = "xingmang-platform/connector-platformusers-v" + ContractVersion
	// credentialPurpose 进凭据审计(规格 §4.5),不影响解析结果。
	credentialPurpose = "platformusers readonly read"
	// credentialCaller 是审计里的请求方身份。
	credentialCaller = "connector:" + ConnectorKey
)

// NewRealClient 构造真实客户端,并在构造期就把护栏校验做掉。
//
// 校验放在构造期而不是首次调用:一个配置错误的客户端应该让进程在启动时就
// 说清楚,而不是等到有人打开用户页签才报错——那时候已经有人在等着看数据了。
func NewRealClient(cfg RealConfig, opts ...Option) (*RealClient, error) {
	source, err := ParseSource(cfg.Source)
	if err != nil {
		return nil, err
	}
	cfg.Source = source

	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.Endpoint)), "https://") {
		return nil, fmt.Errorf("platformusers: endpoint 必须是 https(ADR-018 闸 1),得到 %q", cfg.Endpoint)
	}
	if len(cfg.TargetAllowlist) == 0 {
		// 留空按「一个都不许连」处理,而不是「随便连」
		return nil, fmt.Errorf("platformusers: 目标主机白名单为空——护栏 fail closed(ADR-004)")
	}
	if strings.TrimSpace(cfg.CredentialRef) == "" {
		return nil, fmt.Errorf("platformusers: 缺少 CredentialRef(凭据只经引用,宪法 7 条)")
	}
	ref, err := secrets.ParseCredentialRef(cfg.CredentialRef)
	if err != nil {
		return nil, fmt.Errorf("platformusers: CredentialRef 形状不对,应为 secret://<scope>/<name>: %w", err)
	}
	if cfg.Secrets == nil {
		return nil, fmt.Errorf("platformusers: 缺少 SecretProvider")
	}

	endpoint, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/"))
	if err != nil {
		return nil, fmt.Errorf("platformusers: endpoint 无法解析: %w", err)
	}

	options := realClientOptions{now: time.Now}
	for _, o := range opts {
		if o != nil {
			o(&options)
		}
	}
	if options.now == nil {
		options.now = time.Now
	}

	return &RealClient{
		cfg: cfg,
		// 闸 2/4:只读、限目标、拒重定向、带超时的 HTTP 客户端。
		http:     connector.NewReadOnlyClientWithBase(options.base, cfg.TargetAllowlist, defaultRequestTimeout),
		endpoint: endpoint,
		ref:      ref,
		now:      options.now,
	}, nil
}

// ListUsers 按 c.cfg.Source 翻全量、在本机过滤/排序/分页(见 listing.go 与
// upstream.go 顶部注释:上游的 search/keyword 都会匹配邮箱,不能转发)。
func (c *RealClient) ListUsers(ctx context.Context, filter ListFilter) (UserPage, error) {
	now := c.clock()
	f, err := filter.Normalize(now, nil)
	if err != nil {
		// 区间参数不合法归 bad_response:这是调用方给错了参数
		return UserPage{}, connectorBadPeriod(err)
	}
	if _, err := ParseSource(f.Source); err != nil && f.Source != "" {
		return UserPage{}, connectorBadSource(f.Source)
	}

	var (
		all          []User
		totalBalance Amount
		activeToday  CountValue
		snapshot     Snapshot
		fetchErr     error
	)
	switch c.cfg.Source {
	case SourceSub2API:
		all, totalBalance, activeToday, snapshot, fetchErr = c.fetchSub2APIUsers(ctx, now)
	case SourceNewAPI:
		all, totalBalance, activeToday, snapshot, fetchErr = c.fetchNewAPIUsers(ctx, now)
	default:
		// 不可达:cfg.Source 已在 NewRealClient 里经 ParseSource 校验过。
		return UserPage{}, connectorBadSource(c.cfg.Source)
	}
	if fetchErr != nil {
		return UserPage{}, fetchErr
	}

	matched := filterUsers(all, f)
	sortUsers(matched, f.Sort)
	pageUsers, next, err := paginateUsers(matched, f.Cursor, f.Limit)
	if err != nil {
		return UserPage{}, err
	}
	totals := periodTotals(matched)

	// TotalCount 声称的是「符合条件的总数」(contract.go 的字段注释)。
	// 一旦这次读取因为翻页预算耗尽而没能翻完全量(Watermark 里带
	// ";truncated"),len(matched) 就只是一个下界,不是真的总数——报成
	// Known 等于把一个不完整的数字冒充成完整的(宪法 12 条)。
	totalCount := KnownCount(int64(len(matched)))
	if strings.Contains(snapshot.Watermark, ";truncated") {
		totalCount = UnknownCount()
	}

	return UserPage{
		Users:        pageUsers,
		TotalCount:   totalCount,
		TotalBalance: totalBalance,
		ActiveToday:  activeToday,
		PeriodTotals: totals,
		Period:       f.Period,
		NextCursor:   next,
		Snapshot:     snapshot,
	}, nil
}

// GetUser 是 UserDetailReader 的 real 实现(XM-USERS-V2-REAL,Task 4/5,
// SUB2_REAL_APPROVAL / NEWAPI_REAL_APPROVAL)。
//
// 两个上游都没有原生的"按 ID 精确查询"端点——两份批准的证据都只覆盖各自的
// 用户清单端点,所以真正的分页扫描逻辑在 sub2api_v2.go / newapi_v2.go 里
// (getSub2APIUser / getNewAPIUser),本方法只做来源校验、区间归一化与
// 分发,与 ListUsers 的结构对称。
func (c *RealClient) GetUser(ctx context.Context, query GetUserQuery) (UserDetail, error) {
	if err := ctx.Err(); err != nil {
		return UserDetail{}, err
	}
	if err := query.Ref.Validate(); err != nil || query.Ref.Platform != c.cfg.Source {
		return UserDetail{}, connectorBadSource(query.Ref.Platform)
	}
	now := c.clock()
	period, err := (Period{Day: query.Day, Granularity: query.Granularity}).Normalize(now, nil)
	if err != nil {
		return UserDetail{}, connectorBadPeriod(err)
	}

	var detail UserDetail
	switch c.cfg.Source {
	case SourceSub2API:
		detail, err = c.getSub2APIUser(ctx, query.Ref)
	case SourceNewAPI:
		detail, err = c.getNewAPIUser(ctx, query.Ref)
	default:
		// 不可达:cfg.Source 已在 NewRealClient 里经 ParseSource 校验过。
		return UserDetail{}, connectorBadSource(c.cfg.Source)
	}
	if err != nil {
		return UserDetail{}, err
	}
	detail.Period = period
	detail.Capabilities = c.V2Capabilities()
	return detail, nil
}

// V2Capabilities 声明本 real 客户端已实现且经共享 contracttest 验证过的 v2
// 能力。**只有 detail_read 一项**:DailyUsage / Key metadata 的 real reader
// 需要各自独立的 DAILY_USAGE_APPROVAL / KEY_SCOPE_APPROVAL 真实数据面审批
// (设计文档 §0),SUB2_REAL_APPROVAL / NEWAPI_REAL_APPROVAL 明确不覆盖它们
// ——声明了却没实现 DailyUsageReader / KeyMetadataReader,contracttest 的
// assertV2Capabilities 会在断言阶段就拒绝这种"先宣称、后实现"的做法。
//
// 两个来源共用同一个方法而不按 c.cfg.Source 分支:real 端目前只对两个来源
// 都实现了这一项能力,没有理由让它们的声明不同(与 Fake 端 Sub2API 独占
// daily/key 两项的情况不同,那是因为 Fake 两项都实现了,real 都没实现)。
func (c *RealClient) V2Capabilities() []registry.Capability {
	return []registry.Capability{CapabilityUserDetailRead}
}

func (c *RealClient) clock() time.Time { return c.now().UTC() }

// credential 解析并缓存凭据。
//
// 明文从这里出来,只走到 authorize() 那一行拼头,再没有第三个去处:
// 不进日志、不进错误、不进任何返回值(宪法 7 条)。
func (c *RealClient) credential(ctx context.Context) (secrets.SecretValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached {
		return c.token, nil
	}
	v, err := c.cfg.Secrets.Resolve(secrets.WithCaller(ctx, credentialCaller), c.ref, credentialPurpose)
	if err != nil {
		return secrets.SecretValue{}, err
	}
	if v.IsZero() {
		return secrets.SecretValue{}, secrets.ErrEmptySecret
	}
	c.token, c.cached = v, true
	return v, nil
}

// authorize 是明文唯一出现的地方——**一瞬**:Reveal 的结果直接进请求头,
// 不落任何变量之外的地方,也不参与任何错误信息的拼接(宪法 7 条)。
func (c *RealClient) authorize(ctx context.Context, op string, req *http.Request) error {
	v, err := c.credential(ctx)
	if err != nil {
		return connector.NewError(connector.KindAuth, op, err)
	}
	switch c.cfg.Source {
	case SourceSub2API:
		req.Header.Set(sub2apiAuthHeader, v.Reveal())
	case SourceNewAPI:
		req.Header.Set(newapiAuthHeader, newapiAuthScheme+v.Reveal())
	}
	return nil
}

// respMeta 是一次响应的元信息,用来推导新鲜度。
type respMeta struct {
	receivedAt time.Time
	serverDate time.Time
	status     int
}

// observedAt 给出这次响应可用的最佳观测时刻:上游数据时间戳(两个上游的
// 用户列表都不给)> 上游 Date 头 > 本地接收时刻。
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
func (c *RealClient) get(ctx context.Context, op, routePath string, query url.Values, out any) (respMeta, error) {
	if err := ctx.Err(); err != nil {
		return respMeta{}, connector.NewError(connector.KindUnavailable, op, err)
	}

	target := *c.endpoint
	target.Path = strings.TrimSuffix(c.endpoint.Path, "/") + routePath
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return respMeta{}, connector.NewError(connector.KindInternal, op, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if err := c.authorize(ctx, op, req); err != nil {
		return respMeta{}, err
	}

	resp, err := c.http.Do(req)
	meta := respMeta{receivedAt: c.clock()}
	if err != nil {
		return meta, classifyTransportError(err, op)
	}
	defer func() {
		// 把剩余响应体读完再关,连接才能复用;读多少有上限,免得「关个连接」
		// 变成「把一个畸形的巨大响应读完」。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	meta.status = resp.StatusCode
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		meta.serverDate = t.UTC()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// 上游的响应体一个字都不进错误链:它可能带用户数据,极端情况下还
		// 可能把请求头原样回显出来(宪法 7 条)。
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
		// json 的错误只含偏移量与类型名,不含响应内容,可以安全进错误链。
		return meta, connector.NewError(connector.KindBadResponse, op, err)
	}
	return meta, nil
}

// classifyStatus 把 HTTP 状态码映射成稳定分类(ADR-004)。
func classifyStatus(code int) connector.ErrorKind {
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusLocked:
		// 423 与 sub2api 的合规确认门同一处置:这个账号现在没被授权用,
		// 归 auth 而不是 bad_response(否则会被读成"上游响应格式非法")。
		return connector.KindAuth
	case code == http.StatusTooManyRequests:
		return connector.KindRateLimited
	case code == http.StatusNotFound, code == http.StatusMethodNotAllowed, code == http.StatusNotImplemented:
		// 路由不存在 = 这个上游版本没有这项能力,不是「上游挂了」。
		return connector.KindNotSupported
	case code >= 500:
		return connector.KindUnavailable
	default:
		// 4xx 里剩下的(400/409/422…)都是「我们发的请求上游看不懂」。
		// 3xx 也落在这里,但实际上到不了:重定向在传输层就被拒了。
		return connector.KindBadResponse
	}
}

// classifyTransportError 把传输层错误映射成稳定分类。
//
// 关键在于**不覆盖已有分类**:allowlist 拒绝(forbidden_target)与重定向
// 拒绝是护栏自己给出的判断,被 http.Client 包进 *url.Error 之后如果一律
// 改判成 unavailable,「配置把目标写错了」就会伪装成「上游挂了」。
func classifyTransportError(err error, op string) error {
	var ce *connector.Error
	if errors.As(err, &ce) {
		return connector.NewError(ce.Kind, op, err)
	}
	return connector.NewError(connector.KindUnavailable, op, err)
}

// connectorBadSource / connectorBadCursor / connectorBadPeriod 是参数错误。
//
// 归 bad_response 而非 internal:参数是调用方给的,让它去改请求。
func connectorBadSource(source string) error {
	return connector.NewError(connector.KindBadResponse, "platformusers.list_users",
		fmt.Errorf("未知来源 %q", source))
}

func connectorBadCursor(cursor string) error {
	return connector.NewError(connector.KindBadResponse, "platformusers.list_users",
		fmt.Errorf("游标 %q 不合法", cursor))
}

// connectorBadPeriod 包装区间参数错误(业务日写法、未知粒度)。
//
// 把原始 err 裹进去而不是换一句自己的话:那条 err 已经说清了是哪一项不合法
// (「未知统计粒度 "weekly"」比「区间参数不合法」有用得多),而它是我们自己
// 契约层产生的文本,不是上游正文,不涉及透传风险。
func connectorBadPeriod(err error) error {
	return connector.NewError(connector.KindBadResponse, "platformusers.list_users", err)
}
