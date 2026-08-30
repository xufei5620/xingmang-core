package ops

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// State 是新鲜度的主状态。一个徽章只能显示一个状态，因此必须定义优先级。
type State string

const (
	// StateUninitialized：从未成功采集过，**且当前没有失败**——也就是「这条
	// 指标还没接上」。它是中性状态，不代表出问题。带着错误码的记录一律不是
	// 它（见 Freshness 的优先级说明）。
	StateUninitialized State = "uninitialized"
	StateFailed        State = "failed"  // 最近一次同步失败（含第一次就失败）
	StateStale         State = "stale"   // 超过新鲜度阈值
	StatePartial       State = "partial" // 数据不完整
	StateFresh         State = "fresh"   // 新鲜且完整
)

// SyncStatus 是最近一次同步的结果。
type SyncStatus string

const (
	SyncOK     SyncStatus = "ok"
	SyncFailed SyncStatus = "failed"
)

var (
	// ErrInvalidStatus：同步状态非法。
	ErrInvalidStatus = errors.New("invalid sync status")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("invalid format")
	// ErrInconsistent：字段之间自相矛盾。
	ErrInconsistent = errors.New("inconsistent fields")
)

// metricKeyPattern 允许点分与下划线，便于表达 sub2api.revenue.daily 这类键。
var metricKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// ParseSyncStatus 解析同步状态。
func ParseSyncStatus(s string) (SyncStatus, error) {
	switch SyncStatus(s) {
	case SyncOK, SyncFailed:
		return SyncStatus(s), nil
	default:
		return "", fmt.Errorf("sync status %q: %w", s, ErrInvalidStatus)
	}
}

// Observation 是一个指标的最近一次观测（规格 §9.1 的九个字段）。
type Observation struct {
	ID          uuid.UUID
	MetricKey   string
	Source      string
	Environment string

	// ObservedAt 为 nil 表示从未成功采集过。
	ObservedAt *time.Time
	// SyncedAt 是最近一次尝试同步的时刻（无论成败）。
	SyncedAt  time.Time
	Watermark string

	Status        SyncStatus
	IsPartial     bool
	LastSuccess   *time.Time
	LastErrorCode string

	StalenessThresholdSeconds int32
	Value                     map[string]any

	UpdatedAt time.Time

	// RollupPolicyVersion and ExpectedIntervalSeconds are immutable metadata
	// copied into each raw sample at write time.  A zero policy version is kept
	// as a backwards-compatible signal for callers predating DS1; Store fills
	// the v1 migration default while every periodic writer supplies it
	// explicitly.
	RollupPolicyVersion     int16
	ExpectedIntervalSeconds *int32
}

// Freshness 是给前端的新鲜度信息：一个主状态 + 完整原始字段。
//
// 只给主状态会丢信息（比如「又旧又不完整」只能显示一个）；只给原始字段则
// 每个前端都要自己实现优先级判断，迟早不一致。两者都给。
type Freshness struct {
	State State
	// StalenessSeconds 为 nil 表示无法计算（从未采集）。
	// 规格 §9.1：该值不持久化，每次查询按 now - observed_at 动态算。
	StalenessSeconds *int64
	IsPartial        bool
	Source           string
	ObservedAt       *time.Time
	LastSuccess      *time.Time
	LastErrorCode    string
	ThresholdSeconds int32
}

// Freshness 依据给定时刻判定新鲜度。
//
// 优先级：失败 > 未初始化 > 延迟 > 部分 > 新鲜。
// 理由：从运维视角，「同步失败了」比「数据旧了」更需要立即动作；
// 「数据旧了」又比「数据不全」更严重——不全但新的数据至少反映当前。
// 被主状态盖住的信息仍在 IsPartial / LastErrorCode 等字段里，不丢。
//
// 失败排在未初始化**前面**（XM-0031 修正，回归 Codex 冷审 PR #43 head
// `ba8e275` 第 3 条）：「从未采集过」与「第一次采集就失败了」是两个不同的
// 事实，后者必须可见。第一次同步失败时任务写的是 SyncFailed +
// last_error_code，但没有任何历史值可留，于是 ObservedAt / LastSuccess 都是
// nil；旧的优先级会在检查 failed 之前先返回 uninitialized，把一个**正在发生
// 的故障**（凭据错误、连接配置缺项、上游拒绝）显示成中性的「尚未接入」。
// 前端把 uninitialized 定为 neutral 徽章，于是错误码只剩 hover title 能看见。
//
// 判据是 LastErrorCode 非空（等价于 Status==SyncFailed，见
// validateStatusConsistency）而不是「LastSuccess 也为 nil」：正常的首次失败两者
// 都为空，但万一出现「有 last_success 却没有 observed_at」这种上游写坏的行，
// 有错误码时仍然判 failed 才是诚实的——把一条带着错误码的记录说成
// 「尚未接入」，无论 LastSuccess 是什么值都是在撒谎。
// LastSuccess 仍如实返回，前端可据此区分「一直没成功过」与「曾经成功过」。
func (o Observation) Freshness(now time.Time) Freshness {
	f := Freshness{
		IsPartial:        o.IsPartial,
		Source:           o.Source,
		ObservedAt:       o.ObservedAt,
		LastSuccess:      o.LastSuccess,
		LastErrorCode:    o.LastErrorCode,
		ThresholdSeconds: o.StalenessThresholdSeconds,
	}

	if o.ObservedAt == nil {
		// 没有观测时刻：区分「一次都没采过」与「第一次采就失败了」。
		// StalenessSeconds 两种情况都留 nil——没有 observed_at 就没有可算的
		// 滞后量，编一个 0 会让失败看起来像刚刚成功过。
		if o.LastErrorCode != "" {
			f.State = StateFailed
			return f
		}
		f.State = StateUninitialized
		return f
	}

	secs := int64(now.UTC().Sub(o.ObservedAt.UTC()).Seconds())
	if secs < 0 {
		// 观测时间在未来（时钟漂移或数据错误）：按 0 处理，不让它显示成「很新鲜」
		secs = 0
	}
	f.StalenessSeconds = &secs

	switch {
	case o.Status == SyncFailed:
		f.State = StateFailed
	case secs >= int64(o.StalenessThresholdSeconds):
		f.State = StateStale
	case o.IsPartial:
		f.State = StatePartial
	default:
		f.State = StateFresh
	}
	return f
}

// ValidMetricKey 报告 key 是否**形态**合法。
//
// 它只验形状，不验存在性——这是落库时的判据（见 validateShared）。
// 「这个指标存不存在」是另一个问题，由 KnownMetricKey 回答。
//
// 从前这里的注释宣称它能让「查询参数里拼错的 key 当场 400」，那是不成立的
// （Codex 冷审 PR #48 第 6 条）：`foo.bar` 形态完全合法，照样返回 200 空数组，
// 正好落进注释声称要避免的「被当成真无数据」。注释与实现的差距已按 XM-0031
// 补上——HTTP 层改用 KnownMetricKey，本函数的契约诚实地缩回「只验形态」。
func ValidMetricKey(key string) bool {
	return metricKeyPattern.MatchString(key)
}

// registeredMetrics 是平台已知的指标白名单。
//
// 为什么用白名单而不是只验形态（XM-0031，回归 Codex 冷审 PR #48 第 6 条）：
// 只验形态时，一个拼错的 `sub2api.revenu.daily` 会返回 200 + 空数组，而前端
// 无从区分「这个指标真的没有数据」与「这个指标根本不存在」——后者是调用错误，
// 静默返回空正是宪法 12 条禁止的「用沉默撒谎」。
//
// 值来自各 connector 的 Metric* 常量（`connectors/sub2api`、`connectors/invoice`），
// 但**不 import 它们**：那些包依赖本包（ToObservations 返回 ops.Observation），
// 反向 import 会成环。字面量重复的代价由 ops_test 里的一致性测试兜住——那个测试
// 在外部测试包里，可以同时看见两边，任何一边加减指标都会当场失败。
//
// 扩展口是 RegisterMetricKey：将来 NewAPI / CPA 等采集模块在自己的 init 里
// 注册各自的指标，不必回来改这张表。
var registeredMetrics = struct {
	mu   sync.RWMutex
	keys map[string]struct{}
}{
	keys: map[string]struct{}{
		"sub2api.users.total":      {},
		"sub2api.users.balance":    {},
		"sub2api.revenue.daily":    {},
		"sub2api.cost.daily":       {},
		"sub2api.channels.balance": {},
		"sub2api.channels.status":  {},
		// 逐笔订单按日按状态资金汇总（XM-PAY0）。与上面的 sub2api.revenue.daily
		// 不是同一个口径——那条来自支付看板的按日聚合序列（无法拆到状态），
		// 这条来自逐笔订单翻页归日，见 connectors/sub2api/payments.go 顶部
		// 的口径差异说明。
		"sub2api.payments.daily": {},
		"invoice.requests.daily": {},
		"invoice.amount.daily":   {},
		// NewAPI（XM-0035，规格 §8.4）。充值与订阅分成两条而不是合并成
		// 「收入」：两笔钱的业务含义不同，合成一个数之后就再也拆不开了。
		"newapi.users.total":        {},
		"newapi.recharge.daily":     {},
		"newapi.subscription.daily": {},
		"newapi.channels.status":    {},
		"newapi.models.usage":       {},
		// 逐笔订单按日按状态资金汇总（XM-PAY0）。与 newapi.recharge.daily
		// 不是同一个口径，理由同 sub2api.payments.daily。
		"newapi.payments.daily": {},
		// 计量型渠道成本核算（XM-0037a，connectors/metering）。
		//
		// ⚠️ 与上面的 `sub2api.cost.daily` **不是同一个口径**，也绝不能合并：
		// 那条读的是 admin 面板 trend[].cost（自营实例口径，刻意避开
		// actual_cost），这条是核算成本（每令牌 /v1/usage actual_cost ÷
		// recharge_ratio）。设计稿 XM-0037 §3.1 有显著标注；
		// connectors/metering 的 TestMetricKeysDoNotCollideWithPanelCost
		// 钉住两者不同名。
		"finance.cost.daily":    {},
		"finance.revenue.daily": {},
		// 利润台账入账（XM-0037b，internal/platform/finance）。
		//
		// 与上面两条同族但**不同层**：那两条观测的是「上游说了什么」，
		// 这条观测的是「台账记下了什么」。设计稿 §5 的三条不静默纪律本来就会
		// 让一部分读数不入账（两侧未知跳过、只有一侧不建行），所以两个数
		// **本就该不同**——合并成一条会让那个差异永远看不见。
		"finance.profit.daily": {},
		// 请求量/成功率（XM-REQLOG-METRICS，connectors/reqlog 的
		// MetricSub2APIRequests*/MetricNewAPIRequests* 常量）。命名空间是
		// **平台**（sub2api/newapi），不是连接器（reqlog）：原料来自 reqlog
		// 落盘的请求审计索引，但回答的问题与 sub2api.revenue.daily 等既有
		// 指标同属平台维度——与 finance.cost.daily 定义在 connectors/metering
		// 而不是 connectors/finance 是同一条先例（见该常量注释）。
		"sub2api.requests.daily":            {},
		"sub2api.requests.success_rate_24h": {},
		"sub2api.requests.trend_7d":         {},
		"newapi.requests.daily":             {},
		"newapi.requests.success_rate_24h":  {},
		"newapi.requests.trend_7d":          {},
		// 运行保障（XM-OPS0，internal/platform/jobs）。
		//
		// 这四条不来自任何 Connector 契约，而是控制平面自己的运维信号
		// （worker 是否还活着、连接器是否可达、清理任务上一轮跑得怎样），
		// 因此常量定义在 internal/platform/jobs（heartbeat.go / connector_probe.go /
		// retention.go），不在某个 connectors/* 包里——这里仍按字面量重复的老规矩登记
		// （ops 不能反向 import jobs），一致性由 ops_test 外部测试包里的
		// TestRegisteredMetricsMatchConnectorContracts 兜住（它可以同时 import ops 与
		// jobs，不构成生产代码的环）。
		"platform.heartbeat":          {},
		"platform.retention.last_run": {},
		"sub2api.connector.health":    {},
		"newapi.connector.health":     {},
		// CPA（XM-CPA0，connectors/cpa）。CPA = CLI Proxy API + cpa-manager-plus，
		// 只读文件后端直读宿主机 usage.sqlite（只读绑定挂载，从不挂载凭据文件）。
		// 四条键字面量与 connectors/cpa/contract.go 的 Metric* 常量逐字对应，
		// 由 TestRegisteredMetricsMatchConnectorContracts 钉住一致。
		"cpa.requests.daily": {},
		"cpa.cost.daily":     {},
		// cpa.keys.usage 只是一条小型「key 数 + 前 N 采样」的周期观测——完整逐
		// key 明细走 GET /api/v1/platforms/cpa/keys 现读，不经这条观测（见该常量
		// 在 contract.go 里的说明）。
		"cpa.keys.usage": {},
		// cpa.accounts.health 读 codex_inspection_runs/results 的最近一轮结果，
		// 落在"渠道保障"页签位置（ADMIN-IA CPA 第 4 格，M1.5 徽标）——语义其实是
		// 账号巡检而非模型路由验证，裁定见 contracts/connectors/cpa.read.v1.md §9。
		"cpa.accounts.health": {},
	},
}

// RegisterMetricKey 注册一个新的指标键，让它能通过 HTTP 层的存在性校验。
//
// 供其他采集模块在 init / 启动装配时调用。形态非法直接返回错误——一个连落库
// 都通不过的键注册进来毫无意义。重复注册是幂等的，不报错：多个模块共享同一
// 条指标是合理的，为此让进程起不来不划算。
func RegisterMetricKey(key string) error {
	if !ValidMetricKey(key) {
		return fmt.Errorf("metric_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$: %w",
			key, ErrInvalidFormat)
	}
	registeredMetrics.mu.Lock()
	defer registeredMetrics.mu.Unlock()
	registeredMetrics.keys[key] = struct{}{}
	return nil
}

// KnownMetricKey 报告 key 是否是**已注册**的指标。
//
// HTTP 查询用它而不是 ValidMetricKey：不存在的指标必须当场 400，
// 不能返回一个会被读成「真的没数据」的空数组。
func KnownMetricKey(key string) bool {
	registeredMetrics.mu.RLock()
	defer registeredMetrics.mu.RUnlock()
	_, ok := registeredMetrics.keys[key]
	return ok
}

// RegisteredMetricKeys 返回全部已注册的指标键（升序）。
// 给错误文案与一致性测试用：告诉调用方「有哪些」比只说「你写错了」有用。
func RegisteredMetricKeys() []string {
	registeredMetrics.mu.RLock()
	defer registeredMetrics.mu.RUnlock()
	out := make([]string, 0, len(registeredMetrics.keys))
	for k := range registeredMetrics.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate 校验观测记录的领域不变量。
func (o Observation) Validate() error {
	if err := o.validateShared(); err != nil {
		return err
	}
	if o.StalenessThresholdSeconds <= 0 {
		return fmt.Errorf("staleness_threshold_seconds 必须为正: %w", ErrInvalidFormat)
	}
	return o.validateStatusConsistency()
}

// ValidateSample 校验历史样本的领域不变量。
//
// 与 Validate 只差一条：样本不校验 staleness_threshold_seconds。阈值回答的是
// 「现在这条数据算不算旧」，那是最新态才需要的判据；历史点位记录的是**当时**
// 的事实，对着一个过去的时刻重算新鲜度没有意义，所以样本表压根不存它
// （见 db/migrations/000005_ops_history.up.sql）。
//
// 「失败必须带错误码」这条**照样**校验——趋势图上那段红完全靠 status 与
// last_error_code 渲染，这里放行一条没有原因的失败样本，图上就会出现一段
// 没人解释得了的红。
func (o Observation) ValidateSample() error {
	if err := o.validateShared(); err != nil {
		return err
	}
	return o.validateStatusConsistency()
}

// validateShared 是最新态与样本共有的字段校验。
func (o Observation) validateShared() error {
	if o.MetricKey == "" {
		return fmt.Errorf("metric_key: %w", ErrMissingField)
	}
	if !ValidMetricKey(o.MetricKey) {
		return fmt.Errorf("metric_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$: %w",
			o.MetricKey, ErrInvalidFormat)
	}
	if o.Source == "" {
		return fmt.Errorf("source: %w", ErrMissingField)
	}
	if o.Environment == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if _, err := ParseSyncStatus(string(o.Status)); err != nil {
		return err
	}
	if o.SyncedAt.IsZero() {
		return fmt.Errorf("synced_at: %w", ErrMissingField)
	}
	return nil
}

// validateStatusConsistency：失败必须带错误码，成功必须不带——与数据库 CHECK
// 同一条规则，让「静默失败」在领域层与库层都不可表示。
func (o Observation) validateStatusConsistency() error {
	if (o.Status == SyncFailed) != (o.LastErrorCode != "") {
		return fmt.Errorf("status=%s 与 last_error_code=%q 不一致: %w",
			o.Status, o.LastErrorCode, ErrInconsistent)
	}
	return nil
}

// ScopeRead 是读取运营指标所需的权限。
//
// 与 registry.ScopeRead 分开授予而不是共用一个「读」权限：指标里将来会有
// 收入、余额这类业务数据（XM-0017 接入 Sub2API 之后），比「有哪些服务」
// 敏感一个量级，不该被同一个 scope 一并放行。
const ScopeRead = "ops.read"
