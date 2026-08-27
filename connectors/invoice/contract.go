// Package invoice 定义开票系统的只读接入契约（CR-0002、ADR-018 四道只读闸）。
//
// # 状态：草案（DRAFT），未冻结
//
// 字段清单、状态集合、水位语义、金额的 wire 层编码**均待 CR-0002 双方确认**
// （开票线 issue #49，宪法 20 条：跨线契约须双方确认后方可冻结）。
// 确认之前：
//   - XM-0029（真实客户端与看板卡片）**不得依赖**本契约的具体字段形状；
//   - 本包的合并**不等于跨线批准**，开票线随时可能推翻其中任何一项；
//   - 本包只服务一个目的：让上层开发不被尚未落地的端点与凭据阻塞。
//
// 冻结之后上面的状态标题会改为「冻结」，届时破坏性变更须发 v2。
//
// 本包**只声明契约与提供 Fake**，不含任何真实网络实现——真实实现是 XM-0029。
// 任何实现（Fake 或真实）都必须通过 contracttest 套件才算合规。
//
// 铁律：
//   - ReadClient 接口**只有读方法**。开票的写操作（审核、开具、退票）永远
//     留在开票系统自己那边，平台不代劳，因此本包不存在也不会有 WriteClient
//     （ADR-018 闸 4、CR-0002「平台永不直写开票系统业务表」）；
//   - 每个返回值都带 ObservedAt 与 Watermark（规格 §9.1：禁止裸数字）；
//   - 金额一律整数最小货币单位 + Currency，禁止 float（规格 §5.9）；
//   - **契约里不存在任何 PII 字段**：抬头/税号/银行账号/地址/电话/邮箱在
//     类型层面就没有位置。CR-0002 勘察发现开票系统现有 admin 端点会内嵌
//     领域结构吐出全量 PII，所以只读投影必须是手写白名单；平台这一侧把
//     「PII 不进平台」做成 contracttest 里的反射断言，而不是评审时的口头提醒。
package invoice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey = "invoice"
	// ContractVersion 是本只读契约的版本。破坏性变更必须发新版本。
	ContractVersion = "1"
)

// ReadCapabilities 是开票系统只读接入的能力清单（CR-0002 建议实现 §1~§3、§6）。
//
// 每一项都必须能被 registry.ParseCapability 解析，且 IsWrite() 为 false——
// contracttest 会断言这一点，让「只读」成为可验证的属性而非口头承诺。
//
// 清单刻意保持最小：只覆盖运营看板真正要展示的东西（开票量、金额、失败与
// 待处理）。多要一项能力就多一份被滥用的面，能力清单不是许愿单。
var ReadCapabilities = []registry.Capability{
	"invoice.service.version_read",
	"invoice.requests.read",
	"invoice.summary.daily_read",
	"invoice.health.read",
}

// Snapshot 是每个读取结果都必须携带的新鲜度元数据（规格 §9.1）。
//
// 没有「只返回值不返回快照信息」的数据结构——裸数字在类型层面就不存在。
//
// **为什么不复用 sub2api.Snapshot**：两个契约独立演进，各自钉住各自上游的
// 版本与语义。共享一个结构体会让任意一边的破坏性变更（比如给水位换个类型、
// 把 IsPartial 拆成两个标志）无声地传染到另一边，然后在毫不相干的 Connector
// 上炸出编译错误或——更糟——语义漂移。字段重复几行是刻意付出的代价：
// 契约之间的耦合应当是零，哪怕代价是抄一遍。
type Snapshot struct {
	// ObservedAt 是上游数据的观测时刻，不是本地接收时刻。
	ObservedAt time.Time
	// Watermark 是上游的数据水位，用于判断是否读到了完整区间。
	// CR-0002 勘察：开票系统可用 source_economic_stream_watermarks 的
	// min(watermark_at)，开票记录自身可用 max(updated_at)。
	Watermark string
	// IsPartial 表示本次只读到了部分数据（如分页中断、上游降级）。
	IsPartial bool
}

// CurrencyCNY 是当前唯一在用的币种。
//
// 字段仍然保留：金额没有币种就是无意义的数字，而「现在只有一种」不是
// 「以后只有一种」。把币种写死进代码，将来多币种时要改的是全链路而不是一处。
const CurrencyCNY = "CNY"

// BusinessDayLayout 是业务日字符串的格式。
const BusinessDayLayout = "2006-01-02"

// BusinessDayTimeZone 记录业务日的切分时区。
//
// **平台侧不做时区换算**：业务日由开票系统按 Asia/Shanghai 切分后给出
// （CR-0002 勘察：开票系统全部 TIMESTAMPTZ、显示时区固定 Asia/Shanghai），
// 平台只按 BusinessDayLayout 校验格式并原样透传。这里刻意不调用
// time.LoadLocation：平台库内一律 UTC（规格 §5.9），凭空造一个「上海午夜」
// 的时刻只会制造两套口径。本常量是给文档与错误信息用的。
const BusinessDayTimeZone = "Asia/Shanghai"

// 契约层的取值错误。调用方用 errors.Is 判定，实现方负责包装成 connector.Error。
var (
	// ErrInvalidStatus：开票状态不在 9 项枚举内。
	ErrInvalidStatus = errors.New("invalid invoice status")
	// ErrInvalidDay：业务日不符合 BusinessDayLayout。
	ErrInvalidDay = errors.New("invalid business day")
	// ErrInvalidRange：时间范围自相矛盾（From 晚于 To）。
	ErrInvalidRange = errors.New("invalid time range")
	// ErrInvalidCursor：分页游标无法解析或已失效。
	ErrInvalidCursor = errors.New("invalid page cursor")
)

// Status 是开票申请的状态。
//
// 9 项枚举来自 CR-0002 勘察结论（开票系统 migrations/0001_init.sql:124-127 与
// internal/domain/types.go:59-67，DB 与 Go 两侧一致）。平台**不发明状态**：
// 这里多一项少一项都是与上游脱节，看板上就会出现永远为 0 或永远漏计的格子。
type Status string

const (
	StatusPendingReview          Status = "pending_review"           // 待审核
	StatusNeedsChanges           Status = "needs_changes"            // 需修改后重提
	StatusApproved               Status = "approved"                 // 审核通过，待开具
	StatusRejected               Status = "rejected"                 // 审核驳回
	StatusUserCancelled          Status = "user_cancelled"           // 用户主动撤销
	StatusManualIssuing          Status = "manual_issuing"           // 人工开具中
	StatusIssuedAwaitingDocument Status = "issued_awaiting_document" // 已开具，待回传票据
	StatusIssued                 Status = "issued"                   // 已开具并完成
	StatusRefundAttention        Status = "refund_attention"         // 涉退款，需人工关注
)

// AllStatuses 是全部 9 项状态，顺序即上面的声明顺序。
func AllStatuses() []Status {
	// 返回副本：调用方（尤其是测试）拿去排序或截断不该影响别人。
	return []Status{
		StatusPendingReview, StatusNeedsChanges, StatusApproved,
		StatusRejected, StatusUserCancelled, StatusManualIssuing,
		StatusIssuedAwaitingDocument, StatusIssued, StatusRefundAttention,
	}
}

// ParseStatus 解析开票状态；未知状态一律拒绝。
//
// 拒绝而不是归入「其他」：上游加了新状态却没走 CR，平台这边会立刻在契约
// 测试与真实调用里炸出来，而不是把新状态悄悄吞掉，让日汇总的分子分母
// 长期对不上还没人发现。
func ParseStatus(s string) (Status, error) {
	for _, known := range AllStatuses() {
		if Status(s) == known {
			return known, nil
		}
	}
	return "", fmt.Errorf("invoice status %q: %w", s, ErrInvalidStatus)
}

func (s Status) String() string { return string(s) }

// ValidateBusinessDay 校验业务日格式（不做时区换算，理由见 BusinessDayTimeZone）。
func ValidateBusinessDay(day string) error {
	if _, err := time.Parse(BusinessDayLayout, day); err != nil {
		return fmt.Errorf("business day %q 须形如 %s（%s 业务日）: %w",
			day, BusinessDayLayout, BusinessDayTimeZone, ErrInvalidDay)
	}
	return nil
}

// InvoiceRequest 是一条开票申请的**只读投影**（CR-0002 建议实现 §1 的字段白名单）。
//
// 字段清单是白名单而非「领域结构去掉几个字段」：
// id / request_no / status / amount_minor / currency / source_type /
// submitted_at / updated_at。
//
// **这里没有、也不会有 profile / 抬头 / 税号 / 银行账号 / 地址 / 电话 / 邮箱。**
// CR-0002 勘察发现开票系统现有 admin_dto.go:10 直接内嵌 domain.InvoiceRequest，
// 会吐出全量 PII——平台侧的防线不是「记得别读那些字段」，而是让那些字段在
// 平台的类型里根本不存在：读不到就泄不了。contracttest 用反射把这条断言成
// 可执行的测试，任何人往这个结构体上加 Email 字段都会当场红。
type InvoiceRequest struct {
	Snapshot

	// ID 是开票申请的主键（上游生成，平台不解释其格式）。
	ID string
	// RequestNo 是给人看的申请单号。
	RequestNo string
	// Status 是 9 项枚举之一。
	Status Status
	// AmountMinor 是整数最小货币单位（分），禁止 float（规格 §5.9）。
	// CR-0002 勘察：上游 amount_minor BIGINT，金额纪律已合规，无需整改。
	//
	// ⚠️ **wire 层编码待 CR-0002 确认**：本字段在 Go 侧固定 int64，但 HTTP
	// 响应里它可能是 JSON number，也可能是十进制字符串——超过 2^53 的整数在
	// JavaScript 的 JSON.parse 下会静默丢精度，所以不少系统会选字符串。
	// 这不影响契约结构体（int64 是平台侧的既定纪律），只影响 XM-0029 的解码器：
	// 那一层要按 CR 最终确认的编码来写，**不要**猜。
	AmountMinor int64
	// Currency 是 ISO 4217 三字母币种，当前固定 CNY。
	Currency string
	// SourceType 是上游标注的来源类型。平台**不解释其语义**，原样透传给
	// 看板做分组——上游改了取值集合不该逼平台跟着发版。
	SourceType string
	// SubmittedAt 是提交时刻，UTC（规格 §5.9：时间库内一律 UTC）。
	SubmittedAt time.Time
	// UpdatedAt 是最后更新时刻，UTC。它同时是本条记录的水位来源。
	UpdatedAt time.Time
}

// RequestPage 是一页开票申请。
type RequestPage struct {
	// Snapshot 是**整页**的新鲜度。
	//
	// 每个 Item 自己也带 Snapshot，为什么页面还要一份：空页。
	// 「这一天 0 张发票」与「上游降级所以读到 0 张」在看板上长得一模一样，
	// 而 Items 为空时逐项快照一个都没有，新鲜度就无从判断——正是规格 §9.1
	// 要堵的裸数字。页级快照让空页也能回答「这个 0 是什么时候的 0」。
	Snapshot

	Items []InvoiceRequest

	// NextCursor 为空表示没有更多数据。
	//
	// 游标对调用方**不透明**：不要解析它、不要自己拼一个。契约只保证把它
	// 原样回传能拿到紧接着的下一页。实现应当用 keyset（按最后一条记录定位）
	// 而非 offset——offset 在上游并发写入时会漏记录或重复记录，而「不重不漏」
	// 是 contracttest 会断言的硬要求。
	NextCursor string
}

// DailySummary 是某个业务日的开票汇总（CR-0002 建议实现 §3）。
type DailySummary struct {
	Snapshot

	// Day 是业务日，格式 2006-01-02，按 Asia/Shanghai 切分（见 BusinessDayTimeZone）。
	Day string
	// Count 是该业务日的开票申请总数。
	Count int64
	// TotalAmountMinor 是总金额，整数最小货币单位。
	// wire 层编码同 InvoiceRequest.AmountMinor，待 CR-0002 确认。
	TotalAmountMinor int64
	// Currency 是币种，当前固定 CNY。
	Currency string

	// FailedCount 是「失败」数。
	//
	// ⚠️ **口径由开票系统冻结，平台不自行解释状态集合。**
	// 哪些状态计入「失败」是开票业务的定义，不是平台能替它下的判断——
	// 平台这边只负责把上游给出的数字接进看板。Fake 当前采用
	// rejected + refund_attention **仅为演示**，好让上层卡片有个非零的数
	// 可渲染；它不构成任何口径主张，CR-0002 确认后以开票线的定义为准。
	// 在那之前，任何依赖此字段的告警阈值都不应上线。
	FailedCount int64

	// PendingCount 是「待处理」数。
	//
	// ⚠️ **口径由开票系统冻结，平台不自行解释状态集合。**
	// Fake 当前采用 pending_review + needs_changes + manual_issuing +
	// issued_awaiting_document **仅为演示**，同样不构成口径主张。
	// （举个争议点：issued_awaiting_document 已经开完票只差回传票据，
	// 算不算「待处理」要看运营是否需要盯它——这该由开票线回答。）
	PendingCount int64
}

// ListQuery 是开票申请分页查询的参数。
type ListQuery struct {
	// From / To 是提交时间的闭区间过滤，UTC。零值表示该端不设限。
	From time.Time
	To   time.Time

	// Status 是状态过滤，空表示不过滤。
	//
	// 用 string 而不是 Status：「不过滤」需要一个零值，而 Status 的零值若是
	// 空串就等于在枚举里凿了个非法洞。类型的合法取值集合不该为了省一次转换
	// 而被稀释——非空时由 Validate 走 ParseStatus 校验。
	Status string

	// Limit 是本页最大条数。<=0 用 DefaultPageLimit，超过 MaxPageLimit 会被截断。
	Limit int32

	// Cursor 是上一页返回的 NextCursor，空表示从头开始。
	Cursor string
}

const (
	// DefaultPageLimit 是未指定 Limit 时的每页条数。
	DefaultPageLimit int32 = 50
	// MaxPageLimit 是每页条数上限。
	//
	// 截断而不是报错：上层想「尽量多拿点」时不该因为多写了个零就整个查询失败，
	// 但也不能让一次请求把上游拖垮。上限是保护上游的，不是考调用方记性的。
	MaxPageLimit int32 = 200
)

// EffectiveLimit 返回实际生效的每页条数。
func (q ListQuery) EffectiveLimit() int32 {
	if q.Limit <= 0 {
		return DefaultPageLimit
	}
	if q.Limit > MaxPageLimit {
		return MaxPageLimit
	}
	return q.Limit
}

// Validate 校验查询参数。
//
// 放在契约层而不是各实现里：Fake 与 XM-0029 的真实客户端必须用同一条判据，
// 否则「Fake 上能跑、真环境上报错」这类问题会一路漏到联调。
func (q ListQuery) Validate() error {
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return fmt.Errorf("from=%s 晚于 to=%s: %w",
			q.From.UTC().Format(time.RFC3339), q.To.UTC().Format(time.RFC3339),
			ErrInvalidRange)
	}
	if q.Status != "" {
		if _, err := ParseStatus(q.Status); err != nil {
			return err
		}
	}
	return nil
}

// ReadClient 是开票系统的只读接入契约。
//
// **只有读方法。** 开票的审核/开具/退票永远由开票系统自己完成，平台不代劳，
// 因此这里不会有配对的 WriteClient——ADR-018 闸 4 在这里体现为接口形状本身。
type ReadClient interface {
	// Version 探测上游版本并给出是否在兼容矩阵内。
	// 不支持的版本返回 Supported=false 而非报错：是否 Fail Closed 由调用方
	// 按场景决定（读取可降级）。
	//
	// CR-0002 勘察：开票系统 /version 尚不存在（最小缺口，建议 Codex 首件做）。
	Version(ctx context.Context) (connector.VersionInfo, error)

	// Health 返回运营健康状态；失败时用结构化 ErrorKind，不透传上游原始错误。
	Health(ctx context.Context) (connector.HealthResult, error)

	// Capabilities 返回本连接当前实际可用的能力（可能因上游版本而少于 ReadCapabilities）。
	Capabilities(ctx context.Context) ([]registry.Capability, error)

	// ListRequests 分页读取开票申请的只读投影。
	//
	// 非法参数（From>To、未知状态、坏游标）必须被拒绝而不是静默忽略：
	// 静默忽略过滤条件会让调用方拿到一份自以为过滤过的全量数据。
	ListRequests(ctx context.Context, q ListQuery) (RequestPage, error)

	// DailySummary 读取某业务日（2006-01-02）的开票汇总。
	DailySummary(ctx context.Context, day string) (DailySummary, error)
}

// 指标键（写进 ops.metric_observation 的 metric_key）。
const (
	// MetricRequestsDaily 是当日开票单量：总数 / 失败 / 待处理。
	MetricRequestsDaily = "invoice.requests.daily"
	// MetricAmountDaily 是当日开票金额。
	MetricAmountDaily = "invoice.amount.daily"
)

// DefaultStalenessThresholdSeconds 是这批指标的默认新鲜度阈值。
//
// 1 小时，比 Sub2API 的 30 分钟宽一倍：开票是人工审核驱动的低频业务，
// 一小时没有新数据是常态而不是故障。阈值定得比业务节奏还紧，看板就会
// 长期挂着橙色，然后所有人学会无视它——那比没有阈值更糟。
//
// 导出它是给 XM-0029 的看板卡片用的：前端要显示「多久算旧」，判据必须
// 与产出指标时用的是同一个常量，而不是在前端再写一遍 3600。
const DefaultStalenessThresholdSeconds int32 = 3600

func snapshotToObservation(
	metricKey, instanceID, environment string, snap Snapshot, now time.Time,
	value map[string]any,
) ops.Observation {
	observedAt := snap.ObservedAt.UTC()
	o := ops.Observation{
		MetricKey:                 metricKey,
		Source:                    instanceID,
		Environment:               environment,
		SyncedAt:                  now.UTC(),
		Watermark:                 snap.Watermark,
		Status:                    ops.SyncOK,
		IsPartial:                 snap.IsPartial,
		StalenessThresholdSeconds: DefaultStalenessThresholdSeconds,
		Value:                     value,
	}
	// 零值 ObservedAt 表示上游没给观测时刻——保持为空而不是用 now 冒充，
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）
	if !snap.ObservedAt.IsZero() {
		o.ObservedAt = &observedAt
		o.LastSuccess = &observedAt
	}
	return o
}

// ToObservations 把日汇总转成新鲜度模型，接进看板（规格 §9.1）。
//
// 这是 Connector 与看板之间的唯一接缝：Connector 不直接写库，
// 由调用方（XM-0029 的 Worker 任务）拿这些 Observation 去 Upsert。
//
// 只从 DailySummary 出指标、不从 ListRequests 出：逐条开票申请是明细，
// 明细进详情页而不是进指标表——把 N 条记录塞进一个指标值里，既撑爆
// ops.metric_observation 的 JSON 又让新鲜度失去意义。
func ToObservations(
	now time.Time, instanceID, environment string, summary DailySummary,
) []ops.Observation {
	return []ops.Observation{
		snapshotToObservation(MetricRequestsDaily, instanceID, environment, summary.Snapshot, now,
			map[string]any{
				"day":           summary.Day,
				"count":         summary.Count,
				"failed_count":  summary.FailedCount,
				"pending_count": summary.PendingCount,
			}),
		snapshotToObservation(MetricAmountDaily, instanceID, environment, summary.Snapshot, now,
			map[string]any{
				"day":                summary.Day,
				"amount_minor_units": summary.TotalAmountMinor,
				"currency":           summary.Currency,
			}),
	}
}
