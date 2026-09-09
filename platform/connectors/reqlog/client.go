package reqlog

import (
	"context"
	"encoding/base64"
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

// 真实只读客户端的**骨架**（XM-0039）。
//
// 已经落地并有测试的部分（不依赖 reqlog 的 API 形状，所以现在就能做对）：
//
//	闸 1 拒绝可写连接配置 → connector.Config.Validate()，构造第一步就跑；
//	闸 2 强制只读        → connector.NewReadOnlyClient：只放行 GET/HEAD；
//	闸 3 复核服务端只读   → HTTP 通道上做到主机精确 allowlist + 拒绝重定向；
//	闸 4 包内无写路径     → ReadClient 接口只有读方法，本文件只有 GET。
//	凭据                → 只经 CredentialRef，明文只在 authorize() 存活一瞬。
//
// **还没有的部分**：路由与响应解析。reqlog 控制台的 HTTP API 形状尚未见到
// 源码（/root/reqlog 的单文件 Go 程序，1,336 行）。所以 ListRequests 与
// RequestContent 一律返回 not_supported，而不是对着一组编出来的路由发请求。
//
// 为什么不先编一组路由「等真实的来了再改」：编出来的路由会 404，而 404 在
// classifyStatus 里归 not_supported，运维看到的是同一个分类——但中间白白发了
// 一次请求，也让「这条链路没接通」看起来像「上游少了个接口」。更糟的是
// 编出来的**字段名**：它们会一路长进前端的类型定义、测试的断言与文档，
// 等真实形状到位那天，要改的就不是一个文件了。
//
// 接入清单（拿到 reqlog 源码或端点清单后按顺序做）：
//
//  1. 把列表/详情两条路由与响应形状写进 upstream.go（新建），
//     本文件的 get() 与错误纪律不用动。构造 Snapshot 时**必须**把
//     Instance 填成 cfg.ServiceInstanceID——前端靠它区分演示数据与真实
//     用户对话，留空会让界面默认当成真的（contracttest 会拦下这一条）；
//  2. 逐字段核对 contracts/connectors/reqlog.read.v1.md 的「等真实 API
//     核对的字段清单」，把核对结论写回那张表；
//  3. 让真实实现跑一遍 contracttest.RunSuite——Fake 已经过了，
//     套件因此是有效的判据；
//  4. 处理 endpoint 的 https 前提，见 ErrLoopbackEndpointNotAllowed。
const (
	// defaultRequestTimeout 是单次请求的兜底超时。
	defaultRequestTimeout = 15 * time.Second

	// maxResponseBytes 限制单次响应体大小。
	//
	// 比 sub2api 的 8 MiB 大一档：这条通道要取回**请求正文与完整 SSE 流**，
	// 而契约层对单侧原始载荷的上限是 512 KiB。留 16 MiB 是给「上游先给全量、
	// 由我们截断」这条路径留的余量；真正的截断边界是 MaxRawPayloadBytes。
	maxResponseBytes = 16 << 20

	// userAgent 让 reqlog 的访问日志能分辨这些读取来自哪个平台组件。
	userAgent = "xingmang-platform/connector-reqlog-v" + ContractVersion

	// credentialPurpose 进凭据审计（规格 §4.5）。
	credentialPurpose = "reqlog console readonly"
	// credentialCaller 是审计里的请求方身份。
	credentialCaller = "connector:" + ConnectorKey

	// unknownVersion 是上游没给版本时的占位值。
	unknownVersion = "unknown"

	// authHeader 是 Basic Auth 的落点。
	authHeader = "Authorization"
)

// ErrConsoleAPIShapeUnverified 是数据读取在真实 API 形状核实之前的确定性失败。
//
// 分类为 not_supported（不是 internal）：它说的是「本部署还不具备真实读取
// 能力」，运维一看 error_code 就知道这是在等一份还没到手的输入，
// 而不是自己哪里配错了——与 jobs.ErrNewAPIRealClientUnavailable 同一条思路。
var ErrConsoleAPIShapeUnverified = errors.New(
	"reqlog 控制台 API 形状尚未核实（XM-0039）：real 模式的路由与响应解析待补，" +
		"当前只有 fake 模式可用")

// ErrLoopbackEndpointNotAllowed 记录一处**已知的、尚未拍板的**冲突。
//
// reqlog 控制台按实现报告是 `http://127.0.0.1:9300`（回环 + 明文 HTTP +
// Basic Auth，靠 SSH 隧道访问）。而 connector.Config.Validate() 硬性要求
// endpoint 是 **https**（ADR-018 闸 1）。两者今天对不上：照现状配 real 模式，
// 构造客户端这一步就会失败。
//
// 三条出路，**都需要人来定**，本任务不擅自选：
//
//	a. 平台与 reqlog 同机部署，控制台前面挂一层本地 TLS（改部署，不改代码）；
//	b. 隧道那一端终结 TLS，平台连的是 https（改运维流程）；
//	c. 放宽 Validate 允许回环地址走 http（**改的是全体连接器共用的安全闸**，
//	   代价最大、影响面最广，需要单独的 ADR）。
//
// 这个变量存在的意义是让这条冲突**在代码里有一个位置**而不是只躺在 PR 描述
// 里：配置 real 模式失败时，错误链里会带上它，人不用去翻文档就知道卡在哪。
var ErrLoopbackEndpointNotAllowed = errors.New(
	"reqlog 控制台是 http://127.0.0.1:9300（回环明文），而只读连接配置要求 https：" +
		"接入前需先定 TLS 落点（同机本地 TLS / 隧道端终结 / 放宽闸 1），见 client.go 注释")

// Option 调整客户端的构造。
type Option func(*clientOptions)

type clientOptions struct {
	base              http.RoundTripper
	now               func() time.Time
	supportedVersions []string
}

// WithBaseTransport 指定底层 RoundTripper（默认 http.DefaultTransport）。
//
// 存在的唯一理由是契约测试要连 httptest 服务器。**护栏一个都不能少**：
// base 依旧被 connector.ReadOnlyTransport 包在里面，只读方法限制、
// 主机 allowlist、拒绝重定向在测试路径与生产路径上是同一份代码。
func WithBaseTransport(rt http.RoundTripper) Option {
	return func(o *clientOptions) { o.base = rt }
}

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(o *clientOptions) { o.now = now }
}

// WithSupportedVersions 覆盖兼容矩阵。
func WithSupportedVersions(versions ...string) Option {
	return func(o *clientOptions) { o.supportedVersions = versions }
}

type client struct {
	http      *http.Client
	endpoint  *url.URL
	provider  secrets.SecretProvider
	ref       secrets.CredentialRef
	now       func() time.Time
	supported []string

	// mu 保护凭据缓存。缓存的意义是一轮读取里的几次请求不必各打一次
	// Provider（每次都会留一条审计记录）。
	mu     sync.Mutex
	token  secrets.SecretValue
	cached bool
}

// NewClient 构造 reqlog 的真实只读客户端。
//
// 构造期**不做任何 I/O**：凭据在首次读取时才解析，用的是那次请求的 ctx。
// 因此本函数返回的错误只可能是配置错误。
//
// ⚠️ 当前 endpoint 必须是 https（闸 1 的要求）。reqlog 控制台的现状与之冲突，
// 见 ErrLoopbackEndpointNotAllowed。
func NewClient(cfg connector.Config, sp secrets.SecretProvider, opts ...Option) (ReadClient, error) {
	options := clientOptions{
		now:               time.Now,
		supportedVersions: SupportedUpstreamVersions,
	}
	for _, o := range opts {
		if o != nil {
			o(&options)
		}
	}
	if options.now == nil {
		options.now = time.Now
	}
	if cfg.Timeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）。
		// 放在 Validate 之前：这是补默认值，不是放宽校验。
		cfg.Timeout = defaultRequestTimeout
	}
	// 闸 1：拒绝可写连接配置（ADR-018）。https、CredentialRef 形态、
	// 非空 allowlist、endpoint 主机自己也在 allowlist 内——全在这里。
	if err := cfg.Validate(); err != nil {
		// 把已知的 https 冲突接进错误链：配 real 模式失败的人第一眼看到的
		// 应该是「这条冲突还没拍板」，而不是一句泛泛的「endpoint 必须是 https」
		if isLoopbackEndpoint(cfg.Endpoint) {
			err = fmt.Errorf("%w: %w", ErrLoopbackEndpointNotAllowed, err)
		}
		return nil, connector.NewError(connector.KindInternal, "reqlog.client.config", err)
	}
	ref, err := secrets.ParseCredentialRef(cfg.CredentialRef)
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "reqlog.client.config", err)
	}
	if sp == nil {
		return nil, connector.NewError(connector.KindInternal, "reqlog.client.config",
			errors.New("secret provider 为空：没有 Provider 就解析不出凭据"))
	}
	endpoint, err := url.Parse(strings.TrimSuffix(cfg.Endpoint, "/"))
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "reqlog.client.config", err)
	}

	return &client{
		// 闸 2/4：只读、限目标、拒重定向、带超时的 HTTP 客户端
		http:      connector.NewReadOnlyClientWithBase(options.base, cfg.TargetAllowlist, cfg.Timeout),
		endpoint:  endpoint,
		provider:  sp,
		ref:       ref,
		now:       options.now,
		supported: append([]string(nil), options.supportedVersions...),
	}, nil
}

// isLoopbackEndpoint 判断 endpoint 是否指向回环地址。
//
// 只用于把错误信息说得更准，不参与任何放行判定——放行与否完全由
// connector.Config.Validate() 决定，这里读不到、也不该读那道闸的结论。
func isLoopbackEndpoint(endpoint string) bool {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (c *client) clock() time.Time { return c.now().UTC() }

// credential 解析并缓存凭据。
//
// 明文从这里出来，只走到 applyBasicAuth 那一行拼头，再没有第三个去处：
// 不进日志、不进错误、不进任何返回值（宪法 7 条）。
func (c *client) credential(ctx context.Context) (secrets.SecretValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached {
		return c.token, nil
	}
	// 只在成功时缓存：瞬时失败（Provider 抖动、变量还没注入）下一次读取会重试，
	// 而不是把失败也记住
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

// applyBasicAuth 把凭据拼成 Basic Auth 头。
//
// **凭据的约定形态是 `用户名:口令` 整串。** 为什么不拆成「配置里写用户名 +
// 密钥里只放口令」：connector.Config 里没有用户名字段，而给它加一个，等于让
// 「凭据的一半」有了一个不受 CredentialRef 管辖、会被打进日志与审计摘要的
// 落点。整串走 CredentialRef，两半就受同一套纪律管（宪法 7 条）。
//
// 代价是轮换时要连用户名一起换一次值——这是可接受的：轮换本来就是改一个
// 环境变量再重启（见 docs/modules/connector/RUNBOOK.md）。
//
// 明文在这里存活一瞬：Reveal 的结果直接编码进请求头，不落任何中间变量，
// 也不参与任何错误信息的拼接。
func applyBasicAuth(req *http.Request, v secrets.SecretValue) {
	req.Header.Set(authHeader,
		"Basic "+base64.StdEncoding.EncodeToString([]byte(v.Reveal())))
}

// authorize 解析凭据并拼上请求头。
//
// 解析失败归 auth：拿不到凭据与凭据无效，对调用方是同一件事（读不了），
// 而且都得靠人去看凭据配置，不是靠重试能好的。
func (c *client) authorize(ctx context.Context, op string, req *http.Request) error {
	v, err := c.credential(ctx)
	if err != nil {
		return connector.NewError(connector.KindAuth, op, err)
	}
	applyBasicAuth(req, v)
	return nil
}

// respMeta 是一次响应的元信息，用来推导新鲜度。
type respMeta struct {
	receivedAt time.Time
	serverDate time.Time
	status     int
	latency    time.Duration
}

// observedAt 给出这次响应可用的最佳观测时刻。
//
// reqlog 是一个抄录器：它的「数据观测时刻」就是我们问它的时刻——它不缓存、
// 不聚合，读到什么就是库里当下有什么。所以这里比 sub2api 那边简单：
// 上游 Date 头 > 本地接收时刻，没有第三层。
func (m respMeta) observedAt() time.Time {
	if !m.serverDate.IsZero() {
		return m.serverDate.UTC()
	}
	return m.receivedAt.UTC()
}

// get 发一次只读请求并把响应体读回来。
//
// 返回原始字节而不是解进结构体：响应形状还没定（见文件顶部），
// 而「怎么发请求」这一段与形状无关，现在就该是对的、被测过的。
//
// out 的解析由将来的 upstream.go 负责；本函数只保证：只读、限目标、
// 带超时、带 Basic Auth、有大小上限、错误分类稳定。
func (c *client) get(ctx context.Context, op, routePath string, query url.Values) ([]byte, respMeta, error) {
	// 上下文已经取消就别再发请求了：契约要求取消后立即返回
	if err := ctx.Err(); err != nil {
		return nil, respMeta{}, connector.NewError(connector.KindUnavailable, op, err)
	}

	target := *c.endpoint
	target.Path = strings.TrimSuffix(c.endpoint.Path, "/") + routePath
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, respMeta{}, connector.NewError(connector.KindInternal, op, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if err := c.authorize(ctx, op, req); err != nil {
		return nil, respMeta{}, err
	}

	start := c.clock()
	resp, err := c.http.Do(req)
	meta := respMeta{receivedAt: c.clock()}
	meta.latency = meta.receivedAt.Sub(start)
	if err != nil {
		return nil, meta, classifyTransportError(err, op)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	meta.status = resp.StatusCode
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		meta.serverDate = t.UTC()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// reqlog 的响应体**一个字都不进错误链**：它装的正是用户请求的明文。
		// 这条纪律在本连接器上比在别处更硬——别处泄漏的是上游的内部细节，
		// 这里泄漏的是用户对话内容（宪法 7 条 + 交接文档 §9.4）。
		return nil, meta, connector.NewError(classifyStatus(resp.StatusCode), op,
			fmt.Errorf("upstream status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, meta, classifyTransportError(err, op)
	}
	if len(body) > maxResponseBytes {
		return nil, meta, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应体超过 %d 字节上限", maxResponseBytes))
	}
	return body, meta, nil
}

// classifyStatus 把 HTTP 状态码映射成稳定分类（ADR-004）。
func classifyStatus(code int) connector.ErrorKind {
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return connector.KindAuth
	case code == http.StatusTooManyRequests:
		return connector.KindRateLimited
	case code == http.StatusNotFound, code == http.StatusMethodNotAllowed,
		code == http.StatusNotImplemented:
		return connector.KindNotSupported
	case code >= 500:
		return connector.KindUnavailable
	default:
		return connector.KindBadResponse
	}
}

// classifyTransportError 把传输层错误映射成稳定分类。
//
// 关键在于**不覆盖已有分类**：allowlist 拒绝（forbidden_target）与重定向
// 拒绝是护栏自己给出的判断，在这里一律改判成 unavailable 的话，
// 「配置把目标写错了」就会伪装成「上游挂了」。
func classifyTransportError(err error, op string) error {
	var ce *connector.Error
	if errors.As(err, &ce) {
		return connector.NewError(ce.Kind, op, err)
	}
	return connector.NewError(connector.KindUnavailable, op, err)
}

// unverified 是四个数据方法共用的确定性失败。
func unverified(op string) error {
	return connector.NewError(connector.KindNotSupported, op, ErrConsoleAPIShapeUnverified)
}

// Version 探测 reqlog 版本。
//
// ⚠️ reqlog 是否发布版本号未知（见 fakeDefaultVersion 的说明）。在形状核实
// 之前返回 not_supported，而不是回一个 `unknown` + Supported=false ——后者
// 会让看板显示「探测到了，只是版本不受支持」，那是**一句假话**：我们根本
// 没问过它。
func (c *client) Version(context.Context) (connector.VersionInfo, error) {
	return connector.VersionInfo{}, unverified("reqlog.service.version_read")
}

// Health 返回运营健康状态。
//
// 与其他方法不同，这里返回**结构化的不健康结果而不是错误**：契约里
// 「不健康」是一个要能落进看板的结果，而「这条链路还没接通」正是一种
// 不健康。让它报错，看板上这一格就会是空的，而不是显示「未接通」。
func (c *client) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := ctx.Err(); err != nil {
		return connector.HealthResult{}, connector.NewError(
			connector.KindUnavailable, "reqlog.health.read", err)
	}
	return connector.HealthResult{
		Healthy:   false,
		CheckedAt: c.clock(),
		ErrorKind: connector.KindNotSupported,
		Detail:    "reqlog console API shape not verified yet",
	}, nil
}

// Capabilities 返回本连接**当前实际可用**的能力。
//
// 形状核实之前是**空清单**，不是 ReadCapabilities 的副本：一项没有代码可以
// 兑现的能力，声明出来就是说谎（与 sub2api 的 Capabilities 同一条纪律，
// 只是那边靠探测得出结论，这边靠事实——我们知道自己还没实现）。
func (c *client) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := ctx.Err(); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "reqlog.capabilities", err)
	}
	return []registry.Capability{}, nil
}

// ListRequests 按过滤条件分页读取请求元数据。
//
// 参数照样先校验再失败：一个 source 拼错的调用，应该拿到「参数不对」而不是
// 「功能没上线」——两者的下一步动作完全不同。
func (c *client) ListRequests(ctx context.Context, filter ListFilter) (RequestLogPage, error) {
	const op = "reqlog.requests.read"
	if err := ctx.Err(); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if err := ValidateFilter(filter); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	return RequestLogPage{}, unverified(op)
}

// RequestContent 读取单条请求的完整内容。
func (c *client) RequestContent(ctx context.Context, source, id string) (RequestLogContent, error) {
	const op = "reqlog.request.content_read"
	if err := ctx.Err(); err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if _, err := ParseSource(source); err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if strings.TrimSpace(id) == "" {
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op,
			errors.New("id 为空"))
	}
	return RequestLogContent{}, unverified(op)
}
