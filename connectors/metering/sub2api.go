package metering

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Sub2API 上游的路由与响应形状——**唯一定义处**。
//
// 形状依据：设计稿 §3.1/§3.2 对 SoloAI v2.5.130 的逐行引用
// （`relaymon/sub2api.go:263` FetchSub2APIUsageCost、
//  `admin/sub2api_admin.go:79` AccountRevenue）。上游没有 OpenAPI 规格，
// 所以这里的每个字段名都来自那份标准答案。
//
// ⚠️ **未对真实实例验证过**：真实只读账号到位前，本文件的正确性只由本地
// 假上游的契约测试保证。账号到位后按 RUNBOOK 的验证清单逐项核对——
// 尤其是金额单位与业务日边界，那两样错了之后数字看起来完全正常。
//
// 上游改版时，要改的应该只有这一个文件。

const (
	// sub2apiRouteUsage 是**成本侧**唯一来源（§3.1）。
	//
	// ⚠️ 它用 **apikey 自鉴权**：`Authorization: Bearer <该令牌自己的明文>`，
	// 不是 admin key。所以成本取数是**每令牌一次请求**，每次用不同的凭据。
	// 上游要求该令牌已被分组，否则返 403（见 classifyStatus 的注释）。
	//
	// ⚠️ 响应字段是 `usage.today.actual_cost`——**不是** admin 面板的
	// `trend[].cost`。后者是自营实例口径（connectors/sub2api 在读的那个），
	// 两者同名不同义，混用会让毛利算反（设计稿 §3.1 的显著标注）。
	sub2apiRouteUsage = "/v1/usage"

	// sub2apiRouteAccountStats 是**收入侧**来源（§3.2）。
	// 用 admin key（x-api-key），取 `data.summary.today.user_cost`。
	sub2apiRouteAccountStats = "/api/v1/admin/accounts/%s/stats"

	// sub2apiRouteVersion 是版本探测路由（需 admin 认证）。
	sub2apiRouteVersion = "/api/v1/admin/system/version"

	// sub2apiRouteHealth 挂在引擎根上，不带 /api/v1 前缀，也不需要认证。
	sub2apiRouteHealth = "/health"

	// sub2apiAdminAuthHeader 是 admin 侧的程序化访问头，值形如 "admin-"+64 位十六进制。
	//
	// 不用 Authorization: Bearer <JWT>：那条路走的是登录令牌，会过期，
	// 需要拿账号密码去续——一个每 5 分钟跑一次的采集任务不该握着密码。
	sub2apiAdminAuthHeader = "X-Api-Key"

	// sub2apiCostPurpose / sub2apiRevenuePurpose 进凭据审计（规格 §4.5），
	// 不影响解析结果。分成两个是为了让审计能区分「谁在读成本」与「谁在读收入」
	// ——两者用的是完全不同的凭据（每令牌明文 vs admin key）。
	sub2apiCostPurpose    = "metering sub2api token cost readonly"
	sub2apiRevenuePurpose = "metering sub2api account revenue readonly"
)

// Sub2APISupportedVersions 是兼容矩阵（ADR-004）。
//
// 只写到 major.minor：补丁版本升级不该让整条采集链路判为不支持，
// 而 minor 变更在这个上游意味着接口可能动过，值得人看一眼。
// 与 connectors/sub2api 的矩阵取值一致（同一个上游，同一条 0.1 线）。
var Sub2APISupportedVersions = []string{"0.1"}

type sub2apiClient struct {
	*httpBase

	// adminRef 是账号级 admin key 的引用文本（收入侧 §3.2 用）。
	//
	// 单独存一份而不是留着整个 connector.Config：Config 里还有 endpoint、
	// allowlist 等字段，留一份完整副本只会多一个可能被日志打印出来的地方。
	// 它是**引用**不是凭据，可安全出现在日志与审计中（ADR-014）。
	adminRef string
}

// NewSub2APIClient 构造 Sub2API 的计量取数只读客户端。
//
// cfg.CredentialRef 是**账号级 admin key**（收入侧 §3.2 用）；成本侧的
// 每令牌明文由 TokenRef.CredentialRef 逐次解析（§3.1），不经这里。
func NewSub2APIClient(
	cfg connector.Config, sp secrets.SecretProvider, opts ...Option,
) (ReadClient, error) {
	base, err := newHTTPBase(cfg, sp, "metering.sub2api.config", Sub2APISupportedVersions, opts)
	if err != nil {
		return nil, err
	}
	// 币种决定最小单位小数位，未登记的币种在这里就要拒绝——
	// 猜错的那 100 倍不会有任何症状（宪法 13 条）。
	if _, err := money.CurrencyScale(base.currency); err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.sub2api.config", err)
	}
	// admin key 的引用形态在构造期校验：一个形态就不对的 ref 不该等到
	// 第一次取收入时才炸。
	if _, err := secrets.ParseCredentialRef(cfg.CredentialRef); err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.sub2api.config", err)
	}
	return &sub2apiClient{httpBase: base, adminRef: cfg.CredentialRef}, nil
}

func (c *sub2apiClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	const op = "metering.service.version_read"
	var payload struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
		Version string `json:"version"`
	}
	meta, err := c.get(ctx, op, sub2apiRouteVersion, nil, c.authorizeAdmin(ctx, op), &payload)
	if err != nil {
		return connector.VersionInfo{}, err
	}
	// 上游把版本包在 data 信封里；旧版本直接放顶层。两种都收——
	// 只认一种等于把「0.1.183 上炸」换成「0.1.133 上炸」。
	detected := strings.TrimSpace(payload.Data.Version)
	if detected == "" {
		detected = strings.TrimSpace(payload.Version)
	}
	if detected == "" {
		detected = unknownVersion
	}
	return connector.VersionInfo{
		Detected:    detected,
		Fingerprint: fingerprint(c.endpoint.Host, detected),
		Supported:   versionSupported(detected, c.supported),
		DetectedAt:  meta.receivedAt,
	}, nil
}

func (c *sub2apiClient) Health(ctx context.Context) (connector.HealthResult, error) {
	const op = "metering.health.read"
	start := c.clock()
	var payload struct {
		Status string `json:"status"`
	}
	// /health 不带信封也不需要认证。
	meta, err := c.get(ctx, op, sub2apiRouteHealth, nil, nil, &payload)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// 本进程在关机或调用方超时，不是上游的健康状况——
			// 把它记成「上游不健康」是假信号，比没有信号更糟。
			return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, op, ctxErr)
		}
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: c.clock(),
			LatencyMS: c.clock().Sub(start).Milliseconds(),
			ErrorKind: connector.KindOf(err),
			Detail:    healthDetail(connector.KindOf(err)),
		}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Status), "ok") {
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

// Capabilities 返回本连接**当前实际可用**的能力。
//
// 只探 health 与 version 两条：成本与收入两条路由都需要**具体的**令牌 /
// 账号 id 才发得出去（`/v1/usage` 要该令牌的明文，`/accounts/{id}/stats`
// 要一个真实账号 id），拿一个编造的 id 去探测只会在上游日志里留下一串
// 404，还可能被限流当成攻击。这两条的可用性由第一次真实取数报告，
// 那时的错误分类（auth / not_supported / rate_limited）比一次伪造的探测准。
func (c *sub2apiClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	const op = "metering.capabilities"
	out := make([]registry.Capability, 0, len(ReadCapabilities))

	if _, err := c.get(ctx, op, sub2apiRouteHealth, nil, nil, nil); err == nil {
		out = append(out, "metering.health.read")
	} else if connector.KindOf(err) != connector.KindNotSupported {
		// 认证失败、限流、网络不通：这不是「能力不存在」，是「现在问不出来」。
		// 返回一份猜出来的清单会让调用方以为上游少了能力，从而做出错误的
		// 降级决策——报错更诚实。
		return nil, err
	}

	if _, err := c.get(ctx, op, sub2apiRouteVersion, nil, c.authorizeAdmin(ctx, op), nil); err == nil {
		out = append(out, "metering.service.version_read")
		// 版本路由通了说明 admin key 有效，收入侧因此是可达的（§3.2 同一把 key）
		out = append(out, "metering.account.revenue_read")
	} else if connector.KindOf(err) != connector.KindNotSupported {
		return nil, err
	}

	// 成本能力不探测但**照样声明**：它的凭据是每令牌的，不在连接级别，
	// 这里没有可用来探测的对象。声明它是因为本客户端确实实现了它——
	// 真正的可用性由第一次取数报告。
	out = append(out, "metering.token.usage_read")
	return out, nil
}

// TokenUsage 读一个上游令牌今日的实扣（§3.1）。
//
// ⚠️ **只回答今天**：上游的 `/v1/usage` 响应里那一段就叫 `today`，
// 端点不接受任何日期参数（SoloAI FetchSub2APIUsageCost 也只取 today）。
// 要别的业务日一律返回 not_supported——把今天的数当成那一天的返回回去，
// 是一个看起来完全正常的错数字，比读不到糟得多。
func (c *sub2apiClient) TokenUsage(
	ctx context.Context, token TokenRef, day string,
) (TokenUsage, error) {
	const op = "metering.token.usage_read"

	if err := ValidateBusinessDay(day); err != nil {
		// 非法业务日是**调用方**的错，不该变成一次上游读取
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if strings.TrimSpace(token.CredentialRef) == "" {
		return TokenUsage{}, connector.NewError(connector.KindInternal, op,
			fmt.Errorf("令牌 %s 缺少 credential_ref：sub2api 成本侧要用该令牌自己的明文打 %s（§3.1）",
				token.UpstreamTokenID, sub2apiRouteUsage))
	}
	if !c.isToday(day) {
		return TokenUsage{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("sub2api %s 只回答今天（响应字段为 today，端点无日期参数）；请求的业务日是 %s",
				sub2apiRouteUsage, day))
	}

	// 明文只在这一瞬出现：Reveal 的结果直接进请求头，不落任何变量之外的地方，
	// 也不参与任何错误信息的拼接（宪法 7 条）。
	value, err := c.resolve(ctx, op, token.CredentialRef, sub2apiCostPurpose)
	if err != nil {
		return TokenUsage{}, err
	}
	authorize := func(req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+value.Reveal())
		return nil
	}

	var payload struct {
		Usage struct {
			// Today 是指针：上游在**今日无流量**时给 null（§3.2 对
			// today==null 的说明同样适用于用量段）。指针让「今天没有消费」
			// 与「上游没给这个字段」可以分开，而 0 值做不到这个区分。
			Today *struct {
				ActualCost money.RawAmount `json:"actual_cost"`
			} `json:"today"`
		} `json:"usage"`
	}
	meta, err := c.get(ctx, op, sub2apiRouteUsage, nil, authorize, &payload)
	if err != nil {
		return TokenUsage{}, err
	}

	observedAt := meta.observedAt(time.Time{})
	usage := TokenUsage{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  watermark(day, observedAt),
		},
		UpstreamTokenID: token.UpstreamTokenID,
		Day:             day,
		Currency:        c.currency,
	}

	// today 为 null / 字段缺失 = **今日零流量 = 已知 0**（§3.2、§5.1）。
	// 这不是「未知」：未知要靠返回 error 表达，让台账落 NULL 而不是 0。
	if payload.Usage.Today == nil || payload.Usage.Today.ActualCost.Empty() {
		return usage, nil
	}

	minor, err := payload.Usage.Today.ActualCost.MinorUnits(UsageScale)
	if err != nil {
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	usage.UsageMinorUnits = minor
	return usage, nil
}

// AccountRevenue 读一个自营账号今日的使用计费收入（§3.2）。
//
// ⚠️ **只回答今天**，理由同 TokenUsage：上游的 `/accounts/{id}/stats` 只认
// `days`（从今天往回数几天），取的是 `summary.today.user_cost`，
// 没有「取某个具体日期」的形态。
//
// §10.5 铁律：收入 = 使用计费。`user_cost` 是使用量计费，
// **不含用户充值 / 余额 / 赠送额度**——这条本就满足。
func (c *sub2apiClient) AccountRevenue(
	ctx context.Context, ownAccountID string, day string,
) (AccountRevenue, error) {
	const op = "metering.account.revenue_read"

	if err := ValidateBusinessDay(day); err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	id := strings.TrimSpace(ownAccountID)
	if id == "" {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("own_account_id 为空"))
	}
	if !c.isToday(day) {
		return AccountRevenue{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("sub2api 账号统计只回答今天（summary.today）；请求的业务日是 %s", day))
	}

	var payload struct {
		Data struct {
			Summary struct {
				// Today 为 null = 今日零流量 = **已知 0**，不是未知（§3.2 原文）。
				Today *struct {
					UserCost money.RawAmount `json:"user_cost"`
				} `json:"today"`
			} `json:"summary"`
		} `json:"data"`
	}
	route := fmt.Sprintf(sub2apiRouteAccountStats, url.PathEscape(id))
	// days=1 只要今天：查一个很旧的窗口等于让上游把中间所有天都算一遍，
	// 而我们只用 summary.today。
	query := url.Values{"days": {"1"}}
	meta, err := c.get(ctx, op, route, query, c.authorizeAdmin(ctx, op), &payload)
	if err != nil {
		return AccountRevenue{}, err
	}

	observedAt := meta.observedAt(time.Time{})
	revenue := AccountRevenue{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  watermark(day, observedAt),
		},
		OwnAccountID: id,
		Day:          day,
		Currency:     c.currency,
	}
	if payload.Data.Summary.Today == nil || payload.Data.Summary.Today.UserCost.Empty() {
		return revenue, nil
	}
	minor, err := payload.Data.Summary.Today.UserCost.MinorUnits(UsageScale)
	if err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	revenue.RevenueMinorUnits = minor
	return revenue, nil
}

// authorizeAdmin 返回一个把 admin key 写进请求头的回调。
//
// 凭据在回调**外**解析（用调用方的 ctx，取消与超时才管得住它），
// 明文在回调**内**只出现一瞬。
func (c *sub2apiClient) authorizeAdmin(ctx context.Context, op string) func(*http.Request) error {
	return func(req *http.Request) error {
		value, err := c.resolve(ctx, op, c.adminRef, sub2apiRevenuePurpose)
		if err != nil {
			return err
		}
		req.Header.Set(sub2apiAdminAuthHeader, value.Reveal())
		return nil
	}
}
