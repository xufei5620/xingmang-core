// Package reqlog 定义「请求审计系统」（reqlog）的只读接入契约。
//
// reqlog 是**已经在生产运行的外挂系统**（2026-08-25 上线，/root/reqlog）：
// 宝塔 nginx 与 NewAPI(:3000)/Sub2API(:8081) 之间的透明反向代理，零改上游，
// 全量明文抄录 /v1/* 的请求与响应（含完整 SSE 事件流），127.0.0.1:9300 提供
// 只读控制台。平台在这条链路上的角色是**带权限与审计的只读网关**，
// 不是第二份存储：请求正文永不落平台库（PII + 6.5GB/日，两条理由都成立）。
//
// ⚠️ **本契约为 DRAFT。** reqlog 控制台的真实 HTTP API 形状尚未见到源码，
// 下列类型是按《请求审计系统实现报告》里描述的**字段集合**推导的，字段名与
// 路由都还没有对着实现核对过。因此：
//
//   - `fake` 是当前唯一走得通的模式，样本形状即本契约的可执行说明；
//   - `real` 只搭骨架（四道只读闸 + Basic Auth 经 CredentialRef 全部落地并
//     有测试），但**路由与响应解析留空**，调用即返回 not_supported——
//     编一组路由名再标 DRAFT，只会让接入那天的人拿到一串 bad_response，
//     误以为是自己配错了（见 client.go 顶部的接入清单）。
//
// 待核对项集中列在 contracts/connectors/reqlog.read.v1.md 的「等真实 API 核对
// 的字段清单」一节，那是本契约与真实实现之间唯一的差异台账。
//
// 铁律（与 sub2api / newapi 同源）：
//   - ReadClient 接口**只有读方法**（ADR-018 闸 4）；
//   - 每个结果都带 Snapshot（规格 §9.1：禁止裸数字）；
//   - 摘要**不含正文**——正文只经 RequestContent 单独授权读取。
package reqlog

import (
	"context"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey = "reqlog"
	// ContractVersion 是本只读契约的版本。破坏性变更必须发新版本。
	//
	// 仍是 "1" 而不是 "0"：DRAFT 说的是「还没对着真实 API 核对过」，
	// 不是「版本号可以随便动」。真实 API 到位后若形状对不上，改的是 v1 的
	// 内容（契约尚未冻结，允许），形状一旦冻结再变才发 v2。
	ContractVersion = "1"
)

// SourceSub2API / SourceNewAPI 是 reqlog 的 `source` 取值，也是平台侧
// service_type 的取值——两边**恰好同名**，所以路由参数 {platform} 可以直接
// 当 source 用。这不是巧合可以依赖的东西：真实 API 若用了别的写法，
// 映射要落在 ParseSource 这一个函数里，不许散到调用方去。
const (
	SourceSub2API = "sub2api"
	SourceNewAPI  = "newapi"
)

// Sources 是本连接器认得的全部来源。
//
// 平台上有七个被管平台，但只有这两个在 reqlog 的抄录范围内（它只代理
// NewAPI 与 Sub2API 的 /v1/*）。前端据此决定「请求」页签对哪些平台显示——
// 给 CPA 挂一个永远空的请求页签，等于说「这个平台没有请求」，而事实是
// 我们压根没抄它。
var Sources = []string{SourceNewAPI, SourceSub2API}

// ReadCapabilities 是本连接器的只读能力清单。
//
// 每一项都必须能被 registry.ParseCapability 解析且 IsWrite() 为 false——
// contracttest 会断言这一点，让「只读」成为可验证属性而非口头承诺。
var ReadCapabilities = []registry.Capability{
	"reqlog.service.version_read",
	"reqlog.requests.read",
	"reqlog.request.content_read",
	"reqlog.health.read",
}

// Snapshot 是每个读取结果都必须携带的新鲜度元数据（规格 §9.1）。
//
// 对 reqlog 而言这是「我们**什么时候**问的它」，而不是「数据什么时候产生」：
// 逐条请求各自带着自己的发生时刻（Summary.OccurredAt），而整页数据的新鲜度
// 取决于这次读取。两者都要，缺一个页面就得靠猜。
type Snapshot struct {
	// ObservedAt 是本次读取的观测时刻（UTC）。
	ObservedAt time.Time
	// Watermark 是上游的数据水位，用于判断是否读到了完整区间。
	Watermark string
	// IsPartial 表示本次只读到了部分数据（分页中断、上游截断、区间越过保留期）。
	IsPartial bool
	// Instance 是回答这次读取的 reqlog 实例标识（Fake 用 FakeInstance）。
	//
	// 它存在的唯一理由是让界面能回答「我现在看到的这段对话是真的吗」。
	// 别的连接器把这个问题交给指标的 source 字段（看板据此挂演示数据横幅），
	// 而请求详情不走指标表——它是实时读，没有 observation 可以承载来源。
	// 所以来源必须由读取结果自己带上，否则一屏编造的对话与一屏真实用户问答
	// 在页面上完全无法区分，而这一页恰恰是最不能分不清真假的一页。
	Instance string
}

// FakeInstance 是 Fake 的实例标识。
//
// 前端的演示数据横幅按**整串相等**匹配已知演示实例（web 侧
// lib/demoData.ts 的 DEFAULT_DEMO_SOURCES），所以这个串两边必须逐字一致——
// 改了这里就要改那里，漏改不会报错，只会让演示对话不再挂横幅。
const FakeInstance = "reqlog-fake"

// RetentionDays 是 reqlog 的保留窗口。
//
// 写成常量而不是让页面自己知道：一个「查不到 2026-07 的请求」的界面，
// 必须说得出是「那天没有请求」还是「已经过了保留期」。这两件事在屏幕上
// 长得一模一样，而处置完全不同（后者要去看别的证据源，前者不用）。
//
// ⚠️ 值取自实现报告的「30 天自动清理」，**未对真实部署核对**。
const RetentionDays = 30

// RequestLogSummary 是一条请求的元数据。
//
// **不含正文。** 这不是「暂时没放」，是契约层的隔离：列表页读的是本类型，
// 而正文要另一个 scope（request.content.read）与一条审计事件才拿得到。
// 把正文塞进这里，等于让「看列表」顺带获得「看全部对话内容」的能力——
// 交接文档 §9.4 把这两件事分成两级权限，分级就落在这个类型边界上。
type RequestLogSummary struct {
	// ID 是 reqlog 侧的记录标识，也是 RequestContent 的入参。
	//
	// 平台一律当**不透明字符串**：不解析、不排序、不构造。但有一条形状约束
	// ——**必须是 URL 路径安全的**（见 IsPathSafeID）。这条约束不是洁癖：
	// 记录在 reqlog 里按天分目录存放，真实标识很可能长成 "20260828/000123"，
	// 而带斜杠的 id 塞进 `/requests/{id}` 会被路由切成两段，表现为一个
	// 「明明存在却查不到」的 404——最难查的那一类。
	//
	// 所以映射责任在**连接器**：真实实现负责把上游标识变成路径安全的形态
	// （日期路径可以直接把 `/` 换成 `-`，实在不行退到 base64url），
	// 并在 RequestContent 里做反向映射。contracttest 会断言这条。
	ID string
	// Source 是 newapi / sub2api。
	Source string
	// OccurredAt 是请求发生时刻（UTC）。
	OccurredAt time.Time
	// Username 是 token_prefix 经 reqlog 的映射表解出的用户名；
	// 映射不到时为空串——**不用 token_prefix 顶替**，那会让界面上出现一列
	// 半是用户名半是令牌片段的东西，人分不出哪个是哪个。
	Username string
	// TokenPrefix 是令牌前缀。
	//
	// 它**不是凭据**：只有前若干位，不足以复原令牌，也不能用于调用上游。
	// 保留它的理由是映射失败时它是唯一的追查锚点（拿它去 NewAPI 后台反查）。
	// 即便如此，前端只在详情页显示，列表页优先显示用户名。
	TokenPrefix string
	Model       string
	// Status 是上游返回的 HTTP 状态码；0 表示 reqlog 没记到（连接中断）。
	Status int
	// DurationMS 是整次请求耗时。
	DurationMS int64
	// TTFBMS 是首字节时间；nil 表示未记录（非流式请求通常没有）。
	//
	// 用指针而不是 0/-1 哨兵：0 毫秒是一个**合法**的观测值（缓存命中），
	// 与「没测」必须分得开，否则趋势图上会多出一批不存在的零延迟请求。
	TTFBMS *int64
	// Token 计数。上游没给时为 0——reqlog 的索引行里这三个字段恒存在，
	// 所以这里不用指针；真实 API 若允许缺失，要改成指针并回来改这条注释。
	TokensIn    int64
	TokensOut   int64
	TokensCache int64
	// Stream 表示这是不是一次 SSE 流式请求。
	Stream bool
	// UpstreamRequestID 来自 X-Oneapi-Request-Id / X-Request-Id，
	// 是「站内日志 ↔ 明文抄录」之间唯一的跨系锚点。
	UpstreamRequestID string
	// ClientIP 是**已按 MaskIP 脱敏**的客户端地址（见该函数的口径说明）。
	//
	// 契约在这里就脱敏，而不是留给前端：一个原样返回明文 IP 的 Query 端点，
	// 只要有人写个脚本抓一遍列表就等于导出了全量用户 IP，而列表只要
	// request.read 这一级权限。脱敏放在最靠近数据源的一侧才拦得住。
	ClientIP string
}

// RequestLogPage 是一页请求元数据。
type RequestLogPage struct {
	Snapshot
	Items []RequestLogSummary
	// NextCursor 是下一页的游标；空串表示已经翻到底。
	//
	// 不透明字符串而不是 offset/序号：reqlog 的真实分页形态未知，
	// 而 offset 分页在「一直有新请求写入」的数据上必然重复与漏读。
	NextCursor string
	// RetentionDays 随每页返回，让界面能就地说清「只覆盖最近 N 天」。
	RetentionDays int
}

// MessageRole 是一条对话消息的角色。
//
// 不做成枚举去校验：上游模型协议随时会长出新角色（tool / developer /
// function），把没见过的角色判成非法，结果是详情页整条读不出来。
// 未知角色原样透传，由前端按「未知角色」渲染。
type MessageRole = string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Message 是分角色渲染所需的一条消息。
type Message struct {
	Role    MessageRole
	Content string
	// Truncated 表示 Content 已被截断（见 MaxMessageBytes）。
	Truncated bool
	// OriginalBytes 是截断前的字节数；未截断时等于 len(Content)。
	//
	// 必须给：界面要能说「只显示了 64 KiB / 共 3.2 MiB」。一个不声张的
	// 截断比不显示更危险——人会以为看到的就是全部，据此下判断。
	OriginalBytes int64
}

// RawPayload 是一侧的原始载荷。
//
// 分角色消息是**解析结果**，原始载荷是**事实**：两者都要，因为解析可能出错
// （上游协议变化、非 OpenAI 形状的请求体），而排查那种问题只能看原文。
type RawPayload struct {
	Body          string
	Truncated     bool
	OriginalBytes int64
	// ContentType 是 reqlog 记录的原始 Content-Type；空串表示未记录。
	ContentType string
}

// RequestLogContent 是一条请求的完整内容（高敏）。
//
// 三层一起给，因为详情页三层都要，而这条数据**每读一次就要写一条审计事件**
// ——分成三个端点等于把一次查看拆成三条审计记录，反而让「谁看了什么」
// 更难读。
type RequestLogContent struct {
	Snapshot
	// Summary 是同一条记录的元数据，随内容一并返回：详情页可以深链直达，
	// 不该为了页头那几个字段再往列表端点跑一趟。
	Summary RequestLogSummary
	// Messages 是从请求体解析出的分角色消息数组。
	//
	// 解析不出时为空切片而不是报错——原始载荷仍然可用，
	// 「解析失败」不该让人连原文都看不到。
	Messages []Message
	// MessagesParsed 区分「请求体里就没有消息」与「我们没解析出来」。
	MessagesParsed bool
	// FinalReply 是 SSE 事件流装配后的最终回复文本。
	//
	// 非流式请求也填这里（直接取响应体里的回复），让详情页只有一种渲染路径。
	FinalReply string
	// FinalReplyTruncated 表示 FinalReply 已被截断（见 MaxFinalReplyBytes）。
	FinalReplyTruncated bool
	// FinalReplyBytes 是截断前的字节数。
	FinalReplyBytes int64
	// RawRequest / RawResponse 是原始载荷。响应侧对流式请求是**完整 SSE
	// 事件流**，不是装配后的文本。
	RawRequest  RawPayload
	RawResponse RawPayload
}

// 截断规则（契约的一部分，前后端与 Fake 必须用同一组常量）。
//
// 为什么在**契约层**截断而不是让前端自己少渲染一点：这条数据的每一个字节
// 都要经过平台进程、平台日志的采样面与浏览器内存。一个 40 MB 的 SSE 流
// 完整取回来再让前端裁掉，平台已经把它读进内存、也已经承担了那份泄漏面，
// 前端裁不裁只影响卡不卡。真正的边界必须在取数这一侧。
//
// 阈值取值的依据：单条消息 64 KiB 够装一篇长文档的粘贴；最终回复 256 KiB
// 够装一次超长生成；原始载荷各 512 KiB 是排查用的，看不完整也能定位形状。
// 三者都远小于 reqlog 单条记录的实际上限（SSE 流可以到几十 MB）。
const (
	// MaxMessageBytes 是单条消息的字节上限。
	MaxMessageBytes = 64 << 10
	// MaxFinalReplyBytes 是装配后最终回复的字节上限。
	MaxFinalReplyBytes = 256 << 10
	// MaxRawPayloadBytes 是单侧原始载荷的字节上限。
	MaxRawPayloadBytes = 512 << 10
	// MaxMessages 是消息条数上限。
	//
	// 单独限条数而不是只限总字节：一次 5000 条的对话即便每条都很短，
	// 渲染出来也是一屏点不动的页面，而超过这个数之后的消息对「这个人问了
	// 什么」这个问题已经没有增量信息。
	MaxMessages = 200
)

// StatusFilter 是列表的状态过滤口径。
type StatusFilter string

const (
	// StatusAny 不过滤。
	StatusAny StatusFilter = ""
	// StatusSuccess 只要 2xx。
	StatusSuccess StatusFilter = "success"
	// StatusError 只要非 2xx（含 reqlog 没记到状态码的连接中断，Status==0）。
	//
	// 把 Status==0 归进 error 是有意的：那是一次**没成功**的请求，
	// 归进「成功」会让失败率显得比实际好看，而归到第三类又会让界面上
	// 多一个没人看得懂的分类。
	StatusError StatusFilter = "error"
)

// DefaultListLimit / MaxListLimit 是列表分页的边界。
//
// 上限 200 而不是 sub2api 那种无所谓的大数：这一页的每行都对应一条含明文
// 正文的记录，页面越大，一次误操作能拉走的元数据面越大。
const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

// ListFilter 是列表查询的过滤条件。
//
// 全部字段都是可选的**收窄**条件，没有任何一个字段能扩大可见范围——
// 可见范围由 Source（路由参数）与调用者的权限决定，过滤器只在其内部做减法。
type ListFilter struct {
	// Source 必填：newapi / sub2api。空值由实现判为 bad_response，
	// 而不是「查全部」——一个漏填了 source 的查询返回两个平台的混合结果，
	// 是最容易被误读成「这个平台请求量翻倍了」的那种错。
	Source string
	// Username 精确匹配（不是模糊搜索）。
	//
	// 模糊匹配在这里是**扩大**可见范围的操作：输入 "a" 就能扫出全部
	// 含 a 的用户名及其请求元数据。要模糊搜索得先有一个想清楚的授权模型。
	Username string
	// Model 精确匹配。
	Model  string
	Status StatusFilter
	// Since / Until 是请求发生时刻的闭开区间 [Since, Until)；零值表示不设界。
	Since time.Time
	Until time.Time
	// Limit 为 0 时用 DefaultListLimit，超过 MaxListLimit 夹到上限。
	Limit int
	// Cursor 是上一页返回的 NextCursor；空串表示取第一页。
	Cursor string
}

// ReadClient 是 reqlog 的只读接入契约。
//
// **只有读方法。** reqlog 本身也没有写接口（它是个抄录器），
// 但这条纪律与上游有没有写接口无关（ADR-018 闸 4）。
type ReadClient interface {
	// Version 探测 reqlog 版本并给出是否在兼容矩阵内。
	Version(ctx context.Context) (connector.VersionInfo, error)

	// Health 返回运营健康状态；失败时用结构化 ErrorKind，不透传上游原始错误。
	Health(ctx context.Context) (connector.HealthResult, error)

	// Capabilities 返回本连接当前实际可用的能力。
	Capabilities(ctx context.Context) ([]registry.Capability, error)

	// ListRequests 按过滤条件分页读取请求**元数据**（不含正文）。
	ListRequests(ctx context.Context, filter ListFilter) (RequestLogPage, error)

	// RequestContent 读取单条请求的完整内容（高敏）。
	//
	// source 与 id 都要：id 的形状未知，可能在两个 source 之间不唯一，
	// 而「拿 sub2api 的 id 去读 newapi 的内容」必须是一次明确的失败，
	// 不能变成一次跨来源的静默命中。
	//
	// 找不到时返回可被 errors.Is(err, ErrNotFound) 认出的错误。
	RequestContent(ctx context.Context, source, id string) (RequestLogContent, error)
}

// ErrNotFound 表示 reqlog 里没有这条记录。
//
// 与「读不到」分开：过了 30 天保留期、id 拼错、id 属于另一个 source，
// 三者都是 not found，处置都是「别再查了」；而 unavailable 是「过会儿再试」。
// 把它们混成一类，界面就只能给出一句放之四海而皆准的废话。
//
// **判据是 errors.Is(err, ErrNotFound)，不是 connector.KindOf(err)。**
// 调用方必须先判它，再退回按 Kind 分类。理由：connector.ErrorKind 是全体
// 连接器共用的枚举，里面没有「记录不存在」这一档，而为本连接器往那个共用
// 枚举里加一项，等于让所有连接器的错误映射表都跟着改。
//
// 这类错误的 Kind 取 not_supported：它与 not_supported 在**调用方的处置上
// 完全同构**——重试永远不会变好、不该告警、要告诉人「换个查法」。选一个
// 处置相同的既有分类，比新增一档更省，也不会让别的连接器的映射表变长。
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "reqlog: record not found" }
