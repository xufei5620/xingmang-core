package newapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 上游（NewAPI）的路由与响应形状——**唯一定义处**。
//
// 形状依据：`K:/newapi-src`（QuantumNous/new-api，Go + Gin + GORM）的路由注册
// 与 model 层 json tag，只读参考、未做任何改动；口径以
// docs/evidence/EV-2026-08-27-newapi-read-survey.md 与
// contracts/connectors/newapi.read.v1.md 为准。
// 上游没有 OpenAPI/Swagger 规格，所以这里的每个字段名都是从源码里逐个抄的。
//
// ⚠️ **未对真实实例验证过**：真实只读凭据到位前，本文件的正确性只由本地假上游
// 的契约测试保证。凭据到位后按 docs/runbooks/SWITCH-NEWAPI-REAL.md 的验证清单
// 逐项核对——尤其是 quota 换算与业务日边界，那两样错了之后数字看起来完全正常。
//
// 上游改版时，要改的应该只有这一个文件。
//
// ---------------------------------------------------------------------------
// 接入真实实例前置清单（依据普查报告）
// ---------------------------------------------------------------------------
//
//  1. **凭据必须是管理员（role ≥ 10）的 access token。** 本包读的
//     /api/channel/、/api/user/、/api/log/、/api/data/ 全挂 AdminAuth()；
//     渠道路由还额外叠了 casbin 的 ChannelRead 权限。上游**没有只读角色**，
//     所以这把 token 在上游是全权限的——平台侧靠四道只读闸 + 写端点黑名单
//     机械保证碰不到写路径，但凭据本身的权限平台管不了。
//
//  2. **尾斜杠不能省。** /api/channel/、/api/user/、/api/log/、/api/data/
//     在 gin 里注册的是 "/"，少一个斜杠会得到 301 重定向；而本客户端
//     **拒绝一切重定向**（一个 302 就能把请求引到 allowlist 之外），
//     于是症状会是 forbidden_target，而不是「路径写错了」。
//     routeMustEndWithSlash 里钉死了这几条，改动时当场变红。
//
//  3. **业务失败是 HTTP 200 + success:false。** 上游的 common.ApiError 走的是
//     200，只有鉴权失败才是真 401/403。所以状态码检查之后**必须**再看
//     信封里的 success 字段，见 upstreamEnvelope.decode。
//
//  4. **这些 GET 端点会写库，一条都不能碰。** 见 writeDisguisedAsGetRoutes。

const (
	// routeStatus 是**唯一**的版本来源，也是 quota 换算基数的来源。
	//
	// 它不需要鉴权（router/api-router.go 里只挂了限流），所以健康探测走它
	// 不会因为凭据问题而误报「上游挂了」——凭据问题会在数据读取上暴露成 auth。
	routeStatus = "/api/status"

	// routeChannels 是渠道状态、余额与模型数的来源。**尾斜杠必须。**
	routeChannels = "/api/channel/"

	// routeUsers 是用户数与余额的来源。**尾斜杠必须。**
	//
	// ⚠️ 上游这条查询是 Unscoped 的（model/user.go 的 count 与 list 都带
	// Unscoped()），**列表里含软删除用户**，data.total 也把它们算在内。
	// 本包因此不用 data.total 当用户数，而是逐行看 DeletedAt 自己数，
	// 见 fetchUserStats。
	routeUsers = "/api/user/"

	// routeTopups 是充值订单的来源（管理员视角，不限时间窗口，id desc）。
	//
	// ⚠️ 它**没有**时间过滤参数，只能翻页自己筛，见 fetchRechargeDay。
	routeTopups = "/api/user/topup"

	// routeLogs 是错误率的来源。**尾斜杠必须。**
	//
	// 只用它的 data.total（COUNT），不拉日志正文：日志里带 IP、用户名、
	// token 名这些个人数据，采集链路一个字都不该碰。
	routeLogs = "/api/log/"

	// routeUsageData 是逐模型用量的来源。**尾斜杠必须。**
	//
	// ⚠️ 数据来自 quota_data 表，由上游后台每 DataExportInterval（默认 5 分钟）
	// 落盘一次，所以**最近 5 分钟的用量可能还没入库**。这是上游的固有延迟，
	// 不是采集链路的问题，但它会体现在这条指标的新鲜度上。
	routeUsageData = "/api/data/"

	// authHeader / authScheme 是管理员 access token 的携带方式。
	//
	// 上游的 authorizationToken() 同时接受 "Bearer <token>" 与裸 token，
	// 这里固定用 Bearer：裸 token 形态在任何一层代理的日志里都更容易被
	// 误当成别的东西，而 Bearer 是有标准语义的。
	authHeader = "Authorization"
	authScheme = "Bearer "

	// legacyUserHeader 是**旧版本** NewAPI 的第二个鉴权头，值是管理员的用户 id。
	//
	// 普查依据的源码（K:/newapi-src 当前 HEAD）里它**已经不参与鉴权**——
	// docs/authentication.md 明写「面板请求不再依赖 Gin session，也不再要求
	// New-Api-User 请求头」，全仓没有任何中间件读它。
	//
	// 那为什么还发：真实实例跑的补丁版未知（见 SupportedUpstreamVersions），
	// 而旧版本上**没有这个头就是 401**。多发一个会被新版本忽略的头，代价是零；
	// 少发它则会在一个还没升级的实例上表现为「凭据无效」，而那时人会去查
	// token 而不是查版本。所以它是可选配置：配了就发，没配就不发。
	//
	// 它**不是**凭据：用户 id 不是秘密，不走 CredentialRef（那条通道是给
	// 明文秘密用的），走普通配置项。
	legacyUserHeader = "New-Api-User"

	// defaultCurrency 是本契约的记账币种。
	//
	// NewAPI 内部的钱只有一种单位：quota（整数）。它换算成钱的唯一口径是
	// `美元 = quota / quota_per_unit`（上游 controller/billing.go）。
	// 所以这里是 USD，不是 CNY——写成 CNY 会得到一批**看起来完全正常**的
	// 错数字（差一个汇率）。可用 WithCurrency 覆盖，但覆盖的只是标注，
	// 不会替你做汇率换算。
	defaultCurrency = "USD"

	// newapiBusinessDayLayout 是业务日格式（规格 §5.9）。
	newapiBusinessDayLayout = "2006-01-02"

	// upstreamPageSize 是所有分页读取的页大小。
	//
	// 100 是上游的**硬上限**（common/page_info.go 把 PageSize > 100 一律钳到
	// 100），填更大的数不会报错，只会静静地按 100 返回——那会让「翻完了吗」
	// 的判断出错。所以这里就写 100，与上游的钳位对齐。
	upstreamPageSize = 100

	// 各列表的翻页上限。到顶还没拉完就**标记为部分数据**：
	// 少报一部分是可以被看见的，把一轮同步拖死不是。
	//
	// 充值列表的上限明显更小（20 页 = 2000 单），因为它最贵：上游的
	// GetAllTopUps **每翻一页都重跑一次无界 COUNT(*)**，翻 50 页就是 50 次
	// 全表计数，而这条链路每 5 分钟跑一次。正常情况下根本翻不了几页——
	// 扫到「比业务日起点再往前 topupScanMarginDays 天」就停手了（列表按
	// id desc 排），2000 单是给异常繁忙的日子留的天花板，不是常态预算。
	maxUserPages    = 50
	maxChannelPages = 10
	maxTopupPages   = 20

	// defaultActiveUserWindow 是「活跃用户」的判据窗口。
	//
	// ⚠️ **NewAPI 上游没有「活跃用户」这个概念**（普查报告的契约核对红点之一）。
	// 这个口径是平台自定的：last_login_at 落在最近 30 天内。判据必须跟着
	// 数字一起被看见，所以它写进 UserStats 的 Watermark（见 fetchUserStats），
	// 而不是只活在这行注释里。
	defaultActiveUserWindow = 30 * 24 * time.Hour

	// maxErrorRateChannels 是每轮同步最多为几个渠道算错误率。
	//
	// 错误率在上游没有现成端点，只能靠 /api/log/ 的 COUNT 自己算，而且
	// **一个渠道要两次请求**（type=5 的错误数、type=2 的消费数）。渠道多的
	// 实例上这会把一轮同步的请求数放大成两位数倍，20 秒的读取预算撑不住。
	//
	// 超出上限的渠道：错误率留 0，但它们的 Snapshot.IsPartial 置真——
	// 「没测」与「测了是 0」在看板上必须分得开，否则一个没测的渠道会以
	// 「健康」的姿态出现（契约 ChannelStatus.ErrorRatePPM 是非空 int64，
	// 表达不了「未知」，见该字段的 follow-up）。
	maxErrorRateChannels = 40

	// topupScanMarginDays 是充值订单翻页的回溯余量。
	//
	// 上游的充值列表按 id desc 排（model/topup.go 的 GetAllTopUps），而
	// **业务日要看的是 complete_time**——一笔昨天下单、今天才回调成功的订单，
	// id 比今天下的单小，却属于今天的收入。只扫到「create_time < 当日零点」
	// 就停手会漏掉这一类。多扫几天是廉价的保险。
	topupScanMarginDays = 3

	// 上游的渠道状态取值（common/constants.go 的 ChannelStatus* 常量）。
	// 1 = 启用；2 = 手动禁用；3 = 自动禁用；0 = 未知。
	channelStatusEnabled = 1

	// 上游的日志类型（model/log.go 的 LogType* 常量）。
	// 错误率 = type 5 的条数 / (type 2 + type 5) 的条数，判据见普查报告。
	logTypeConsume = 2
	logTypeError   = 5

	// 上游的充值订单状态（common/constants.go 的 TopUpStatus* 常量）。
	topupStatusSuccess = "success"
)

// routeMustEndWithSlash 是**必须**带尾斜杠的路由。
//
// 这几条在 gin 里注册的是 "/"，少一个斜杠会得到 301；而本客户端拒绝重定向，
// 于是症状会是 forbidden_target——一个看起来像「allowlist 配错了」的故障，
// 实际原因却是路径少了一个字符。把它钉成常量清单 + 测试，比在评审里靠眼睛
// 盯住可靠（见 TestRoutesKeepTrailingSlash）。
var routeMustEndWithSlash = []string{routeChannels, routeUsers, routeLogs, routeUsageData}

// ---------------------------------------------------------------------------
// NewAPI 特有的第五道闸：伪装成 GET 的写端点黑名单
// ---------------------------------------------------------------------------

// writeDisguisedAsGetRoutes 是**用 GET 方法但会写数据库**的上游端点。
//
// 通用的四道只读闸（ADR-018）在这里**拦不住**：它们的判据是 HTTP 方法与目标
// 主机，而这些端点方法就是 GET、主机就是同一个上游。闸门放行，库照写不误。
// 所以本包加第五道机械闸：路由在发出去之前先过这份黑名单，命中就归
// write_attempt——与真的发了个 POST 同一个分类，因为后果是同一件事。
//
// 每一条都在源码里追到了实际的写库调用点（普查报告第 5 节）：
//
//	/api/channel/test[/:id]            INSERT system_task；真发上游请求（消耗额度）+
//	                                   UPDATE response_time/test_time + INSERT 消费日志
//	/api/channel/update_balance[/:id]  UPDATE balance/balance_updated_time；
//	                                   余额 ≤0 还会**自动禁用渠道**
//	/api/channel/fetch_models/:id      Codex 渠道 401 时把刷新后的 OAuth token
//	                                   写回 channel.key；Gemini 多 key 持久化 channel_info
//	/api/channel/:id/codex/usage[...]  同上，上游 401/403 时回写 token
//	/api/user/token                    **静默轮换调用者自己的 access token**——
//	                                   调一次就把采集凭据自己打掉了，最危险的一条
//	/api/user/aff                      首次调用会生成并持久化邀请码（lazy write on GET）
//	/api/user/epay/notify              标记充值订单成功 + 给用户加额度
//	/api/subscription/epay/{notify,return}  创建用户订阅 + 结单
//	/api/oauth/**                      可能 INSERT 用户 + 必定 INSERT 登录会话
//	/v1/realtime                       WebSocket 升级，扣额度 + 写消费日志
//	/v1/video*、/kling/v1/videos/**    轮询时 UPDATE task 状态
//
// 清单里的路径是**前缀**：写 /api/channel/test 就同时挡住 /api/channel/test/:id。
// `*` 匹配任意单个路径段，用来表达 /api/channel/:id/codex/usage 这种把参数
// 夹在中间的形状。
//
// 这份清单**不是**给人看的文档，是会被执行的判据：assertReadOnlyRoute 在每次
// 请求前跑它，TestWriteDisguisedAsGetRoutesAreBlocked 钉死每一条都真的被挡。
var writeDisguisedAsGetRoutes = []string{
	"/api/channel/test",
	"/api/channel/update_balance",
	"/api/channel/fetch_models",
	"/api/channel/*/codex/usage",
	"/api/user/token",
	"/api/user/aff",
	"/api/user/epay/notify",
	"/api/subscription/epay/notify",
	"/api/subscription/epay/return",
	"/api/oauth",
	"/v1/realtime",
	"/v1/video/generations",
	"/v1/videos",
	"/kling/v1/videos",
}

// assertReadOnlyRoute 在请求发出前拦下伪装成 GET 的写端点。
//
// 归类 write_attempt 而不是 forbidden_target：目标主机没问题，问题是这个
// **操作**会写库。运维看到 write_attempt 才会去找「谁往只读通道里加了写路径」，
// 看到 forbidden_target 只会去查 allowlist。
func assertReadOnlyRoute(routePath string) error {
	clean := "/" + strings.Trim(strings.TrimSpace(routePath), "/")
	for _, pattern := range writeDisguisedAsGetRoutes {
		if routeMatchesPrefix(clean, pattern) {
			return fmt.Errorf("%s 是 GET 但会写上游数据库，只读通道禁止访问", pattern)
		}
	}
	return nil
}

// routeMatchesPrefix 判断 path 是否落在 pattern 这棵子树下。
//
// 按**路径段**比较而不是字符串前缀：字符串前缀会让 /api/user/tokens-report
// 被 /api/user/token 误伤，也会让 /api/channel/testing 被 /api/channel/test
// 误伤。段比较之后 pattern 恰好是 path 的前若干段才算命中。
// pattern 里的 `*` 匹配任意单个段。
func routeMatchesPrefix(path, pattern string) bool {
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(patternParts) > len(pathParts) {
		return false
	}
	for i, want := range patternParts {
		if want == "*" {
			continue
		}
		if !strings.EqualFold(want, pathParts[i]) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 币种与鉴权
// ---------------------------------------------------------------------------

// currencyScale 返回币种的最小单位小数位。
//
// 不认识的币种**报错而不是猜 2 位**：猜错的那 100 倍不会有任何症状，
// 只会让所有金额静静地错着（宪法 13 条）。
func currencyScale(code string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "USD", "CNY", "EUR", "GBP", "HKD", "AUD", "CAD", "SGD":
		return 2, nil
	case "JPY", "KRW", "VND":
		return 0, nil
	default:
		return 0, fmt.Errorf("未登记的币种 %q：最小单位小数位必须显式登记，不能猜", code)
	}
}

// applyAuth 把凭据明文写进请求头。
//
// 这是明文**唯一**的去处，调用点见 client.authorize——那里 Reveal，
// 这里写头，之后明文再没有第三个落点（宪法 7 条）。
//
// userID 不是凭据（见 legacyUserHeader 的注释），为空时不发这个头。
func applyAuth(req *http.Request, v secrets.SecretValue, userID string) {
	req.Header.Set(authHeader, authScheme+v.Reveal())
	if userID != "" {
		req.Header.Set(legacyUserHeader, userID)
	}
}

// ---------------------------------------------------------------------------
// 响应外壳
// ---------------------------------------------------------------------------

// upstreamEnvelope 是 NewAPI 所有 JSON 响应的统一外壳。
//
// ⚠️ **业务失败是 HTTP 200 + success:false**（上游 common.ApiError 走 200），
// 只有鉴权失败才是真 401/403。所以状态码检查之后必须再看这个 success 字段，
// 否则一次「渠道查询失败」会被当成一次成功读取，然后把空数据写进看板。
type upstreamEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// decode 校验业务成功标志并把 data 解进 out。
//
// 上游的 message 一个字都不进错误链：它是上游写给人看的文本，
// 里面出现过用户名与订单号（ADR-004、宪法 7 条）。
func (e upstreamEnvelope) decode(op string, out any) error {
	if !e.Success {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream reported success=false"))
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应缺少 data 字段"))
	}
	if err := json.Unmarshal(e.Data, out); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	return nil
}

// upstreamPage 是 common.PageInfo 与渠道列表共用的分页形状。
//
// 上游有两种分页外壳：/api/user/、/api/log/、/api/user/topup 走 common.PageInfo，
// /api/channel/ 是 handler 自己拼的 gin.H（多一个 type_counts）。两者的
// items/total/page/page_size 四个键名恰好一致，所以一个结构体够用。
type upstreamPage[T any] struct {
	Items    []T       `json:"items"`
	Total    rawAmount `json:"total"`
	Page     rawAmount `json:"page"`
	PageSize rawAmount `json:"page_size"`
}

// pageQuery 拼分页参数。
//
// 页码参数叫 **p** 不是 page（common/page_info.go），这是最容易抄错的一处：
// 写成 page 不会报错，上游会静静地按第 1 页返回，于是翻页变成了原地打转，
// 而「总数对不上」要等到很久以后才会被发现。
func pageQuery(page, size int) url.Values {
	return url.Values{"p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}}
}

// minimalPageQuery 是能力探测用的最小代价查询。
func minimalPageQuery() url.Values { return pageQuery(1, 1) }

// parseUnixSeconds 把上游的秒级时间戳字段转成 UTC 时刻；0 或空返回零值。
//
// 零值是有意义的信号：调用方会退到 Date 头或本地接收时刻，
// 而不是拿当前时间冒充一次观测（规格 §9.1）。
func parseUnixSeconds(v rawAmount) time.Time {
	secs, err := v.count()
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// ---------------------------------------------------------------------------
// 能力探测
// ---------------------------------------------------------------------------

type routeProbe struct {
	route string
	query url.Values
}

func (p routeProbe) key() string { return p.route + "?" + p.query.Encode() }

type capabilityProbe struct {
	capability registry.Capability
	// probes 全部可达该能力才算可用。
	probes []routeProbe
}

// implementedCapabilities 是本客户端**实现了**的能力及其支撑路由。
//
// 契约的 ReadCapabilities 有 7 项，这里也是 7 项——本客户端把契约的每个读方法
// 都实现了。探测一律取最小页（page_size=1），代价可控。
//
// 探测用 GET 而不是 HEAD：上游是 Gin，GET 路由不会自动响应 HEAD，
// 一发 HEAD 全都是 404——那会让所有能力看起来都「不存在」。
func implementedCapabilities(now time.Time) []capabilityProbe {
	// 用量端点的时间范围不能留空：上游把缺失参数解析成 0，而查询是
	// `created_at >= 0 AND created_at <= 0`，那会返回空数组而不是报错——
	// 探测因此会「成功」，但成功得毫无意义。给一个真实的窗口。
	usage := url.Values{
		"start_timestamp": {strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)},
		"end_timestamp":   {strconv.FormatInt(now.Unix(), 10)},
	}
	// 日志探测必须**带上过滤条件**，不能只发 page_size=1。
	//
	// 上游的 GetAllLogs 无论取几条都会先跑一次 `COUNT(*)`，不带过滤就是对
	// 整张日志表做全表 COUNT——那是这条链路上唯一一个能把上游数据库拖慢的
	// 查询。探测的意义只是「这条路由在不在」，代价不该比它的价值大。
	// 顺带一个好处：type 非零，与 countLogs 保持同一条纪律（0 在上游表示
	// 不过滤，绝不能发）。
	probeLogs := pageQuery(1, 1)
	probeLogs.Set("type", strconv.Itoa(logTypeError))
	probeLogs.Set("start_timestamp", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10))
	probeLogs.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))

	return []capabilityProbe{
		{capability: "newapi.service.version_read", probes: []routeProbe{{route: routeStatus}}},
		{capability: "newapi.health.read", probes: []routeProbe{{route: routeStatus}}},
		{capability: "newapi.users.read", probes: []routeProbe{{route: routeUsers, query: minimalPageQuery()}}},
		{capability: "newapi.orders.read", probes: []routeProbe{{route: routeTopups, query: minimalPageQuery()}}},
		{capability: "newapi.channels.read", probes: []routeProbe{{route: routeChannels, query: minimalPageQuery()}}},
		{capability: "newapi.errors.read", probes: []routeProbe{{route: routeLogs, query: probeLogs}}},
		{capability: "newapi.models.usage_read", probes: []routeProbe{{route: routeUsageData, query: usage}}},
	}
}

// ---------------------------------------------------------------------------
// 服务状态：版本、健康、quota 换算基数
// ---------------------------------------------------------------------------

// statusPayload 是 /api/status 的 data 里本包用得到的部分。
//
// 那个 data 有 60 多个键（主题、OAuth 开关、公告……），这里**只解三个**：
// 多解一个字段就多一处会随上游改版而炸的地方，而剩下那些我们一个都不用。
type statusPayload struct {
	Version   string    `json:"version"`
	StartTime rawAmount `json:"start_time"`
	// QuotaPerUnit 是 quota → 美元的换算基数（上游 common.QuotaPerUnit，
	// 默认 500000）。**运行期可变**：root 能通过 PUT /api/option/ 随时改它
	// （model/option.go:587），所以它必须每轮现读，不能编译期写死。
	QuotaPerUnit rawAmount `json:"quota_per_unit"`
}

func (c *client) fetchStatus(ctx context.Context, op string) (statusPayload, respMeta, error) {
	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeStatus, nil, &env)
	if err != nil {
		return statusPayload{}, meta, err
	}
	var payload statusPayload
	if err := env.decode(op, &payload); err != nil {
		return statusPayload{}, meta, err
	}
	return payload, meta, nil
}

// quotaPerUnit 读取并缓存 quota → 美元的换算基数。
//
// 缓存的粒度是**一个客户端实例**，而客户端的生命周期是「一轮同步」
// （见 jobs 的工厂注释）：一轮里的四次读取共用一次 /api/status，
// 而下一轮会重新读——root 改了这个值，最多一个周期就跟上了。
//
// 换算基数在上游是 float64。这里用 rawAmount 接住字面量再按整数解析：
// 它在实践中永远是整数（500000），而万一不是，四舍五入到整数的误差
// （最坏 0.5/500000）远小于把它塞进 float64 再参与除法的误差链。
func (c *client) quotaPerUnit(ctx context.Context, op string) (int64, error) {
	c.mu.Lock()
	if c.quotaUnitOK {
		defer c.mu.Unlock()
		return c.quotaUnit, nil
	}
	c.mu.Unlock()

	// 锁必须在这一行之前放掉：fetchStatus 会一路走到 authorize → credential，
	// 而那里要拿的是**同一把** mu。抱着锁去发 HTTP 请求就是死锁。
	payload, _, err := c.fetchStatus(ctx, op)
	if err != nil {
		return 0, err
	}
	unit, err := payload.QuotaPerUnit.count()
	if err != nil {
		return 0, connector.NewError(connector.KindBadResponse, op, err)
	}
	if unit <= 0 {
		// 上游的 option 写入路径把 ParseFloat 的 error 丢掉了
		// （model/option.go:588 的 `_`），非法值会把 QuotaPerUnit 置 0。
		// 拿 0 做除数是崩溃，拿默认值顶上是**编数**——两个都不行，
		// 所以这里报错，让这一轮的金额指标写成失败观测。
		return 0, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream quota_per_unit=%d，无法换算金额", unit))
	}

	c.mu.Lock()
	c.quotaUnit, c.quotaUnitOK = unit, true
	c.mu.Unlock()
	return unit, nil
}

func (c *client) fetchVersion(ctx context.Context, op string) (string, respMeta, error) {
	payload, meta, err := c.fetchStatus(ctx, op)
	if err != nil {
		return "", meta, err
	}
	// 上游的版本串形态**不可假定**：默认是带 v 前缀的 "v0.0.0"，可被 VERSION
	// 文件、VERSION 环境变量、CI 的 git describe 或分支镜像的
	// "<prefix>-YYYYMMDD-<sha>" 覆盖。normalizeVersion 只做「去 v 前缀 +
	// 砍预发布后缀」，认不出来的形态自然落到 Supported=false。
	return strings.TrimSpace(payload.Version), meta, nil
}

// fetchHealth 判断上游是否在正常服务。
//
// 判据是 /api/status 回了 200 且 success=true。它是**存活探针**：这条路由
// 不需要鉴权，所以健康与凭据是否有效无关——凭据问题会在数据读取上暴露成
// auth 分类，不该让健康指标跟着变红（那会让人去查上游而不是查凭据）。
func (c *client) fetchHealth(ctx context.Context, op string) (bool, respMeta, error) {
	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeStatus, nil, &env)
	if err != nil {
		return false, meta, err
	}
	return env.Success, meta, nil
}

// ---------------------------------------------------------------------------
// 用户与余额
// ---------------------------------------------------------------------------

// userItem 是 /api/user/ 列表里本包用得到的字段。
//
// **刻意只解这四个。** 上游那个 User 结构体带着 username / email / github_id /
// telegram_id / remark 一堆个人数据（还有一个被 Omit 但仍序列化成空串的
// password 字段）。采集链路要的是「有多少人、一共多少额度」，多解一个字段
// 就多一处让个人数据流进平台日志的机会（宪法 7 条）。
type userItem struct {
	ID rawAmount `json:"id"`
	// Quota 是用户当前剩余额度（上游是 int，单位 quota）。
	Quota rawAmount `json:"quota"`
	// LastLoginAt 是最后登录时刻（Unix 秒）。
	//
	// ⚠️ json tag 是 last_login_at，**不是** last_login_time；
	// 同理 created_at 不是 created_time。抄错不会报错，只会永远解出 0，
	// 然后活跃用户数恒等于 0。
	LastLoginAt rawAmount `json:"last_login_at"`
	// DeletedAt 是软删除标记。
	//
	// ⚠️ 上游这个字段**没写 json tag**（model/user.go:103 的
	// `DeletedAt gorm.DeletedAt`），所以序列化出来的键是大驼峰的
	// "DeletedAt" 而不是 deleted_at。而列表查询是 Unscoped 的，
	// 软删除用户混在结果里——不看这个字段，用户数就会把删掉的人算进去。
	//
	// 用 json.RawMessage 而不是 *time.Time：gorm.DeletedAt 未删除时序列化成
	// null，已删除时是时间串，而我们只需要区分这两者，不需要解析出时刻。
	DeletedAt json.RawMessage `json:"DeletedAt"`
}

func (u userItem) softDeleted() bool {
	text := strings.TrimSpace(string(u.DeletedAt))
	return text != "" && text != "null"
}

// userTotals 是用户列表翻页的聚合结果。
type userTotals struct {
	// total 是**未软删除**的用户数。
	total int64
	// active 是最近 activeWindow 内登录过的未软删除用户数。
	active int64
	// quota 是未软删除用户的剩余额度之和，单位仍是 quota（尚未换算成钱）。
	//
	// 保持 quota 单位到最后一步才除 quota_per_unit：每个人各自除完再相加，
	// 误差会按人数累积（见 quotaToMinorUnits 的调用纪律）。
	quota int64
	// truncated 表示没拉完（到达翻页上限或上游 total 比拉到的多）。
	truncated bool
}

func (c *client) fetchUserStats(ctx context.Context) (UserStats, error) {
	const op = opUsers

	unit, err := c.quotaPerUnit(ctx, op)
	if err != nil {
		return UserStats{}, err
	}

	totals, meta, err := c.aggregateUsers(ctx, op)
	if err != nil {
		return UserStats{}, err
	}
	balance, err := quotaToMinorUnits(totals.quota, unit, c.scale)
	if err != nil {
		return UserStats{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	// 上游这条路由不返回任何数据时间戳，只能退到 Date 头/本地接收时刻。
	// 含义因此从「数据什么时候被观测」滑向「我们什么时候问的」——
	// 运行手册的验证清单里有一条专门核对它。
	observedAt := meta.observedAt(time.Time{})
	return UserStats{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			// 水位里带上活跃用户的判据与换算基数：这两个数字都是**平台自定
			// 或上游可变**的口径，看板显示「活跃 176 人」时，人要能当场问出
			// 「活跃是怎么算的」并得到答案，而不是去翻源码（普查报告的契约
			// 核对红点：ActiveUsers 无上游概念，须写判据）。
			Watermark: fmt.Sprintf("users@%d/active:last_login_%dd/qpu:%d",
				observedAt.Unix(), int(c.activeWindow.Hours()/24), unit),
			IsPartial: totals.truncated,
		},
		TotalUsers:        totals.total,
		ActiveUsers:       totals.active,
		BalanceMinorUnits: balance,
		Currency:          c.currency,
	}, nil
}

// aggregateUsers 翻页统计用户数、活跃数与额度合计。
//
// 为什么要翻页：上游**没有余额聚合端点**，也没有一个「不含软删除的用户数」
// 可以直接读（/api/user/ 的 data.total 是 Unscoped 的，把删掉的人也算在内；
// /api/user/2fa/stats 倒是不含软删除，但那是 2FA 模块的统计，拿它当用户总数
// 会让两个数在看板上对不上）。所以只能自己数。
//
// 代价是每轮同步几次列表请求，收益是这两条指标真的说得出口。
func (c *client) aggregateUsers(ctx context.Context, op string) (userTotals, respMeta, error) {
	var (
		out      userTotals
		lastMeta respMeta
		fetched  int64
		reported int64
	)
	activeSince := c.clock().Add(-c.activeWindow).Unix()

	for page := 1; page <= maxUserPages; page++ {
		var env upstreamEnvelope
		meta, err := c.get(ctx, op, routeUsers, pageQuery(page, upstreamPageSize), &env)
		if err != nil {
			return userTotals{}, meta, err
		}
		lastMeta = meta

		var payload upstreamPage[userItem]
		if err := env.decode(op, &payload); err != nil {
			return userTotals{}, meta, err
		}
		if page == 1 {
			// ⚠️ 这个 total 含软删除用户，**只用来判断翻页有没有翻完**，
			// 绝不当作用户数报出去。
			t, err := payload.Total.count()
			if err != nil {
				return userTotals{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}
		for _, item := range payload.Items {
			fetched++
			if item.softDeleted() {
				continue
			}
			out.total++
			quota, err := item.Quota.count()
			if err != nil {
				return userTotals{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			out.quota += quota
			if lastLogin, err := item.LastLoginAt.count(); err == nil && lastLogin >= activeSince {
				out.active++
			}
		}
		if len(payload.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == maxUserPages {
			out.truncated = true
		}
	}
	if reported > fetched {
		out.truncated = true
	}
	return out, lastMeta, nil
}

// ---------------------------------------------------------------------------
// 渠道
// ---------------------------------------------------------------------------

// channelItem 是 /api/channel/ 列表里本包用得到的字段。
//
// **刻意不解 key**：上游把它 Omit 出了 SQL，但字段是非指针 string，
// JSON 里仍然会出现 `"key":""`。解它没有任何用处，只会让一个凭据形状的字段
// 出现在平台的内存结构里（宪法 7 条）。
type channelItem struct {
	ID   rawAmount `json:"id"`
	Name string    `json:"name"`
	// Type 是上游的渠道类型编号。契约把它当**不透明字符串**透传给看板做分组，
	// 平台不解释其语义——上游改了取值集合不该逼平台跟着发版。
	Type rawAmount `json:"type"`
	// Status：1=启用，2=手动禁用，3=自动禁用，0=未知。
	Status rawAmount `json:"status"`
	// Balance 是渠道余额，上游是 float64 美元。
	//
	// ⚠️ 它**不是**平台自动查回来的真实账户余额：上游只在有人点
	// /api/channel/update_balance（GET 但写库，在黑名单里）时才刷新它，
	// 而且 60 种渠道类型里只有 10 种支持查余额。订阅型账号更是永远静默为 0。
	// 所以这个数的语义是「上次有人刷新时，上游认为这个渠道还剩多少」。
	Balance rawAmount `json:"balance"`
	// BalanceUpdatedTime 是余额的刷新时刻（Unix 秒）。
	//
	// **0 = 从来没刷新过 = 这个渠道没有「余额」这个概念**（普查报告钉的
	// nil 判据）。它是把「未配置」与「已耗尽」分开的唯一依据，
	// 没有它就只能看见一排理直气壮的 $0.00。
	BalanceUpdatedTime rawAmount `json:"balance_updated_time"`
	// Models 是逗号分隔的模型名字符串（**不是数组**）。
	Models string `json:"models"`
	// ResponseTime 是上次测试的响应耗时，单位已经是毫秒。
	ResponseTime rawAmount `json:"response_time"`
}

// fetchChannels 读取全部渠道。
//
// 注意它**不读 quota_per_unit**：渠道余额在上游是十进制美元（channel.balance
// 是 float64 USD），不是 quota，不需要换算基数。少一次 /api/status 请求，也让
// 「/api/status 挂了」不至于连渠道状态一起读不到——分组失败记录的价值就在
// 这种地方（见 jobs 的 newapiReadErrors）。
func (c *client) fetchChannelDirectory(ctx context.Context) (ChannelDirectorySnapshot, error) {
	const op = opChannels

	var (
		out       []ChannelStatus
		ids       []int64
		lastMeta  respMeta
		fetched   int64
		reported  int64
		truncated bool
		oldestBal time.Time
	)

	for page := 1; page <= maxChannelPages; page++ {
		var env upstreamEnvelope
		meta, err := c.get(ctx, op, routeChannels, pageQuery(page, upstreamPageSize), &env)
		if err != nil {
			return ChannelDirectorySnapshot{}, err
		}
		lastMeta = meta

		var payload upstreamPage[channelItem]
		if err := env.decode(op, &payload); err != nil {
			return ChannelDirectorySnapshot{}, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return ChannelDirectorySnapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}
		for _, item := range payload.Items {
			id, err := item.ID.count()
			if err != nil || id == 0 {
				// 没有 ID 的渠道无法被引用，也说明我们对响应形状的理解错了
				return ChannelDirectorySnapshot{}, connector.NewError(connector.KindBadResponse, op,
					fmt.Errorf("渠道条目缺少可用的 id 字段"))
			}
			status, err := item.Status.count()
			if err != nil {
				return ChannelDirectorySnapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			latency, err := item.ResponseTime.count()
			if err != nil {
				return ChannelDirectorySnapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			if latency < 0 {
				// 契约要求延迟非负；上游给了负数说明字段含义变了。
				latency = 0
			}
			balance, balanceAt, err := channelBalance(item, c.scale)
			if err != nil {
				return ChannelDirectorySnapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			if balance != nil && !balanceAt.IsZero() &&
				(oldestBal.IsZero() || balanceAt.Before(oldestBal)) {
				oldestBal = balanceAt
			}

			out = append(out, ChannelStatus{
				ChannelID:         strconv.FormatInt(id, 10),
				Name:              strings.TrimSpace(item.Name),
				Type:              strings.TrimSpace(string(item.Type)),
				Enabled:           status == channelStatusEnabled,
				BalanceMinorUnits: balance,
				Currency:          c.currency,
				ModelCount:        countModels(item.Models),
				LatencyMS:         latency,
			})
			ids = append(ids, id)
		}
		fetched += int64(len(payload.Items))
		if len(payload.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == maxChannelPages {
			truncated = true
		}
	}
	if reported > fetched {
		truncated = true
	}

	measured, err := c.fetchChannelErrorRates(ctx, op, out, ids)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}

	observedAt := lastMeta.observedAt(time.Time{})
	base := watermarkForChannels(observedAt, oldestBal)
	coveragePartial := false
	for i := range out {
		if !measured[i] {
			coveragePartial = true
		}
		out[i].Snapshot = Snapshot{
			ObservedAt: observedAt,
			Watermark:  base,
			// 没算错误率的渠道单独标记为部分数据：它的 ErrorRatePPM 是 0，
			// 而 0 在契约里的意思是「测了，没有错误」。不标记的话，一个
			// 从没被测过的渠道会以「健康」的姿态出现在看板上。
			IsPartial: truncated || !measured[i],
		}
	}
	reportedCount := reported
	return ChannelDirectorySnapshot{
		Snapshot: Snapshot{ObservedAt: observedAt, Watermark: base, IsPartial: truncated || coveragePartial},
		Completeness: DirectoryCompleteness{
			Complete:      !truncated && fetched == reported,
			Truncated:     truncated,
			ReportedCount: &reportedCount,
			FetchedCount:  fetched,
			Evidence:      "reported_count",
		},
		CoveragePartial: coveragePartial,
		Items:           out,
	}, nil
}

func (c *client) fetchChannels(ctx context.Context) ([]ChannelStatus, error) {
	directory, err := c.fetchChannelDirectory(ctx)
	if err != nil {
		return nil, err
	}
	return LegacyChannelStatuses(directory), nil
}

// channelBalance 按普查报告的判据决定渠道余额是不是「未配置」。
//
// 判据只有一条：**balance_updated_time == 0 就是从来没刷新过**，
// 那意味着这个渠道没有「余额」这个概念（订阅型账号、或者根本不支持查余额的
// 渠道类型），返回 nil。返回 0 会在看板上变成「这个渠道没钱了」的假警报。
//
// 第二个返回值是余额的刷新时刻，用来标注这个数字有多旧——上游默认**不会**
// 自动刷新余额（要人去点 update_balance，而那是个写端点），所以真实实例上
// 这个时刻可能是几个月前。
func channelBalance(item channelItem, scale int) (*int64, time.Time, error) {
	updatedAt := parseUnixSeconds(item.BalanceUpdatedTime)
	if updatedAt.IsZero() {
		return nil, time.Time{}, nil
	}
	minor, err := item.Balance.minorUnits(scale)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &minor, updatedAt, nil
}

// watermarkForChannels 把渠道余额的最旧刷新时刻写进水位。
//
// 契约 v1 的 ChannelStatus 没有「余额观测时刻」这个字段，而余额的新鲜度与
// 渠道状态的新鲜度**在这个上游上差得很远**（状态是刚读的，余额可能是几个月前
// 有人手点出来的）。把它塞进水位是契约内唯一能如实表达这件事的位置：
// 水位本来就是「我读到了哪儿」的自由文本，看板与排查的人都看得到。
//
// 契约 v2 应当给它一个正经字段，见 PR 的 follow_ups。
func watermarkForChannels(observedAt, oldestBalanceAt time.Time) string {
	if oldestBalanceAt.IsZero() {
		return fmt.Sprintf("channels@%d/balance:none", observedAt.Unix())
	}
	return fmt.Sprintf("channels@%d/balance_oldest:%d", observedAt.Unix(), oldestBalanceAt.Unix())
}

// countModels 数一个渠道挂了多少个模型。
//
// 上游的 models 是逗号分隔的字符串，而且它自己的 GetModels() 只 Trim 首尾逗号、
// 不做 TrimSpace——所以 "a, b,,c" 这种脏数据是可能的。这里按逗号切开后逐个
// 去空白并丢掉空项，得到的才是「真的挂了几个模型」。
func countModels(models string) int64 {
	var n int64
	for _, part := range strings.Split(models, ",") {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n
}

// fetchChannelErrorRates 逐渠道算错误率，就地填进 channels。
//
// 上游**没有错误率端点**，只能自己算（普查报告：ErrorRatePPM 须由 logs
// type=5/(2,5) 自算）。做法是对 /api/log/ 发两次 COUNT 查询：
//
//	分子 = type=5（错误日志）的条数
//	分母 = type=2（消费日志）+ type=5 的条数
//
// 只取分页外壳里的 total，**page_size=1，日志正文一条都不解**：日志里带 IP、
// 用户名、token 名这些个人数据，采集链路一个字都不该碰（宪法 7 条）。
//
// 返回的 measured[i] 说明第 i 个渠道到底算没算。超出 maxErrorRateChannels 的
// 渠道不算——两次请求乘以渠道数会把一轮同步的预算吃光。启用的渠道排在前面：
// 停用渠道按契约一律不算异常，它们的错误率对看板的判断没有影响。
func (c *client) fetchChannelErrorRates(
	ctx context.Context, op string, channels []ChannelStatus, ids []int64,
) ([]bool, error) {
	measured := make([]bool, len(channels))
	if len(channels) == 0 {
		return measured, nil
	}

	start, end := c.errorRateWindow()
	budget := maxErrorRateChannels
	for _, idx := range errorRatePriority(channels) {
		if budget <= 0 {
			break
		}
		budget--

		errorLogs, err := c.countLogs(ctx, op, ids[idx], logTypeError, start, end)
		if err != nil {
			return nil, err
		}
		consumeLogs, err := c.countLogs(ctx, op, ids[idx], logTypeConsume, start, end)
		if err != nil {
			return nil, err
		}
		ppm, err := ratePPM(errorLogs, errorLogs+consumeLogs)
		if err != nil {
			return nil, connector.NewError(connector.KindBadResponse, op, err)
		}
		channels[idx].ErrorRatePPM = ppm
		measured[idx] = true
	}
	return measured, nil
}

// errorRatePriority 给出算错误率的顺序：启用的渠道优先。
//
// 预算有限时先算启用的：停用渠道按契约一律不算异常（ChannelStatus.Unhealthy），
// 所以它们的错误率对「有几个渠道不对劲」这个问题没有贡献。
// 同一组内保持读到的顺序，让结果稳定可复现。
func errorRatePriority(channels []ChannelStatus) []int {
	order := make([]int, len(channels))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return channels[order[a]].Enabled && !channels[order[b]].Enabled
	})
	return order
}

// errorRateWindow 是错误率的统计窗口：最近 24 小时。
//
// 为什么不是「当前业务日」：业务日刚过零点时窗口里只有几分钟的数据，
// 一次偶发错误就能把错误率顶到 100%，看板会在每天凌晨规律性地报警。
// 滑动 24 小时的分母始终是满的。
func (c *client) errorRateWindow() (start, end int64) {
	now := c.clock()
	return now.Add(-24 * time.Hour).Unix(), now.Unix()
}

// countLogs 用 /api/log/ 的分页 total 当 COUNT 用。
//
// ⚠️ 查询参数叫 **channel**（不是 channel_id），对应的 SQL 列才是 channel_id。
// 另外 type=0 在上游的意思是「不过滤」，所以调用方绝不能传 0。
func (c *client) countLogs(ctx context.Context, op string, channelID int64, logType int, start, end int64) (int64, error) {
	if logType == 0 {
		return 0, connector.NewError(connector.KindInternal, op,
			fmt.Errorf("logType=0 在上游表示不过滤，错误率口径会静默变成全量"))
	}
	query := pageQuery(1, 1)
	query.Set("type", strconv.Itoa(logType))
	query.Set("channel", strconv.FormatInt(channelID, 10))
	query.Set("start_timestamp", strconv.FormatInt(start, 10))
	query.Set("end_timestamp", strconv.FormatInt(end, 10))

	var env upstreamEnvelope
	if _, err := c.get(ctx, op, routeLogs, query, &env); err != nil {
		return 0, err
	}
	// 只解 total：items 里是日志正文，一条都不要。
	var payload struct {
		Total rawAmount `json:"total"`
	}
	if err := env.decode(op, &payload); err != nil {
		return 0, err
	}
	total, err := payload.Total.count()
	if err != nil {
		return 0, connector.NewError(connector.KindBadResponse, op, err)
	}
	if total < 0 {
		return 0, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("日志条数为负: %d", total))
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// 业务日：充值与订阅
// ---------------------------------------------------------------------------

// topupItem 是 /api/user/topup 列表里本包用得到的字段。
//
// 不解 trade_no：订单号是业务标识，聚合金额用不上它，
// 而它会出现在错误信息与日志里的风险不为零。
type topupItem struct {
	// Amount 与 Money 的语义**随 payment_provider 变**，见 topupUSD。
	Amount rawAmount `json:"amount"`
	Money  rawAmount `json:"money"`
	// PaymentProvider 决定上面两个字段哪个才是美元金额。
	PaymentProvider string `json:"payment_provider"`
	// Status 是字符串（"pending"/"success"/"failed"），不是 int。
	Status string `json:"status"`
	// CreateTime / CompleteTime 是 Unix 秒。
	CreateTime   rawAmount `json:"create_time"`
	CompleteTime rawAmount `json:"complete_time"`
}

// 充值订单的金额语义分桶（普查报告：charge money 三语义按 provider 分桶）。
//
// 上游没有「这笔订单值多少美元」这个字段，只有 Amount 与 Money 两个数，
// 而**哪个是美元取决于 provider**。三种语义都是从上游 model/topup.go 里
// 各个 Recharge* 函数计算 quota 的那一行倒推出来的：
//
//	epay          quota = Amount × QuotaPerUnit   → 美元 = Amount
//	waffo         quota = Amount × QuotaPerUnit   → 美元 = Amount
//	waffo_pancake quota = Amount × QuotaPerUnit   → 美元 = Amount
//	stripe        quota = Money  × QuotaPerUnit   → 美元 = Money（经分组倍率换算后）
//	creem         quota = Amount                  → 美元 = Amount / QuotaPerUnit
//
// balance（余额内部划转）不算收入：那笔钱早就在平台里了，
// 把它计进当日充值等于把同一笔钱数两遍。
//
// 认不出来的 provider **不猜**：既不按 Amount 也不按 Money 顶上，
// 而是计进 unclassified 并把这一天标记为部分数据。猜错的那一笔是
// 「看起来完全正常」的错数字，最难查。
const (
	providerEpay         = "epay"
	providerStripe       = "stripe"
	providerCreem        = "creem"
	providerWaffo        = "waffo"
	providerWaffoPancake = "waffo_pancake"
	providerBalance      = "balance"
)

// topupKind 是一笔充值订单在收入口径下的归属。
//
// 三态而不是「认不认得出」两态：内部划转（provider=balance）是**认得出来的
// 非收入**，与「provider 不认识、金额只好丢掉」完全不同。合成两态之后，
// 一个正常的余额划转会把整天标记成部分数据，或者一笔看不懂的订单会被静静
// 当成 0——两种错都会让人对着看板做错判断。
type topupKind int

const (
	// topupUnknown：provider 认不出来，金额语义未知，只能丢掉并如实标注。
	topupUnknown topupKind = iota
	// topupRevenue：外部真金白银进来了，计入当日充值。
	topupRevenue
	// topupInternal：平台内部划转（余额支付），钱早就在系统里了。
	// 计进当日充值等于把同一笔钱数两遍，所以既不计金额也不计单数。
	topupInternal
)

// topupUSDQuota 把一笔充值折算成**quota 单位**的整数。
//
// 为什么统一折成 quota 而不是直接折成美分：三种语义里 creem 那一支本来就是
// quota，另外两支是「美元 × quota_per_unit = quota」。全部折到 quota 之后
// 就能整天先 SUM、最后只除一次 quota_per_unit——这正是金额纪律要求的
// 「先 SUM 再除」（每笔各自除完再相加，误差会按笔数累积）。
func topupUSDQuota(item topupItem, quotaPerUnit int64) (int64, topupKind, error) {
	switch strings.ToLower(strings.TrimSpace(item.PaymentProvider)) {
	case providerEpay, providerWaffo, providerWaffoPancake:
		usd, err := item.Amount.minorUnits(quotaScaleDigits)
		if err != nil {
			return 0, topupUnknown, err
		}
		quota, err := scaledUSDToQuota(usd, quotaPerUnit)
		if err != nil {
			return 0, topupUnknown, err
		}
		return quota, topupRevenue, nil
	case providerStripe:
		usd, err := item.Money.minorUnits(quotaScaleDigits)
		if err != nil {
			return 0, topupUnknown, err
		}
		quota, err := scaledUSDToQuota(usd, quotaPerUnit)
		if err != nil {
			return 0, topupUnknown, err
		}
		return quota, topupRevenue, nil
	case providerCreem:
		// Creem 的 Amount 直接就是 quota，不乘 quota_per_unit。
		quota, err := item.Amount.count()
		if err != nil {
			return 0, topupUnknown, err
		}
		return quota, topupRevenue, nil
	case providerBalance:
		return 0, topupInternal, nil
	default:
		return 0, topupUnknown, nil
	}
}

// quotaScaleDigits 是把美元金额放大成整数时用的小数位。
//
// 6 位而不是 2 位：上游的 Money 是 decimal，Stripe 的分组倍率换算能产出
// 小数点后好几位；先按 6 位留住精度，最后折算成 quota 时再舍一次，
// 比一上来就舍到分要准。
const quotaScaleDigits = 6

// scaledUSDToQuota 把「放大 10^quotaScaleDigits 倍的美元整数」折成 quota。
//
//	quota = 美元 × quota_per_unit = scaledUSD × quota_per_unit / 10^6
//
// 全程整数，溢出就报错而不是回落到浮点。
func scaledUSDToQuota(scaledUSD, quotaPerUnit int64) (int64, error) {
	if quotaPerUnit <= 0 {
		return 0, fmt.Errorf("quota_per_unit=%d 非正: %w", quotaPerUnit, errAmountFormat)
	}
	negative := scaledUSD < 0
	magnitude := scaledUSD
	if negative {
		if scaledUSD == -maxInt64-1 {
			return 0, fmt.Errorf("金额溢出 int64: %w", errAmountFormat)
		}
		magnitude = -scaledUSD
	}
	if magnitude > maxInt64/quotaPerUnit {
		return 0, fmt.Errorf("金额 %d 乘 quota_per_unit 后溢出 int64: %w", scaledUSD, errAmountFormat)
	}
	factor := pow10(quotaScaleDigits)
	quota := (magnitude*quotaPerUnit + factor/2) / factor
	if negative {
		return -quota, nil
	}
	return quota, nil
}

// rechargeDay 是充值侧对某个业务日的回答。
type rechargeDay struct {
	// quota 是当日成功充值折算出的 quota 合计（尚未换算成钱）。
	quota int64
	// count 是当日成功充值的订单数。
	count int64
	// unclassified 是认不出 provider 的订单数——它们的金额被丢掉了，
	// 必须体现成部分数据。
	unclassified int64
	// truncated 表示翻页到顶还没扫到窗口外，可能漏了更早的订单。
	truncated bool
}

func (c *client) fetchDailyOrders(ctx context.Context, day time.Time) (OrderSummary, error) {
	const op = opOrders
	dayText := day.Format(newapiBusinessDayLayout)

	unit, err := c.quotaPerUnit(ctx, op)
	if err != nil {
		return OrderSummary{}, err
	}
	start, end := dayWindow(day, c.businessDay)

	recharge, meta, err := c.fetchRechargeDay(ctx, op, unit, start, end)
	if err != nil {
		return OrderSummary{}, err
	}
	rechargeMinor, err := quotaToMinorUnits(recharge.quota, unit, c.scale)
	if err != nil {
		return OrderSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	observedAt := meta.observedAt(time.Time{})
	return OrderSummary{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			// 水位带上业务日与**每一处缺口**：光有时间戳分不清「这是哪一天的
			// 汇总」，也看不出订阅金额为什么是 0、有没有订单的金额被丢掉。
			// 这条指标恒为部分数据（见下面的 IsPartial），所以「到底缺了什么」
			// 只能靠水位说清楚，否则运维只看得到一个永远亮着的黄灯。
			Watermark: rechargeWatermark(dayText, observedAt, recharge),
			// **这条指标永远是部分数据**，而且理由与充值侧读没读全无关：
			// 上游根本没有订阅订单的列表端点（见 subscriptionGapReason）。
			// 订阅金额恒为 0 且必须被标注出来，否则看板会把「读不到」
			// 显示成「这个月没人订阅」。
			IsPartial: true,
		},
		Day:                dayText,
		RechargeMinorUnits: rechargeMinor,
		// 订阅金额读不到，留 0——但绝不假装它是真的 0，见上面的 IsPartial。
		SubscriptionMinorUnits: 0,
		// ⚠️ OrderCount 只含充值订单。契约的原意是「两类订单合计单数」，
		// 而订阅订单在 HTTP 上根本列不出来，所以这个数**偏小**。
		// 与其编一个凑数的加数，不如少报并把缺口写进水位。
		OrderCount: recharge.count,
		Currency:   c.currency,
	}, nil
}

// rechargeWatermark 把当日汇总的每一处缺口写进水位。
//
// 三件事必须能被看见：这是哪一天、订阅金额为什么是 0、有没有订单的金额被丢掉。
// 前两件恒定存在，后两件只在真的发生时才出现——恒定出现的标记会被人忽略，
// 只在出事时出现的标记才有信号价值。
func rechargeWatermark(dayText string, observedAt time.Time, day rechargeDay) string {
	mark := fmt.Sprintf("day:%s@%d/subscription:%s", dayText, observedAt.Unix(), subscriptionGapReason)
	if day.unclassified > 0 {
		// 有订单的 payment_provider 我们不认识，金额被丢掉了。运维需要知道
		// 丢了几笔，才判断得出「这天的充值数字能不能用」。
		mark += fmt.Sprintf("/unclassified:%d", day.unclassified)
	}
	if day.truncated {
		mark += "/truncated"
	}
	return mark
}

// subscriptionGapReason 是订阅金额读不到的原因，写进水位让人能当场看懂。
//
// 上游把订阅订单存在 subscription_orders 表里，但**没有任何 HTTP 端点列它**
// （普查报告核实过：全仓只有内部的 GetSubscriptionOrderByTradeNo）。
// 能读到的只有 /api/subscription/admin/users/:id/subscriptions——那是「某个人
// 现在有哪些订阅」，不是「这一天收了多少订阅费」，两者换不过来。
//
// 这条缺口属于 DSN 直查（普查报告的 P4：订阅在册走只读 DSN），本任务范围外。
const subscriptionGapReason = "unavailable_over_http"

// fetchRechargeDay 翻页扫充值订单，累加落在业务日窗口内的成功订单。
//
// 上游这条路由**没有任何时间过滤参数**，只能自己翻页筛。好在它按 id desc 排
// （model/topup.go 的 GetAllTopUps），所以从第一页往后扫，扫到订单的
// create_time 比「窗口起点再往前 topupScanMarginDays 天」还早时就能停手。
//
// 为什么要留余量而不是扫到 create_time < 窗口起点就停：业务日看的是
// complete_time，一笔昨天下单、今天才回调成功的订单 id 比今天下的单小，
// 却属于今天的收入。
func (c *client) fetchRechargeDay(
	ctx context.Context, op string, quotaPerUnit, start, end int64,
) (rechargeDay, respMeta, error) {
	var (
		out      rechargeDay
		lastMeta respMeta
		fetched  int64
		reported int64
	)
	scanFloor := start - int64(topupScanMarginDays)*86400

	for page := 1; page <= maxTopupPages; page++ {
		var env upstreamEnvelope
		meta, err := c.get(ctx, op, routeTopups, pageQuery(page, upstreamPageSize), &env)
		if err != nil {
			return rechargeDay{}, meta, err
		}
		lastMeta = meta

		var payload upstreamPage[topupItem]
		if err := env.decode(op, &payload); err != nil {
			return rechargeDay{}, meta, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return rechargeDay{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}

		reachedFloor := false
		for _, item := range payload.Items {
			createdAt, err := item.CreateTime.count()
			if err != nil {
				return rechargeDay{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			if createdAt > 0 && createdAt < scanFloor {
				// 列表按 id desc，走到这里说明后面只会更早。
				reachedFloor = true
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(item.Status), topupStatusSuccess) {
				continue
			}
			completedAt, err := item.CompleteTime.count()
			if err != nil {
				return rechargeDay{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			// 业务日按**到账时刻**切，不按下单时刻：钱是那天进来的才算那天的收入。
			// complete_time 缺失的成功订单无法归日，计进 unclassified 而不是
			// 硬塞进当天。
			if completedAt <= 0 {
				out.unclassified++
				continue
			}
			if completedAt < start || completedAt > end {
				continue
			}
			quota, kind, err := topupUSDQuota(item, quotaPerUnit)
			if err != nil {
				return rechargeDay{}, meta, connector.NewError(connector.KindBadResponse, op, err)
			}
			switch kind {
			case topupUnknown:
				out.unclassified++
			case topupInternal:
				// 认得出来的非收入：既不计金额也不计单数，也不算缺口。
			case topupRevenue:
				out.quota += quota
				out.count++
			}
		}

		fetched += int64(len(payload.Items))
		if reachedFloor || len(payload.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == maxTopupPages {
			out.truncated = true
		}
	}
	return out, lastMeta, nil
}

// dayWindow 把业务日换算成上游要的秒级时间戳区间（闭区间）。
//
// 上游的过滤是 `created_at >= start AND created_at <= end`（log 与 usedata
// 两处都是），所以 end 取当日最后一秒，而不是次日零点——用次日零点会把
// 次日零点整那一秒的数据算进前一天，跨日边界上出现重复计数。
func dayWindow(day time.Time, loc *time.Location) (start, end int64) {
	local := day.In(loc)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return from.Unix(), from.AddDate(0, 0, 1).Unix() - 1
}

// ---------------------------------------------------------------------------
// 逐模型用量
// ---------------------------------------------------------------------------

// quotaDataItem 是 /api/data/ 数组元素里本包用得到的字段。
//
// ⚠️ 这个端点的 SELECT 是**投影过的**：不带 username 时上游只 SELECT
// model_name / count / quota / token_used / created_at，其余字段在 JSON 里
// 全是零值。所以这里也只解这几个——解别的只会解出一堆 0，
// 然后有人拿那些 0 当真。
type quotaDataItem struct {
	ModelName string    `json:"model_name"`
	Count     rawAmount `json:"count"`
	Quota     rawAmount `json:"quota"`
	// CreatedAt 是**小时桶**的起点（上游落盘时对齐到整点）。
	CreatedAt rawAmount `json:"created_at"`
}

func (c *client) fetchModelUsages(ctx context.Context, day time.Time) ([]ModelUsage, error) {
	const op = opModels

	unit, err := c.quotaPerUnit(ctx, op)
	if err != nil {
		return nil, err
	}
	start, end := dayWindow(day, c.businessDay)

	query := url.Values{
		"start_timestamp": {strconv.FormatInt(start, 10)},
		"end_timestamp":   {strconv.FormatInt(end, 10)},
	}
	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeUsageData, query, &env)
	if err != nil {
		return nil, err
	}
	// data 是**裸数组**，不是 {items,total}——这个端点不走 PageInfo。
	var rows []quotaDataItem
	if err := env.decode(op, &rows); err != nil {
		return nil, err
	}

	// 上游按 (model_name, 小时) 二维分组，同一个模型一天会有多个桶。
	// 这里按模型合并：先把 quota 全部加完，**最后每个模型只除一次**
	// quota_per_unit（见 quotaToMinorUnits 的调用纪律）。
	type bucket struct {
		requests int64
		quota    int64
	}
	totals := make(map[string]*bucket, len(rows))
	order := make([]string, 0, len(rows))
	var newestBucket time.Time

	for _, row := range rows {
		name := strings.TrimSpace(row.ModelName)
		if name == "" {
			// 没有模型名的桶无法归属，也说明我们对形状的理解错了。
			return nil, connector.NewError(connector.KindBadResponse, op,
				fmt.Errorf("用量条目缺少 model_name"))
		}
		count, err := row.Count.count()
		if err != nil {
			return nil, connector.NewError(connector.KindBadResponse, op, err)
		}
		quota, err := row.Quota.count()
		if err != nil {
			return nil, connector.NewError(connector.KindBadResponse, op, err)
		}
		if at := parseUnixSeconds(row.CreatedAt); !at.IsZero() && at.After(newestBucket) {
			newestBucket = at
		}
		b, seen := totals[name]
		if !seen {
			b = &bucket{}
			totals[name] = b
			order = append(order, name)
		}
		b.requests += count
		b.quota += quota
	}

	// ⚠️ 新鲜度：这批数字来自上游的 quota_data 表，由后台每 5 分钟
	// （common.DataExportInterval）落盘一次，所以**最近 5 分钟的用量还没入库**。
	// ObservedAt 取响应时刻（我们什么时候问的），最新的小时桶写进水位——
	// 拿最新桶当 ObservedAt 会让一个刚过整点的读取显示成「1 小时前的数据」，
	// 那比实际情况悲观得多。
	observedAt := meta.observedAt(time.Time{})
	snapshot := Snapshot{
		ObservedAt: observedAt,
		Watermark: fmt.Sprintf("day:%s@%d/bucket:%d",
			day.Format(newapiBusinessDayLayout), observedAt.Unix(), newestBucket.Unix()),
	}

	out := make([]ModelUsage, 0, len(order))
	for _, name := range order {
		b := totals[name]
		consumed, err := quotaToMinorUnits(b.quota, unit, c.scale)
		if err != nil {
			return nil, connector.NewError(connector.KindBadResponse, op, err)
		}
		out = append(out, ModelUsage{
			Snapshot:           snapshot,
			ModelName:          name,
			RequestCount:       b.requests,
			ConsumedMinorUnits: consumed,
			Currency:           c.currency,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 逐笔订单与按日资金汇总（XM-PAY0）
// ---------------------------------------------------------------------------

// maxPaymentOrderPages 给逐笔订单翻页一个硬上限（100×20=2000 笔/次查询）。
// 与 maxTopupPages 用同一个上游端点，取同样的上限——这条链路同样在 HTTP
// 请求超时预算（router.go 30 秒）内被调用，不是后台协程。
const maxPaymentOrderPages = 20

// adminTopupItem 是 /api/user/topup 列表元素里 XM-PAY0 用得到的字段，
// 比既有的 topupItem 多了 id/user_id/trade_no。
//
// 不复用 topupItem 加字段：那个类型只服务于 fetchRechargeDay 的聚合，
// 顶部明写"不解 trade_no：订单号是业务标识，聚合金额用不上它"——给它加上
// 这三个字段会让那句注释失真，也会让那条调用路径多解出用不上的字段。
// 两个类型的 Amount/Money/PaymentProvider/Status/CreateTime/CompleteTime
// 字段名与语义完全一致，转换只是逐字段拷贝，见 decodePaymentOrderRow。
type adminTopupItem struct {
	ID              rawAmount `json:"id"`
	UserID          rawAmount `json:"user_id"`
	Amount          rawAmount `json:"amount"`
	Money           rawAmount `json:"money"`
	TradeNo         string    `json:"trade_no"`
	PaymentProvider string    `json:"payment_provider"`
	Status          string    `json:"status"`
	CreateTime      rawAmount `json:"create_time"`
	CompleteTime    rawAmount `json:"complete_time"`
}

// paymentOrderRow 是 fetchOrders 的内部产出：公开的 Order 之外多带 Quota——
// **未换算成钱的原始 quota**，topupRevenue 之外的订单（内部划转/provider
// 未识别）恒为 0。
//
// 为什么不直接把每行的 AmountMinorUnits 加总当桶合计：quotaToMinorUnits 的
// 调用纪律是"先 SUM quota 再换算一次"（amount.go 的注释——每条明细各自换算
// 再相加，误差按条数累积）。桶合计因此必须走 Quota 求和 + 换算一次，
// 不能直接累加各行已经换算过的 AmountMinorUnits；后者只用于单行展示。
type paymentOrderRow struct {
	Order
	Quota int64
}

// fetchOrders 翻页读取 [from, to] 闭区间（按 create_time）内、可选按 status
// 过滤的全部充值订单。
//
// 上游按 id desc 排序且**没有日期过滤参数**（routeTopups），所以从第一页往后
// 翻，一旦遇到 create_time < from 的订单就可以停手——不需要
// fetchRechargeDay 的回溯余量（topupScanMarginDays）：那个余量是为了补偿
// "排序键 id 与窗口键 complete_time 不同步"，这里窗口键统一换成了
// create_time，而 id 与 create_time 都在插入那一刻确定，天然同步
// （见 payments.go 顶部关于窗口口径的说明）。
//
// status 在这里**恒为客户端过滤**：上游列表端点原生只支持 keyword，
// 不支持 status（与 sub2api 的 /admin/payment/orders 不同，那条原生支持
// status，本函数因此没有把 status 塞进 pageQuery）。
//
// unclassified 返回值：窗口内是否出现过 provider 未识别的订单（topupUnknown）
// ——出现了就该整体标记为部分数据，理由见 payments.go 里 Order.AmountMinorUnits
// 的注释。
func (c *client) fetchOrders(
	ctx context.Context, op string, quotaPerUnit int64, from, to time.Time, status string,
) (rows []paymentOrderRow, meta respMeta, truncated, unclassified bool, err error) {
	var (
		fetched, reported int64
	)
	fromUnix, toUnix := from.Unix(), to.Unix()

	for page := 1; page <= maxPaymentOrderPages; page++ {
		var env upstreamEnvelope
		pageMeta, perr := c.get(ctx, op, routeTopups, pageQuery(page, upstreamPageSize), &env)
		if perr != nil {
			return nil, pageMeta, false, false, perr
		}
		meta = pageMeta

		var payload upstreamPage[adminTopupItem]
		if derr := env.decode(op, &payload); derr != nil {
			return nil, meta, false, false, derr
		}
		if page == 1 {
			t, terr := payload.Total.count()
			if terr != nil {
				return nil, meta, false, false, connector.NewError(connector.KindBadResponse, op, terr)
			}
			reported = t
		}

		reachedFloor := false
		for _, item := range payload.Items {
			createdAtSecs, cerr := item.CreateTime.count()
			if cerr != nil {
				return nil, meta, false, false, connector.NewError(connector.KindBadResponse, op, cerr)
			}
			if createdAtSecs <= 0 {
				// 没有可用的下单时刻，无法归入任何窗口——跳过，不触发早停
				// （既不是"更早"也不是"更晚"，是"不知道"）。
				continue
			}
			if createdAtSecs < fromUnix {
				// 按 id desc 排序，走到这里说明后面只会更早。
				reachedFloor = true
				continue
			}
			if createdAtSecs > toUnix {
				continue
			}
			if status != "" && strings.ToLower(strings.TrimSpace(item.Status)) != status {
				continue
			}

			row, kind, derr := decodePaymentOrderRow(item, createdAtSecs, quotaPerUnit, c.scale, c.currency)
			if derr != nil {
				return nil, meta, false, false, connector.NewError(connector.KindBadResponse, op, derr)
			}
			if kind == topupUnknown {
				unclassified = true
			}
			rows = append(rows, row)
		}

		fetched += int64(len(payload.Items))
		if reachedFloor || len(payload.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == maxPaymentOrderPages {
			truncated = true
		}
	}
	return rows, meta, truncated, unclassified, nil
}

// decodePaymentOrderRow 把上游的原始充值 JSON 换算成 paymentOrderRow，
// 复用既有的 topupUSDQuota 做 provider 分桶（与 fetchRechargeDay 同一套
// 判据，不重新实现）。
func decodePaymentOrderRow(
	item adminTopupItem, createdAtSecs, quotaPerUnit int64, scale int, currency string,
) (paymentOrderRow, topupKind, error) {
	id, err := item.ID.count()
	if err != nil {
		return paymentOrderRow{}, topupUnknown, err
	}
	userID, err := item.UserID.count()
	if err != nil {
		return paymentOrderRow{}, topupUnknown, err
	}

	quota, kind, err := topupUSDQuota(topupItem{
		Amount: item.Amount, Money: item.Money, PaymentProvider: item.PaymentProvider,
		Status: item.Status, CreateTime: item.CreateTime, CompleteTime: item.CompleteTime,
	}, quotaPerUnit)
	if err != nil {
		return paymentOrderRow{}, topupUnknown, err
	}

	rowQuota := int64(0)
	amountMinor := int64(0)
	if kind == topupRevenue {
		rowQuota = quota
		amountMinor, err = quotaToMinorUnits(quota, quotaPerUnit, scale)
		if err != nil {
			return paymentOrderRow{}, kind, err
		}
	}
	// topupInternal（provider=balance）与 topupUnknown 都报 0，理由见
	// payments.go 的 Order.AmountMinorUnits 注释——不重新解释 Amount/Money
	// 语义，沿用 topupUSDQuota 对这两种情形的既有处理。

	return paymentOrderRow{
		Order: Order{
			OrderID:          strconv.FormatInt(id, 10),
			CreatedAt:        time.Unix(createdAtSecs, 0).UTC(),
			Status:           strings.ToLower(strings.TrimSpace(item.Status)),
			AmountMinorUnits: amountMinor,
			Currency:         currency,
			Method:           strings.ToLower(strings.TrimSpace(item.PaymentProvider)),
			UserRef:          userRef(userID),
			UpstreamOrderRef: strings.TrimSpace(item.TradeNo),
		},
		Quota: rowQuota,
	}, kind, nil
}

// fetchOrderPage 读取 filter 窗口内的全部订单及其按原始状态的统计。
// filter 已由调用方（client.go 的 ListOrders）校验过。
func (c *client) fetchOrderPage(ctx context.Context, op string, filter OrderFilter) (OrderPage, error) {
	quotaPerUnit, err := c.quotaPerUnit(ctx, op)
	if err != nil {
		return OrderPage{}, err
	}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	rows, meta, truncated, unclassified, err := c.fetchOrders(ctx, op, quotaPerUnit, filter.From, filter.To, status)
	if err != nil {
		return OrderPage{}, err
	}

	items := make([]Order, 0, len(rows))
	quotaByStatus := make(map[string]int64)
	countByStatus := make(map[string]int64)
	for _, row := range rows {
		items = append(items, row.Order)
		countByStatus[row.Status]++
		quotaByStatus[row.Status] += row.Quota
	}
	stats := make(map[string]OrderStats, len(countByStatus))
	for st, quota := range quotaByStatus {
		// SUM quota 完再换算一次（amount.go 的调用纪律），不是累加各行已经
		// 换算过的 AmountMinorUnits。
		amount, err := quotaToMinorUnits(quota, quotaPerUnit, c.scale)
		if err != nil {
			return OrderPage{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		stats[st] = OrderStats{Count: countByStatus[st], AmountMinorUnits: amount}
	}

	observedAt := meta.observedAt(time.Time{})
	return OrderPage{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  orderWindowWatermark(filter.From, filter.To, observedAt, truncated, unclassified),
			IsPartial:  truncated || unclassified,
		},
		Items:         items,
		StatsByStatus: stats,
	}, nil
}

// orderWindowWatermark 把窗口边界与"为什么可能不完整"一起编码进水位——只给
// IsPartial 布尔值，运维分不清是翻页到顶（maxPaymentOrderPages）还是撞见了
// provider 未识别的订单（unclassified），两者的处置完全不同。做法与既有
// rechargeWatermark 的水位纪律一致：恒定信息（窗口边界）总在，只在真的发生
// 时才出现的信号（缺口原因）才出现。
func orderWindowWatermark(from, to, observedAt time.Time, truncated, unclassified bool) string {
	mark := fmt.Sprintf("window:%s..%s@%d", from.Format(time.RFC3339), to.Format(time.RFC3339), observedAt.Unix())
	if unclassified {
		mark += "/unclassified"
	}
	if truncated {
		mark += "/truncated"
	}
	return mark
}

// fetchDailyPaymentSummary 读取某业务日按归一化状态分桶的资金汇总。
// parsed 已由调用方（client.go 的 DailyPaymentSummary）按业务日时区解析过。
func (c *client) fetchDailyPaymentSummary(ctx context.Context, op string, parsed time.Time) (DailyPaymentSummary, error) {
	quotaPerUnit, err := c.quotaPerUnit(ctx, op)
	if err != nil {
		return DailyPaymentSummary{}, err
	}
	startUnix, endUnix := dayWindow(parsed, c.businessDay)
	from, to := time.Unix(startUnix, 0).UTC(), time.Unix(endUnix, 0).UTC()

	rows, meta, truncated, unclassified, err := c.fetchOrders(ctx, op, quotaPerUnit, from, to, "")
	if err != nil {
		return DailyPaymentSummary{}, err
	}

	quotaByBucket := make(map[string]int64)
	countByBucket := make(map[string]int64)
	unrecognizedStatus := false
	for _, row := range rows {
		bucket, ok := paymentStatusBucket(row.Status)
		if !ok {
			unrecognizedStatus = true
			continue
		}
		countByBucket[bucket]++
		quotaByBucket[bucket] += row.Quota
	}

	byStatus := make(map[string]StatusAmount, len(countByBucket))
	for bucket, quota := range quotaByBucket {
		amount, err := quotaToMinorUnits(quota, quotaPerUnit, c.scale)
		if err != nil {
			return DailyPaymentSummary{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		byStatus[bucket] = StatusAmount{Count: countByBucket[bucket], AmountMinorUnits: amount}
	}

	observedAt := meta.observedAt(time.Time{})
	dayText := parsed.Format(newapiBusinessDayLayout)
	return DailyPaymentSummary{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  dailyPaymentWatermark(dayText, observedAt, truncated, unclassified, unrecognizedStatus),
			IsPartial:  truncated || unclassified || unrecognizedStatus,
		},
		Day:           dayText,
		Currency:      c.currency,
		ByStatus:      byStatus,
		FeeMinorUnits: nil,
		NetMinorUnits: nil,
	}, nil
}

// dailyPaymentWatermark 把业务日与"为什么可能不完整"一起编码进水位，
// 理由与 orderWindowWatermark 相同——见该函数的注释。
func dailyPaymentWatermark(dayText string, observedAt time.Time, truncated, unclassified, unrecognizedStatus bool) string {
	mark := fmt.Sprintf("day:%s@%d", dayText, observedAt.Unix())
	if unclassified {
		mark += "/unclassified"
	}
	if unrecognizedStatus {
		mark += "/unrecognized_status"
	}
	if truncated {
		mark += "/truncated"
	}
	return mark
}
