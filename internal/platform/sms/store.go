package sms

import (
	"context"
	"time"
)

// Store 是领域层需要的持久化能力。
//
// 定成接口而不是直接依赖具体实现：本包最要紧的逻辑（七态推进、未决防重、
// unknown 收敛）必须能在没有数据库的情况下被完整测到——那些分支在真实环境里
// 既难复现又昂贵（每复现一次不确定态，就是真买一次号）。
type Store interface {
	// PrepareOperation 落一条 prepared 台账。
	//
	// 同 requestHash 已有未决的一笔时返回 ErrPendingDuplicate——**这是
	// 防重的核心**：换一个 operation UUID 重发同样的请求要被挡住。
	PrepareOperation(ctx context.Context, op Operation) error
	// MarkSubmitted 把 prepared 推进到 submitted。
	//
	// 单独一步而不是和 Prepare 合并：进程在这两点之间崩溃的处置完全不同
	// ——prepared 崩了可以直接判失败（还没发出去），submitted 崩了只能
	// 落 unknown。合并就分不出来了。
	MarkSubmitted(ctx context.Context, operationID string, at time.Time) error
	// ResolveOperation 写终局状态。
	ResolveOperation(ctx context.Context, op Operation) error
	// GetOperation 读一笔。
	GetOperation(ctx context.Context, operationID string) (Operation, error)
	// ListOperations 列台账，新的在前。
	ListOperations(ctx context.Context, provider string, limit int) ([]Operation, error)
	// RecoverStaleOperations 把进程崩溃遗留的未决操作推进到该去的地方。
	//
	// prepared → failed（还没发出去，钱没花）；
	// submitted → unknown（发出去了但不知道结果）。
	// 返回被改动的条数，供启动日志说明「恢复了几笔」。
	RecoverStaleOperations(ctx context.Context, at time.Time) (int, error)

	// UpsertResource 按 (provider, external_id) 落号码。
	UpsertResource(ctx context.Context, r Resource) (string, error)
	// GetResource 读一个号码，**含完整号码与 token**。
	//
	// 这是内部权威对象，只给取码与生命周期动作用；对外 DTO 走
	// ListResources，那一份不含 token，号码是否可见由权限决定。
	GetResource(ctx context.Context, resourceID string) (Resource, error)
	ListResources(ctx context.Context, provider string, limit int) ([]Resource, error)
	// TouchResourceCodeAt 记「最后一次成功取到码」的时间。
	TouchResourceCodeAt(ctx context.Context, resourceID string, at time.Time) error
	// SetResourceState 写统一状态（我们自己的事实，不改上游原话 status）。
	SetResourceState(ctx context.Context, resourceID string, state NumberState, at time.Time) error
	// ListResourcesByOperation 列出某笔操作买下的号（要号回放用）。
	ListResourcesByOperation(ctx context.Context, operationID string) ([]Resource, error)

	// UpsertOrder 落订单（62）。返回本地 UUID。
	UpsertOrder(ctx context.Context, o Order) (string, error)

	// RecordCode 落一条验证码。已存在同一条时第二个返回值为 false。
	//
	// 去重是必要的：轮询会反复读到同一条短信，而每次都推一遍企业微信
	// 会让人在群里看到十条一样的码，然后开始忽略它们。
	RecordCode(ctx context.Context, c Code) (Code, bool, error)
	ListCodes(ctx context.Context, resourceID string, limit int) ([]Code, error)

	// ProviderStatus 读某家的验证事实。
	ProviderStatus(ctx context.Context, provider string) (ProviderStatus, error)
	ListProviderStatus(ctx context.Context) ([]ProviderStatus, error)
	// SaveProviderStatus 写连接测试的结果。**不改 enabled**——那是另一个动作。
	SaveProviderStatus(ctx context.Context, s ProviderStatus) error
	// SetProviderEnabled 开关一家供应商。
	SetProviderEnabled(ctx context.Context, provider string, enabled bool, at time.Time) error

	// 邮箱接码（Hero，迁移 000040）。按 (provider, external_id) 落，保持本地 UUID。
	UpsertEmail(ctx context.Context, e Email) (string, error)
	GetEmail(ctx context.Context, emailID string) (Email, error)
	ListEmails(ctx context.Context, limit int) ([]Email, error)

	// 余额快照（XM-SMS2 #7）。**追加**而不是覆盖：阶段 3 要用相邻两次的差
	// 与成本事件对账，覆盖写就没有差可算。
	SaveBalanceSnapshot(ctx context.Context, snap BalanceSnapshot) (string, error)
	// LatestBalanceSnapshots 每家最新一条。
	LatestBalanceSnapshots(ctx context.Context) ([]BalanceSnapshot, error)
	// ListRecentBalanceSnapshots 某一家最近的几条，新的在前（对账要相邻两条）。
	ListRecentBalanceSnapshots(ctx context.Context, provider string, limit int) ([]BalanceSnapshot, error)

	// 成本事件（XM-SMS3 #1）。按 (操作, 主体) 幂等：同一笔操作重放不会记两次账。
	AppendCostEvents(ctx context.Context, events []CostEvent) (int, error)
	ListCostEvents(ctx context.Context, provider string, limit int) ([]CostEvent, error)
	// SumCostEventsByCurrency 汇总 (from, to] 内的成本，**按币种分组**：跨币种
	// 相加得到的数字看起来像个金额，其实什么都不是。
	SumCostEventsByCurrency(ctx context.Context, provider string, from, to time.Time) ([]CostSummary, error)

	// 告警（XM-SMS2 #8）。阈值按家配（币种各自不同，不折算）；事件按指纹去重，
	// 条件消失由 ResolveAlertEventsNotIn 自动收敛。
	SaveBalanceThreshold(ctx context.Context, t BalanceThreshold) error
	RemoveBalanceThreshold(ctx context.Context, provider string) error
	ListBalanceThresholds(ctx context.Context) ([]BalanceThreshold, error)
	UpsertAlertEvent(ctx context.Context, ev AlertEvent) (string, error)
	ResolveAlertEventsNotIn(ctx context.Context, fingerprints []string, at time.Time) (int, error)
	ListOpenAlertEvents(ctx context.Context) ([]AlertEvent, error)
	// ListStaleUnknownOperations 列出 updated_at 早于 before 的待核对 unknown。
	ListStaleUnknownOperations(ctx context.Context, before time.Time) ([]Operation, error)
	// ListExpiringRentals 列出 (from, until] 内到期、还在等码的**租用**号。
	ListExpiringRentals(ctx context.Context, from, until time.Time) ([]Resource, error)

	// 路由规则（XM-SMS2 #5）。UpsertRoutingRule 按（环境, 服务, 国家）覆盖；
	// RemoveRoutingRule 不存在时回 ErrRoutingRuleNotFound。
	UpsertRoutingRule(ctx context.Context, r RoutingRule) (string, error)
	RemoveRoutingRule(ctx context.Context, ruleID string) error
	ListRoutingRules(ctx context.Context) ([]RoutingRule, error)
}

// Order 是一笔订单（主要是 62）。
type Order struct {
	ID              string
	Provider        string
	ProviderOrderID string
	GoodsID         string
	Quantity        int
	AmountText      string
	Status          string
}

// ProviderStatus 是某家供应商的验证事实。
//
// 记两件事：**运营开没开这家**（enabled），以及**最近一次连接测试的结果**。
// 密钥不在这里——它在 SecretProvider，库里不存任何密文（宪法 7）。
type ProviderStatus struct {
	Provider string
	// Enabled 是**运营在后台开的开关**，不是部署配置。
	//
	// 新装环境默认关：一个「装好就自动启用」的供应商，会在还没人填密钥、
	// 也没想清楚要不要用它的时候就出现在买号页的可选项里。
	Enabled bool
	// VerifiedAt 为零值 = 从未验证成功。购买前必须非零。
	VerifiedAt time.Time
	// ClientIP 是供应商观察到的我方出口 IP。上游若做 IP 白名单，
	// 这个值对不上就是全部 403 的原因，而那种失败从错误码上看只是「没权限」。
	ClientIP  string
	LastError string
	UpdatedAt time.Time
}

// Verified 说明这家做过成功的连接测试。
func (s ProviderStatus) Verified() bool { return !s.VerifiedAt.IsZero() }

// Usable 说明这家现在能不能用来花钱。
//
// **两个条件都要**：开着（运营的意愿）且验证过（凭据确实能用）。
// 只看其中一个都会漏——开着但没验证的会在买号那一刻才报鉴权错误，
// 验证过但关着的说明运营刻意停了它。
func (s ProviderStatus) Usable() bool { return s.Enabled && s.Verified() }
