package metering

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// NewAPI 上游的路由与响应形状——**唯一定义处**。
//
// 形状依据：设计稿 §3.1 对 SoloAI `relaymon/newapi.go:221`
// （FetchNewAPITokenCost）与 `:21`（NewAPIQuotaPerUSD=500000）的逐行引用。
//
// ⚠️ **未对真实实例验证过**：真实只读账号到位前，本文件的正确性只由本地
// 假上游的契约测试保证。账号到位后按 RUNBOOK 的验证清单逐项核对。

const (
	// newapiRouteLogStat 是**成本侧**唯一来源（§3.1）。
	//
	// ⚠️ `type=2` 是**关键**：它表示「仅 consume（消费）」，不含充值。
	// 抄成别的值不会报错，只会把充值混进成本（§4 标 ★）。
	newapiRouteLogStat = "/api/log/self/stat"

	// newapiRouteStatus 同时给版本与 quota_per_unit。
	//
	// quota_per_unit 从这里读而不是写死 500000：它在 New-API 里是**运行期
	// 可变**的站点配置。写死的后果是上游一改刻度，平台的成本就整体偏一个
	// 倍数，而且不会报错（§4 对 500000 的标注：抄错不报错，金额差 50 万倍）。
	// 读不到时才回落到 ★ 口径常量 newapiDefaultQuotaPerUnit。
	newapiRouteStatus = "/api/status"

	// newapiConsumeLogType 是 `type` 查询参数的取值：2 = 仅消费。
	//
	// ★口径常量（§4）。它是**成本口径的定义**，不是一个可调参数：
	// type=0（全部）会把充值记录算进来，让成本凭空多出一大截。
	newapiConsumeLogType = "2"

	// newapiDefaultQuotaPerUnit 是 quota → 1 单位货币的默认刻度。
	//
	// ★口径常量 500000（§4，对齐 SoloAI newapi.go:21 的 NewAPIQuotaPerUSD）。
	// 它只在上游 /api/status 没给 quota_per_unit 时兜底——上游给了就用上游的，
	// 因为那才是它此刻真正在用的刻度。
	newapiDefaultQuotaPerUnit int64 = 500_000

	// newapiUserHeader 与 newapiSessionCookie 是 self 系列端点的鉴权方式。
	//
	// `/api/log/self/stat` 是**用户自助**端点（路径里的 self 就是这个意思），
	// 它认的是登录会话而不是 admin key：New-Api-User 指明是哪个用户，
	// Cookie 里的 session 是那次登录的凭证。
	newapiUserHeader    = "New-Api-User"
	newapiSessionCookie = "session"

	// newapiCredentialSeparator 分隔凭据里的两段。
	//
	// SecretValue 是**单个字符串**，而这个上游要两样东西（用户 id + 会话）。
	// 约定用 `<user_id>:<session>` 编码，与 DSN 的做法同源。用冒号是因为
	// New-API 的用户 id 是纯数字、会话是 base64 串，两者都不含冒号，
	// 所以第一个冒号切分不会有歧义。
	//
	// 编码方式属于**凭据的内部格式**，不进契约、不进日志：这里只做切分，
	// 明文两段都只走到拼头那一瞬（宪法 7 条）。
	newapiCredentialSeparator = ":"

	// newapiCostPurpose 进凭据审计（规格 §4.5），不影响解析结果。
	newapiCostPurpose = "metering newapi token cost readonly"
)

// NewAPISupportedVersions 是兼容矩阵（ADR-004）。
//
// New-API 的版本串形如 "v0.8.x"；只写到 major.minor，理由同 sub2api 侧。
var NewAPISupportedVersions = []string{"0.8"}

type newapiClient struct {
	*httpBase

	// sessionRef 是账号级会话凭据的引用（`<user_id>:<session>` 那一份）。
	sessionRef string

	// revenue 是收入侧的只读数据库通道（XM-0044）。
	//
	// nil = 没配 XM_NEWAPI_REVENUE_DSN = AccountRevenue 继续返回 not_supported。
	// 它是**注入**进来的而不是本客户端自己开的：连接池要按进程持有
	// （对别人的生产库开 N 个池就是占 N 倍连接），而本客户端每轮采集重建。
	revenue RevenueSource

	// mu 保护 quota_per_unit 缓存。
	//
	// 客户端的生命周期是**一轮采集**，缓存的意义是一轮里的几十次取数不必
	// 各问一次 /api/status。这也让「一轮之内刻度不变」成为结构性事实——
	// 中途变了的话，SumRawUnits 会因为两种 UnitsPerWhole 而报错，
	// 而不是把两种刻度的计数悄悄加起来（见那里的注释）。
	mu           sync.Mutex
	quotaPerUnit int64
	quotaCached  bool
}

// NewNewAPIClient 构造 NewAPI 的计量取数只读客户端。
//
// cfg.CredentialRef 指向的秘密内容形如 `<user_id>:<session>`，
// 见 newapiCredentialSeparator 的说明。
func NewNewAPIClient(
	cfg connector.Config, sp secrets.SecretProvider, opts ...Option,
) (ReadClient, error) {
	base, err := newHTTPBase(cfg, sp, "metering.newapi.config", NewAPISupportedVersions, opts)
	if err != nil {
		return nil, err
	}
	if _, err := money.CurrencyScale(base.currency); err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.newapi.config", err)
	}
	if _, err := secrets.ParseCredentialRef(cfg.CredentialRef); err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.newapi.config", err)
	}
	return &newapiClient{
		httpBase:   base,
		sessionRef: cfg.CredentialRef,
		revenue:    revenueSourceFrom(opts),
	}, nil
}

// newapiStatus 是 /api/status 的响应形状。
type newapiStatus struct {
	Data struct {
		Version string `json:"version"`
		// QuotaPerUnit 用 RawAmount 而不是 int64：它是金额刻度，
		// 上游若用 float64 序列化会写成 500000 或 5e+05，两种都要能收，
		// 而且**不能经 float64**（宪法 13 条）。
		QuotaPerUnit money.RawAmount `json:"quota_per_unit"`
	} `json:"data"`
}

func (c *newapiClient) fetchStatus(ctx context.Context, op string) (newapiStatus, respMeta, error) {
	var payload newapiStatus
	meta, err := c.get(ctx, op, newapiRouteStatus, nil, c.authorize(ctx, op), &payload)
	return payload, meta, err
}

// quotaScale 取 quota → 1 单位货币的刻度。
//
// 优先用上游 /api/status 报的 quota_per_unit（运行期可变），读不到才回落到
// ★ 口径常量 500000。回落是有条件的：**只在上游没给这个字段时**回落；
// 上游给了一个非正数则硬报错——那说明上游状态异常，按 500000 算出来的
// 成本会是一个看起来正常的错数字（宪法 12 条）。
func (c *newapiClient) quotaScale(ctx context.Context, op string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quotaCached {
		return c.quotaPerUnit, nil
	}

	status, _, err := c.fetchStatus(ctx, op)
	if err != nil {
		// 读不到状态就读不到刻度。**不回落到 500000**：那等于在上游不可达时
		// 编一个刻度出来，把「读不到」变成「读到了一个可能错的数」。
		return 0, err
	}
	if status.Data.QuotaPerUnit.Empty() {
		// 上游没有这个字段（旧版本）：回落到与 SoloAI 一致的 ★ 口径常量。
		c.quotaPerUnit, c.quotaCached = newapiDefaultQuotaPerUnit, true
		return c.quotaPerUnit, nil
	}
	value, err := status.Data.QuotaPerUnit.Int64()
	if err != nil {
		return 0, connector.NewError(connector.KindBadResponse, op, err)
	}
	if value <= 0 {
		return 0, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("上游报的 quota_per_unit 是 %d（须为正）：按默认 %d 折算会得到一个"+
				"看起来正常的错数字，故拒绝取数", value, newapiDefaultQuotaPerUnit))
	}
	c.quotaPerUnit, c.quotaCached = value, true
	return c.quotaPerUnit, nil
}

func (c *newapiClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	const op = "metering.service.version_read"
	status, meta, err := c.fetchStatus(ctx, op)
	if err != nil {
		return connector.VersionInfo{}, err
	}
	detected := strings.TrimSpace(status.Data.Version)
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

// Health 用 /api/status 当健康探针。
//
// New-API 没有独立的 /health：状态端点回得出来就说明服务在，
// 这比不做探测好，也比编一个不存在的路由诚实。
func (c *newapiClient) Health(ctx context.Context) (connector.HealthResult, error) {
	const op = "metering.health.read"
	start := c.clock()
	_, meta, err := c.fetchStatus(ctx, op)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// 本进程在关机或调用方超时，不是上游的健康状况
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
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: meta.receivedAt,
		LatencyMS: meta.latency.Milliseconds(),
	}, nil
}

// Capabilities 返回本连接**当前实际可用**的能力。
//
// 收入能力**不在清单里**：newapi 的使用计费收入在自营 new-api 库的
// quota_data 表里（§3.2），走的是数据库通道而不是 HTTP。声明一项本客户端
// 兑现不了的能力等于说谎（同 connectors/sub2api 对能力子集的注释）。
func (c *newapiClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	const op = "metering.capabilities"
	out := make([]registry.Capability, 0, 3)
	if _, _, err := c.fetchStatus(ctx, op); err != nil {
		if connector.KindOf(err) == connector.KindNotSupported {
			// 状态路由都没有：版本与健康都给不出来，成本能力也无从谈起
			// （它要的刻度就在这条路由上）。
			return out, nil
		}
		// 认证失败、限流、网络不通：这不是「能力不存在」，是「现在问不出来」——
		// 返回一份猜出来的清单会让调用方做出错误的降级决策。
		return nil, err
	}
	out = append(out,
		"metering.health.read",
		"metering.service.version_read",
		"metering.token.usage_read",
	)
	return out, nil
}

// TokenUsage 读一个上游令牌今日的实扣（§3.1）。
//
// ⚠️ **只回答今天**。上游端点本身接受任意时间戳区间，但「过去某一天」的
// 右边界该取当日 23:59:59 还是次日 00:00:00（端点是闭区间还是半开区间）
// **没有对真实实例验证过**，而设计稿 §4/§5.3 的口径本就是「今日覆盖、
// 过去日冻结」——平台从不回填历史。与其发一个边界语义未知的请求、
// 拿回一个可能漏掉或多算一天最后一秒的数，不如明说答不出来。
// 账号到位后核对了边界语义，再放开这条限制（RUNBOOK 验证清单）。
func (c *newapiClient) TokenUsage(
	ctx context.Context, token TokenRef, day string,
) (TokenUsage, error) {
	const op = "metering.token.usage_read"

	if err := ValidateBusinessDay(day); err != nil {
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	tokenName := strings.TrimSpace(token.UpstreamTokenID)
	if tokenName == "" {
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream_token_id 为空：newapi 成本侧用它作 token_name 查询参数"))
	}
	if !c.isToday(day) {
		return TokenUsage{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("newapi 成本取数目前只回答今天（历史区间的右边界语义未经真实实例核对，"+
				"且 §4/§5.3 口径为今日覆盖、过去日冻结）；请求的业务日是 %s", day))
	}

	perUnit, err := c.quotaScale(ctx, op)
	if err != nil {
		return TokenUsage{}, err
	}

	start, _, err := c.businessDayBounds(day)
	if err != nil {
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	// 右边界取 now，与 SoloAI 的 `end_timestamp=<now>` 逐字一致（§3.1）。
	// 今日的区间本来就该开到此刻——写成当日 23:59:59 会把今天剩下的
	// 消费一起算进来，而那些消费还没发生。
	now := c.clock()

	query := url.Values{
		"type":            {newapiConsumeLogType},
		"start_timestamp": {strconv.FormatInt(start.Unix(), 10)},
		"end_timestamp":   {strconv.FormatInt(now.Unix(), 10)},
		"token_name":      {tokenName},
	}

	var payload struct {
		Data struct {
			// Quota 是整数 credits。用 RawAmount 收：上游若用 float64
			// 序列化会写成 1.5e+07，直接解进 int64 会失败（宪法 13 条：
			// 换算路径上不出现 float）。
			Quota money.RawAmount `json:"quota"`
		} `json:"data"`
	}
	meta, err := c.get(ctx, op, newapiRouteLogStat, query, c.authorize(ctx, op), &payload)
	if err != nil {
		return TokenUsage{}, err
	}

	observedAt := meta.observedAt(time.Time{})
	usage := TokenUsage{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  watermark(day, observedAt),
		},
		UpstreamTokenID: tokenName,
		Day:             day,
		Currency:        c.currency,
	}

	// 字段缺失 / null = 这个令牌今天没有消费记录 = **已知 0**（§5.1）。
	// 未知靠返回 error 表达，让台账落 NULL 而不是 0。
	quota := int64(0)
	if !payload.Data.Quota.Empty() {
		quota, err = payload.Data.Quota.Int64()
		if err != nil {
			return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
		}
	}

	minor, err := money.DivideByUnits(quota, perUnit, UsageScale)
	if err != nil {
		return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	usage.UsageMinorUnits = minor
	// 把整数 quota 与刻度一起带出去，调用方才做得了「先 SUM 再除」的
	// 账号级合计（§3.1）——逐条折算后相加会多带每条半个微单位的舍入。
	usage.RawUnits = &quota
	usage.UnitsPerWhole = &perUnit
	return usage, nil
}

// AccountRevenue 在 NewAPI 侧**不走 HTTP**，而是委托给只读数据库通道。
//
// §3.2 的口径是只读直连自营 new-api 库：
//
//	SELECT COALESCE(SUM(quota),0) FROM quota_data
//	WHERE created_at >= <业务日 00:00 unix> AND channel_id::text = <own_account_id>
//
// 那是一条数据库通道（ADR-018 的闸 2/3 原文正是针对数据库通道的），
// 与本包的 HTTP 底座是两套东西：不同的凭据形态（DSN vs 会话）、
// 不同的只读保证（服务端 default_transaction_read_only vs 方法白名单）。
//
// 所以它**不是被塞进本客户端**的，而是一个独立实现（revenuedb.go 的
// NewAPIRevenueDB，自带四道闸与 SELECT 白名单），由 WithRevenueSource 挂进来，
// 本方法只做一次转发。两条通道的护栏各自完整，互不稀释——这正是原先那句
// 「硬塞进本客户端只会让两种通道的护栏互相稀释」要守住的东西。
//
// **没挂通道时保持原状：返回 not_supported。** 一个理直气壮的 0 会让 037b
// 把它当成「今天没有收入」写进台账，于是毛利凭空等于成本的负数
// （§5.1 明令禁止）。XM_NEWAPI_REVENUE_DSN 没配的部署就停在这一支，
// 行为与 XM-0044 之前逐字相同。
func (c *newapiClient) AccountRevenue(
	ctx context.Context, ownAccountID string, day string,
) (AccountRevenue, error) {
	const op = "metering.account.revenue_read"
	if c.revenue == nil {
		return AccountRevenue{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("newapi 的使用计费收入在自营 new-api 库的 quota_data 表里（§3.2），"+
				"走只读数据库通道而不是 HTTP；本部署没有配置 XM_NEWAPI_REVENUE_DSN"))
	}
	return c.revenue.AccountRevenue(ctx, ownAccountID, day)
}

// authorize 返回一个把会话凭据写进请求头的回调。
//
// 凭据在回调**外**解析（用调用方的 ctx，取消与超时才管得住它），
// 明文在回调**内**只出现一瞬：切分出的两段直接进头，不落任何变量之外的地方，
// 也不参与任何错误信息的拼接（宪法 7 条）。
func (c *newapiClient) authorize(ctx context.Context, op string) func(*http.Request) error {
	return func(req *http.Request) error {
		value, err := c.resolve(ctx, op, c.sessionRef, newapiCostPurpose)
		if err != nil {
			return err
		}
		userID, session, ok := strings.Cut(value.Reveal(), newapiCredentialSeparator)
		if !ok || strings.TrimSpace(userID) == "" || strings.TrimSpace(session) == "" {
			// 错误里**不含凭据的任何片段**，只说形状不对——
			// 一个把明文回显出来的报错比读不到糟得多（宪法 7 条）。
			return connector.NewError(connector.KindAuth, op,
				fmt.Errorf("newapi 凭据须形如 <user_id>%s<session> 两段，实际不是",
					newapiCredentialSeparator))
		}
		req.Header.Set(newapiUserHeader, strings.TrimSpace(userID))
		req.AddCookie(&http.Cookie{
			Name:  newapiSessionCookie,
			Value: strings.TrimSpace(session),
		})
		return nil
	}
}

// UpstreamBalance 在 NewAPI 侧**尚未接通**，返回 not_supported（§7）。
//
// 这一条的障碍比 sub2api 那条更硬，而且是上游侧的：§7 明确记着
// 「NewAPI 读 `channel.balance`（**需上游先开 CHANNEL_UPDATE_FREQUENCY，
// 否则死水**；覆盖率低）」——也就是说即便平台把读取实现了，
// 多数部署拿回来的仍然是一个从未刷新过的旧数字。
//
// 一个死水余额比没有余额更危险：它会算出一个**看起来精确、实际上停在
// 上个月**的可用天数，而看板上没有任何东西提示它是死的。
//
// 所以这里停在 not_supported。接通它的前置不在本仓：
//  1. 上游开启 CHANNEL_UPDATE_FREQUENCY；
//  2. 确认 channel.balance 的刷新时间戳也能读到——没有那个时间戳，
//     §10.4 的「必须显示观测时间」就无从满足，而**平台不该拿本地读取时刻
//     冒充上游的余额时间**（同 respMeta.observedAt 那条退化路径的纪律）。
func (c *newapiClient) UpstreamBalance(ctx context.Context) (UpstreamBalance, error) {
	const op = "metering.upstream.balance_read"
	if err := ctx.Err(); err != nil {
		return UpstreamBalance{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	return UpstreamBalance{}, connector.NewError(connector.KindNotSupported, op,
		errors.New("newapi 余额读取尚未接通：channel.balance 需上游先开 "+
			"CHANNEL_UPDATE_FREQUENCY 否则是死水（§7），且需要一并读到它的刷新时刻——"+
			"没有那个时刻就满足不了「必须显示观测时间」，而拿本地读取时刻冒充会让"+
			"一个停更的余额看起来很新鲜"))
}
