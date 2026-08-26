package ops

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// State 是新鲜度的主状态。一个徽章只能显示一个状态，因此必须定义优先级。
type State string

const (
	StateUninitialized State = "uninitialized" // 从未成功采集
	StateFailed        State = "failed"        // 最近一次同步失败
	StateStale         State = "stale"         // 超过新鲜度阈值
	StatePartial       State = "partial"       // 数据不完整
	StateFresh         State = "fresh"         // 新鲜且完整
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
// 优先级：未初始化 > 失败 > 延迟 > 部分 > 新鲜。
// 理由：从运维视角，「同步失败了」比「数据旧了」更需要立即动作；
// 「数据旧了」又比「数据不全」更严重——不全但新的数据至少反映当前。
// 被主状态盖住的信息仍在 IsPartial / LastErrorCode 等字段里，不丢。
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

// Validate 校验观测记录的领域不变量。
func (o Observation) Validate() error {
	if o.MetricKey == "" {
		return fmt.Errorf("metric_key: %w", ErrMissingField)
	}
	if !metricKeyPattern.MatchString(o.MetricKey) {
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
	if o.StalenessThresholdSeconds <= 0 {
		return fmt.Errorf("staleness_threshold_seconds 必须为正: %w", ErrInvalidFormat)
	}
	// 失败必须带错误码，成功必须不带——与数据库 CHECK 同一条规则，
	// 让「静默失败」在领域层与库层都不可表示
	if (o.Status == SyncFailed) != (o.LastErrorCode != "") {
		return fmt.Errorf("status=%s 与 last_error_code=%q 不一致: %w",
			o.Status, o.LastErrorCode, ErrInconsistent)
	}
	if o.SyncedAt.IsZero() {
		return fmt.Errorf("synced_at: %w", ErrMissingField)
	}
	return nil
}

// ScopeRead 是读取运营指标所需的权限。
//
// 与 registry.ScopeRead 分开授予而不是共用一个「读」权限：指标里将来会有
// 收入、余额这类业务数据（XM-0017 接入 Sub2API 之后），比「有哪些服务」
// 敏感一个量级，不该被同一个 scope 一并放行。
const ScopeRead = "ops.read"
