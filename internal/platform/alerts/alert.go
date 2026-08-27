package alerts

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Severity 是告警严重度（规格 §9.3 要求的「严重度」字段）。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Status 是告警生命周期状态，五个值逐字来自规格 §9.3。
type Status string

const (
	StatusOpen         Status = "OPEN"
	StatusAcknowledged Status = "ACKNOWLEDGED"
	StatusSilenced     Status = "SILENCED"
	StatusResolved     Status = "RESOLVED"
	StatusReopened     Status = "REOPENED"
)

// NotifyStatus 是投递状态（规格 §9.3「通知投递状态」）。
//
// 它与 Status 正交：告警活着不代表通知送到了。分开建模是有意的——
// 合并成一个枚举就必须在「OPEN 且已投递」和「OPEN 但投递失败」之间二选一，
// 而后者正是最需要被看见的那一种。
type NotifyStatus string

const (
	// NotifyPending：还没投递过，或静默窗口过期后被推回队列等待重投。
	NotifyPending NotifyStatus = "pending"
	// NotifyDelivered：至少一个渠道确认收下了。
	NotifyDelivered NotifyStatus = "delivered"
	// NotifyFailed：投递尝试过并失败，下一轮自动重试（§9.3「失败重试」）。
	NotifyFailed NotifyStatus = "failed"
)

var (
	// ErrInvalidSeverity：严重度非法。
	ErrInvalidSeverity = errors.New("invalid alert severity")
	// ErrInvalidStatus：告警状态非法。
	ErrInvalidStatus = errors.New("invalid alert status")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("invalid format")
	// ErrNotFound：告警或静默窗口不存在。
	ErrNotFound = errors.New("not found")
	// ErrNotAcknowledgeable：告警当前状态不允许确认。
	ErrNotAcknowledgeable = errors.New("alert is not in an acknowledgeable state")
)

// ruleKeyPattern 与库层 CHECK 同一条正则。
var ruleKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// ParseSeverity 解析严重度。
func ParseSeverity(s string) (Severity, error) {
	switch Severity(s) {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return Severity(s), nil
	default:
		return "", fmt.Errorf("severity %q: %w", s, ErrInvalidSeverity)
	}
}

// ParseStatus 解析告警状态。
func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusOpen, StatusAcknowledged, StatusSilenced, StatusResolved, StatusReopened:
		return Status(s), nil
	default:
		return "", fmt.Errorf("status %q: %w", s, ErrInvalidStatus)
	}
}

// activeStatuses 是「还活着」的四个状态，与 000007 迁移里那条部分唯一索引
// 的谓词、以及 db/queries/alerts.sql 里每条查询的 IN 列表逐字一致。
//
// SILENCED 在列表里是关键：静默不是终态。三处必须同时改，任何一处漏改都会
// 让去重失效（同一个问题每 60 秒新开一条）。
var activeStatuses = []Status{
	StatusOpen, StatusAcknowledged, StatusSilenced, StatusReopened,
}

// IsActive 报告该状态是否算「活跃」。
func (s Status) IsActive() bool {
	for _, a := range activeStatuses {
		if s == a {
			return true
		}
	}
	return false
}

// ActiveStatusStrings 返回活跃状态的字符串形式（供 HTTP 层与查询参数使用）。
func ActiveStatusStrings() []string {
	out := make([]string, 0, len(activeStatuses))
	for _, s := range activeStatuses {
		out = append(out, string(s))
	}
	return out
}

// Alert 是一条告警实例（规格 §2.3：Alert = 告警实例）。
type Alert struct {
	ID       uuid.UUID
	RuleKey  string
	DedupKey string
	Severity Severity
	Status   Status
	Title    string
	Detail   string

	Environment string

	// OpenedAt 是首次发现，LastSeenAt 是最近一次仍然满足条件。
	// 两个都留：只留其一就答不出「这个问题持续了多久」。
	OpenedAt       time.Time
	AcknowledgedAt *time.Time
	ResolvedAt     *time.Time
	LastSeenAt     time.Time

	// FireCount 是被去重合并掉的命中次数（含首次），最小为 1。
	// 它是「抖了一下」与「持续两小时」的唯一区分依据。
	FireCount int32

	// SourceMetricKey 指回 ops.metric_observation 里那条指标；
	// 空串表示这条告警与具体指标无关。
	SourceMetricKey string

	NotifyStatus NotifyStatus
	NotifyError  string
	NotifiedAt   *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Silence 是一个静默窗口（规格 §9.3「静默」、§22.2 第 13 项「静默窗口」）。
type Silence struct {
	ID uuid.UUID
	// RuleKey 为空表示全局窗口：该环境下所有规则都不投递。
	RuleKey     string
	Environment string
	// Reason 非空是硬约束。静默是主动让告警闭嘴，没有理由的静默
	// 在事后复盘时与「有人手滑」不可区分。
	Reason    string
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedBy string
	CreatedAt time.Time
}

// Matches 报告该窗口此刻是否覆盖某条规则。
//
// 判定放在 Go 而不是 SQL：窗口数量是个位数，把「全局 vs 按规则」这条语义
// 留在一处，比在查询与代码里各写一遍安全——两份实现迟早会分叉，而分叉的
// 后果是「以为静默了其实没有」或反过来。
func (s Silence) Matches(ruleKey string, at time.Time) bool {
	if s.Environment == "" || ruleKey == "" {
		return false
	}
	if s.RuleKey != "" && s.RuleKey != ruleKey {
		return false
	}
	at = at.UTC()
	// 左闭右开：[starts_at, ends_at)。右开让「窗口正好到期的那一刻」
	// 归属明确——到点即失效，不会出现多静默一轮的边界争议。
	return !at.Before(s.StartsAt.UTC()) && at.Before(s.EndsAt.UTC())
}

// Validate 校验静默窗口的领域不变量（与库层 CHECK 同一套规则）。
func (s Silence) Validate() error {
	if s.Environment == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if strings.TrimSpace(s.Reason) == "" {
		return fmt.Errorf("reason: %w", ErrMissingField)
	}
	if strings.TrimSpace(s.CreatedBy) == "" {
		return fmt.Errorf("created_by: %w", ErrMissingField)
	}
	if s.RuleKey != "" && !ruleKeyPattern.MatchString(s.RuleKey) {
		return fmt.Errorf("rule_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$: %w",
			s.RuleKey, ErrInvalidFormat)
	}
	if s.StartsAt.IsZero() || s.EndsAt.IsZero() {
		return fmt.Errorf("starts_at/ends_at: %w", ErrMissingField)
	}
	if !s.EndsAt.After(s.StartsAt) {
		return fmt.Errorf("ends_at 必须晚于 starts_at: %w", ErrInvalidFormat)
	}
	return nil
}
