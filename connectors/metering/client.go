package metering

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
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 两个上游驱动（sub2api.go / newapi.go）共用的只读 HTTP 底座。
//
// 四道只读闸（ADR-018）在 HTTP 通道上的落点，与 connectors/sub2api/client.go
// 完全一致：
//
//	闸 1 拒绝可写连接配置  → connector.Config.Validate()，构造第一步就跑；
//	闸 2 强制只读          → connector.NewReadOnlyClientWithBase：只放行 GET/HEAD，
//	                        其余方法在 RoundTrip 里就被拒，请求根本不发出；
//	闸 3 复核服务端只读     → HTTP 侧能做的是目标主机精确 allowlist + 拒绝重定向
//	                        （一个 302 就能把请求引到 allowlist 之外），
//	                        以及要求上游发的是只读账号——后者平台强制不了，
//	                        必须在发号那一步保证；
//	闸 4 包内无写路径      → ReadClient 接口只有读方法，本文件也只有 GET。

const (
	// defaultRequestTimeout 是单次请求的兜底超时。
	// 调用方漏填超时不该变成「没有超时」（规格 §18.1-4）。
	defaultRequestTimeout = 15 * time.Second

	// maxResponseBytes 限制单次响应体大小。
	//
	// 成本取数的响应本该只有几十字节（一个 actual_cost 或一个 quota），
	// 上游返回几百 MB 只可能是出了 bug 或返回了别的东西；
	// 采集任务不该把 worker 的内存吃光。
	maxResponseBytes = 8 << 20

	// userAgent 让上游的访问日志能分辨这些读取来自哪个平台组件。
	userAgent = "xingmang-platform/connector-metering-v" + ContractVersion

	// credentialCaller 是凭据审计里的请求方身份（规格 §4.5）。
	credentialCaller = "connector:" + ConnectorKey

	// unknownVersion 是上游没给版本时的占位值。
	//
	// 契约要求「即便不支持也要给出探测值与指纹」，所以这里不能留空；
	// 而 unknown 一定不在兼容矩阵内，于是自然 Supported=false——
	// 「读不到版本」和「版本不支持」在决策上本来就该同样保守。
	unknownVersion = "unknown"
)

// httpBase 是两个驱动共用的连接状态。
type httpBase struct {
	http        *http.Client
	endpoint    *url.URL
	provider    secrets.SecretProvider
	now         func() time.Time
	businessDay *time.Location
	currency    string
	supported   []string
}

// Option 调整客户端的构造。
type Option func(*clientOptions)

type clientOptions struct {
	base              http.RoundTripper
	now               func() time.Time
	businessDay       *time.Location
	currency          string
	supportedVersions []string
}

// WithBaseTransport 指定底层 RoundTripper（默认 http.DefaultTransport）。
//
// 存在的唯一理由是契约测试要连 httptest.NewTLSServer 的自签证书。
// **护栏一个都不能少**：base 依旧被 connector.ReadOnlyTransport 包在里面，
// 只读方法限制、主机 allowlist、拒绝重定向在测试路径与生产路径上是同一份代码。
func WithBaseTransport(rt http.RoundTripper) Option {
	return func(o *clientOptions) { o.base = rt }
}

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(o *clientOptions) { o.now = now }
}

// WithBusinessDayTimezone 声明业务日切日时区。
//
// 默认 CST 固定 +08:00 无夏令时（★口径常量，设计稿 §4，与 SoloAI cstNow 一致）。
// 它在这里有实打实的后果：newapi 的成本端点按时间戳区间取数，那个区间的起点
// 就是本时区的当日零点；sub2api 的端点只回答「今天」，而「今天是哪天」
// 同样由它决定。收入与成本必须共用同一个时间权威——各切各的会让
// 「收入记 D、成本记 D+1」且**不报错**。
func WithBusinessDayTimezone(loc *time.Location) Option {
	return func(o *clientOptions) { o.businessDay = loc }
}

// WithCurrency 声明上游的记账币种（默认 DefaultCurrency）。
//
// 币种决定最小货币单位的小数位，换算路径全靠它，所以它是显式配置而不是
// 猜出来的：猜错币种的金额是**看起来对**的错数字。
func WithCurrency(code string) Option {
	return func(o *clientOptions) { o.currency = code }
}

// WithSupportedVersions 覆盖兼容矩阵。
//
// 给运维一条不改代码就能应急的路：上游临时升到一个还没登记的版本时，
// 与其让整条采集链路判为不支持而降级，不如由人显式声明「这个版本我看过了」。
func WithSupportedVersions(versions ...string) Option {
	return func(o *clientOptions) { o.supportedVersions = versions }
}

// DefaultCurrency 是计量型渠道的记账币种。
//
// ★口径常量（设计稿 §4）：计量型台账**全程 USD**——sub2api 的 actual_cost
// 与 newapi 的 quota/quota_per_unit 口径本就是美元（SoloAI relaymon 两个
// 取数函数的注释都写的是美元）。写死成 CNY 会得到一批**看起来完全正常**
// 的错数字，所以它是显式常量 + WithCurrency 可覆盖，不是猜出来的。
const DefaultCurrency = "USD"

// newHTTPBase 做构造期的全部校验。
//
// 构造期**不做任何 I/O**：凭据在首次读取时才解析，用的是那次请求的 ctx，
// 取消与超时才管得住它；连接也不预热。因此本函数返回的错误只可能是配置错误。
func newHTTPBase(
	cfg connector.Config, sp secrets.SecretProvider, op string,
	defaultVersions []string, opts []Option,
) (*httpBase, error) {
	options := clientOptions{
		now:               time.Now,
		businessDay:       DefaultBusinessDayLocation(),
		currency:          DefaultCurrency,
		supportedVersions: defaultVersions,
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
		options.businessDay = DefaultBusinessDayLocation()
	}

	if cfg.Timeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）。
		// 放在 Validate 之前：这是补默认值，不是放宽校验。
		cfg.Timeout = defaultRequestTimeout
	}
	// 闸 1：拒绝可写连接配置（ADR-018）。https、CredentialRef 形态、
	// 非空 allowlist、endpoint 主机自己也在 allowlist 内——全在这里。
	if err := cfg.Validate(); err != nil {
		return nil, connector.NewError(connector.KindInternal, op, err)
	}
	if sp == nil {
		return nil, connector.NewError(connector.KindInternal, op,
			errors.New("secret provider 为空：没有 Provider 就解析不出凭据"))
	}
	endpoint, err := url.Parse(strings.TrimSuffix(cfg.Endpoint, "/"))
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, op, err)
	}

	return &httpBase{
		// 闸 2/4：只读、限目标、拒重定向、带超时的 HTTP 客户端。
		// 连接配置本身不留在客户端里：它已经被 Validate 消化成这几个字段，
		// 留一份完整副本只会多一个可能被日志打印出来的地方。
		http:        connector.NewReadOnlyClientWithBase(options.base, cfg.TargetAllowlist, cfg.Timeout),
		endpoint:    endpoint,
		provider:    sp,
		now:         options.now,
		businessDay: options.businessDay,
		currency:    strings.ToUpper(strings.TrimSpace(options.currency)),
		supported:   append([]string(nil), options.supportedVersions...),
	}, nil
}

// DefaultBusinessDayLocation 返回 CST 固定 +08:00（无夏令时）。
//
// 用 FixedZone 而不是 time.LoadLocation("Asia/Shanghai")：后者依赖容器里有
// tzdata（scratch 镜像里会失败），而且是一个**会随 tzdata 更新而变**的定义。
// 核算口径里的 CST 必须是固定偏移（§4 标 ★，与 SoloAI cstNow 一致），
// 不该因为某次基础镜像升级就换个口径。
func DefaultBusinessDayLocation() *time.Location {
	return time.FixedZone("+08:00", 8*3600)
}

func (c *httpBase) clock() time.Time { return c.now().UTC() }

// resolve 解析一个 CredentialRef。
//
// 明文从这里出来，只走到调用方拼头那一行，再没有第三个去处：
// 不进日志、不进错误、不进任何返回值（宪法 7 条）。
//
// 每次都问 Provider 而不缓存：本包的凭据是**每令牌**的（sub2api 侧），
// 一轮采集会遍历几十把 key，缓存的收益是省几次 Provider 调用，
// 代价是让明文在进程里活得更久。审计上也更清楚——「这把 key 什么时候被用过」
// 逐次可查（规格 §4.5）。
func (c *httpBase) resolve(
	ctx context.Context, op, refText, purpose string,
) (secrets.SecretValue, error) {
	ref, err := secrets.ParseCredentialRef(strings.TrimSpace(refText))
	if err != nil {
		return secrets.SecretValue{}, connector.NewError(connector.KindInternal, op, err)
	}
	v, err := c.provider.Resolve(secrets.WithCaller(ctx, credentialCaller), ref, purpose)
	if err != nil {
		// 解析失败归 auth：拿不到凭据与凭据无效，对调用方是同一件事（读不了），
		// 而且都得靠人去看凭据配置，不是靠重试能好的。
		return secrets.SecretValue{}, connector.NewError(connector.KindAuth, op, err)
	}
	if v.IsZero() {
		return secrets.SecretValue{}, connector.NewError(connector.KindAuth, op, secrets.ErrEmptySecret)
	}
	return v, nil
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
// 优先级：上游数据时间戳（由各 fetch 传入）> 上游 Date 头 > 本地接收时刻。
// 退到本地接收时刻时，语义已经从「数据什么时候被观测」滑向「我们什么时候问的」
// ——这两者在上游有缓存时能差出一个缓存周期。所以退化路径要有注释、有文档，
// 不能悄悄退。
//
// ⚠️ 本包的两个上游**都没有**在响应体里给数据时间戳（sub2api 的 /v1/usage
// 只有金额，newapi 的 /api/log/self/stat 只有 quota），所以这里实际总是走
// Date 头或本地时刻。账号到位后要核对的第一梯队事项之一。
func (m respMeta) observedAt(upstream time.Time) time.Time {
	if !upstream.IsZero() {
		return upstream.UTC()
	}
	if !m.serverDate.IsZero() {
		return m.serverDate.UTC()
	}
	return m.receivedAt.UTC()
}

// watermark 生成数据水位。
//
// 契约要求水位非空。带上业务日：光有时间戳分不清「这是哪一天的读数」，
// 而成本台账最怕的正是日期错位（§4：收入与成本必须共用同一个时间权威）。
func watermark(day string, observedAt time.Time) string {
	return fmt.Sprintf("day:%s@%d", day, observedAt.UTC().Unix())
}

// get 发一次只读请求并把 JSON 解进 out。
//
// authorize 由调用方提供：两个上游的鉴权头形状完全不同（sub2api 是
// Authorization: Bearer <令牌明文>，newapi 是 New-Api-User + Cookie），
// 而**明文只能在那个回调里出现一瞬**。
func (c *httpBase) get(
	ctx context.Context, op, routePath string, query url.Values,
	authorize func(*http.Request) error, out any,
) (respMeta, error) {
	// 上下文已经取消就别再发请求了：契约要求取消后立即返回。
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
	if authorize != nil {
		if err := authorize(req); err != nil {
			return respMeta{}, err
		}
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
func classifyStatus(code int) connector.ErrorKind {
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		// ⚠️ sub2api 的 /v1/usage 对**未分组**的 apikey 返 403（§3.1）：
		// 那不是凭据错了，是那把 key 在上游还没被放进分组。两者的处置都是
		// 「去上游看这把 key 的配置」，所以归同一类；差别写在 RUNBOOK 里。
		return connector.KindAuth
	case code == http.StatusLocked:
		// 423 在 sub2api 是合规确认门（0.1.183 起的 AdminComplianceGuard）。
		// 归 auth 而不是 not_supported：能力上游是有的，只是这个账号现在
		// 没被授权用，和 401/403 一样原样重试永远不会自己变好。
		return connector.KindAuth
	case code == http.StatusTooManyRequests:
		// ⚠️ 成本采集是**每令牌一次请求**，令牌多了很容易撞限流——
		// SoloAI 正因此把 sk- 加密缓存下来（0084C）以规避 newapi 取 key 限流。
		// 归 rate_limited 让调用方能退避重试，而不是把它当成上游挂了。
		return connector.KindRateLimited
	case code == http.StatusNotFound, code == http.StatusMethodNotAllowed,
		code == http.StatusNotImplemented:
		// 路由不存在 = 这个上游版本没有这项能力，不是「上游挂了」。
		// 分开归类才让调用方做得出正确决策：not_supported 重试没用，
		// unavailable 重试可能有用。
		return connector.KindNotSupported
	case code >= 500:
		return connector.KindUnavailable
	default:
		// 4xx 里剩下的（400/409/422…）都是「我们发的请求上游看不懂」，
		// 归 bad_response：重试同样的请求不会有不同结果。
		return connector.KindBadResponse
	}
}

// classifyTransportError 把传输层错误映射成稳定分类。
//
// 关键在于**不覆盖已有分类**：allowlist 拒绝（forbidden_target）与重定向拒绝
// 是护栏自己给出的判断，被 http.Client 包进 *url.Error 之后如果在这里一律
// 改判成 unavailable，「配置把目标写错了」就会伪装成「上游挂了」，
// 运维会去查上游而不是去查配置。
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
// 矩阵项写 "0.1" 表示整条 minor 线都认，写 "0.1.183" 表示只认那个补丁版本。
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
// 用摘要而不是把原始串拼起来：指纹会进日志与看板，而上游的 build 信息里
// 出现过主机名、内部路径这类东西——摘要一样能比出「变了没有」，
// 但不会顺手把上游的内部细节搬到我们的日志里。
func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// healthDetail 给出与分类一一对应的安全短语。
//
// 固定短语而不是拼上游文本：Detail 会进日志与看板，
// 上游的错误正文里出现过 token 片段与用户邮箱（ADR-004 的由来）。
func healthDetail(kind connector.ErrorKind) string {
	switch kind {
	case connector.KindAuth:
		return "upstream rejected the read-only credential"
	case connector.KindRateLimited:
		return "upstream rate limited the probe"
	case connector.KindNotSupported:
		return "upstream has no status endpoint at the configured path"
	case connector.KindForbiddenTarget:
		return "target host is not in the allowlist"
	case connector.KindBadResponse:
		return "upstream status response was not understood"
	default:
		return "upstream status endpoint unreachable or returned non-2xx"
	}
}

// businessDayBounds 给出某业务日在该时区下的 [起, 止) 时刻。
//
// 起点是当日 00:00:00，止点是次日 00:00:00——**左闭右开**。用「次日零点」
// 而不是「当日 23:59:59」：后者会漏掉最后一秒里的消费，而那一秒的消费
// 会静静地消失，不属于任何一天。
func (c *httpBase) businessDayBounds(day string) (time.Time, time.Time, error) {
	if err := ValidateBusinessDay(day); err != nil {
		return time.Time{}, time.Time{}, err
	}
	start, err := time.ParseInLocation(BusinessDayLayout, day, c.businessDay)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, start.AddDate(0, 0, 1), nil
}

// isToday 报告 day 是否就是该时区下的今天。
//
// sub2api 的成本端点只回答「今天」（响应字段就叫 today，没有日期参数），
// 所以它必须能判断调用方要的是不是今天——要的是别的天就返回 not_supported，
// 而**绝不**把今天的数当成那一天的返回回去。
func (c *httpBase) isToday(day string) bool {
	return c.clock().In(c.businessDay).Format(BusinessDayLayout) == day
}
