package alerts

// Runway 阈值影响预览（XM-C-RUNWAY0/C3b）。这是纯内存比较器：不读写 Store、
// 不触发 Action，也不启动评估轮次。它把当前分类、当前 R5 活跃态与 proposed
// 分类放在同一张证据表里，避免把 worker 延迟误报成阈值变化效果。

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

type RunwayImpactTransition string

const (
	RunwayWouldOpen           RunwayImpactTransition = "would_open"
	RunwayWouldEscalate       RunwayImpactTransition = "would_escalate"
	RunwayWouldDeescalate     RunwayImpactTransition = "would_deescalate"
	RunwayWouldResolve        RunwayImpactTransition = "would_resolve"
	RunwayUnchanged           RunwayImpactTransition = "unchanged"
	RunwayCurrentInconsistent RunwayImpactTransition = "current_inconsistent"
)

type RunwayImpactItem struct {
	AccountID            uuid.UUID
	Name                 string
	Days                 *int
	OldLevel             finance.RunwayLevel
	NewLevel             finance.RunwayLevel
	CurrentAlertSeverity Severity
	CurrentAlertStatus   Status
	AlertCount           int
	Transition           RunwayImpactTransition
	ConsistencyReason    string
	ObservedAt           *time.Time
}

type RunwayImpactCounts struct {
	WouldOpen           int
	WouldEscalate       int
	WouldDeescalate     int
	WouldResolve        int
	Unchanged           int
	CurrentInconsistent int
}

type RunwayImpactCoverage struct {
	Total          int
	Known          int
	UnknownReasons map[string]int
}

type RunwayImpactPreview struct {
	Current  finance.RunwayThresholds
	Proposed finance.RunwayThresholds
	Items    []RunwayImpactItem
	Counts   RunwayImpactCounts
	Coverage RunwayImpactCoverage
}

type activeAlertSet struct {
	items []Alert
}

func isActionable(level finance.RunwayLevel) bool {
	return level == finance.RunwayCritical || level == finance.RunwayWarning
}

func expectedSeverity(level finance.RunwayLevel) Severity {
	if level == finance.RunwayCritical {
		return SeverityCritical
	}
	return SeverityWarning
}

func transitionRank(t RunwayImpactTransition) int {
	switch t {
	case RunwayCurrentInconsistent:
		return 0
	case RunwayWouldOpen:
		return 1
	case RunwayWouldEscalate:
		return 2
	case RunwayWouldDeescalate:
		return 3
	case RunwayWouldResolve:
		return 4
	default:
		return 5
	}
}

func activeR5ByAccount(items []Alert) map[uuid.UUID][]Alert {
	out := make(map[uuid.UUID][]Alert)
	for _, alert := range items {
		if alert.RuleKey != RuleUpstreamRunwayLow || !alert.Status.IsActive() {
			continue
		}
		// R5 的 dedup key 最后一段是 account UUID；标题不是稳定 ID，
		// 不能用字符串猜测归属。旧数据若没有环境段，也仍可取最后一段。
		parts := strings.Split(alert.DedupKey, ":")
		var id uuid.UUID
		if len(parts) > 0 {
			id, _ = uuid.Parse(parts[len(parts)-1])
		}
		if id != uuid.Nil {
			out[id] = append(out[id], alert)
		}
	}
	return out
}

// PreviewRunwayThresholds 比较当前/提议阈值。activeAlerts 只应传入当前环境
// 的告警；函数仍会按 rule/status 过滤，其他规则不会影响一致性判断。
func PreviewRunwayThresholds(
	current, proposed finance.RunwayThresholds,
	runways []finance.UpstreamRunway,
	activeAlerts []Alert,
) (RunwayImpactPreview, error) {
	if err := current.Validate(); err != nil {
		return RunwayImpactPreview{}, err
	}
	if err := proposed.Validate(); err != nil {
		return RunwayImpactPreview{}, err
	}
	alertsByAccount := activeR5ByAccount(activeAlerts)
	out := RunwayImpactPreview{
		Current: current, Proposed: proposed,
		Coverage: RunwayImpactCoverage{UnknownReasons: map[string]int{}},
	}
	for _, runway := range runways {
		if runway.Runway.Reason == finance.RunwayReasonNotApplicable {
			out.Coverage.UnknownReasons[string(runway.Runway.Reason)]++
			continue
		}
		out.Coverage.Total++
		item := RunwayImpactItem{
			AccountID: runway.AccountID, Name: runway.Name,
			Days: runway.Runway.Days, ObservedAt: runway.Runway.BalanceObservedAt,
		}
		alertsForAccount := alertsByAccount[runway.AccountID]
		item.AlertCount = len(alertsForAccount)
		if len(alertsForAccount) > 0 {
			item.CurrentAlertSeverity = alertsForAccount[0].Severity
			item.CurrentAlertStatus = alertsForAccount[0].Status
		}
		if runway.Runway.Days == nil {
			out.Coverage.UnknownReasons[string(runway.Runway.Reason)]++
			if len(alertsForAccount) > 0 {
				item.Transition = RunwayCurrentInconsistent
				item.ConsistencyReason = "unexpected_active_alert"
				if len(alertsForAccount) > 1 {
					item.ConsistencyReason = "duplicate_active_alert"
				}
				out.Items = append(out.Items, item)
				out.Counts.CurrentInconsistent++
			}
			continue
		}
		out.Coverage.Known++
		oldLevel, err := current.Classify(*runway.Runway.Days)
		if err != nil {
			return RunwayImpactPreview{}, err
		}
		newLevel, err := proposed.Classify(*runway.Runway.Days)
		if err != nil {
			return RunwayImpactPreview{}, err
		}
		item.OldLevel, item.NewLevel = oldLevel, newLevel

		// 当前 R5 与当前 classifier 不一致优先于任何 proposed 影响。
		if len(alertsForAccount) > 1 {
			item.Transition = RunwayCurrentInconsistent
			item.ConsistencyReason = "duplicate_active_alert"
		} else if len(alertsForAccount) == 0 && isActionable(oldLevel) {
			item.Transition = RunwayCurrentInconsistent
			item.ConsistencyReason = "missing_active_alert"
		} else if len(alertsForAccount) > 0 && !isActionable(oldLevel) {
			item.Transition = RunwayCurrentInconsistent
			item.ConsistencyReason = "unexpected_active_alert"
		} else if len(alertsForAccount) > 0 && alertsForAccount[0].Severity != expectedSeverity(oldLevel) {
			item.Transition = RunwayCurrentInconsistent
			item.ConsistencyReason = "severity_mismatch"
		} else {
			switch {
			case !isActionable(oldLevel) && isActionable(newLevel):
				item.Transition = RunwayWouldOpen
			case oldLevel == finance.RunwayWarning && newLevel == finance.RunwayCritical:
				item.Transition = RunwayWouldEscalate
			case oldLevel == finance.RunwayCritical && newLevel == finance.RunwayWarning:
				item.Transition = RunwayWouldDeescalate
			case isActionable(oldLevel) && !isActionable(newLevel):
				item.Transition = RunwayWouldResolve
			default:
				item.Transition = RunwayUnchanged
			}
		}
		out.Items = append(out.Items, item)
		switch item.Transition {
		case RunwayWouldOpen:
			out.Counts.WouldOpen++
		case RunwayWouldEscalate:
			out.Counts.WouldEscalate++
		case RunwayWouldDeescalate:
			out.Counts.WouldDeescalate++
		case RunwayWouldResolve:
			out.Counts.WouldResolve++
		case RunwayCurrentInconsistent:
			out.Counts.CurrentInconsistent++
		default:
			out.Counts.Unchanged++
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		if transitionRank(out.Items[i].Transition) != transitionRank(out.Items[j].Transition) {
			return transitionRank(out.Items[i].Transition) < transitionRank(out.Items[j].Transition)
		}
		return out.Items[i].AccountID.String() < out.Items[j].AccountID.String()
	})
	return out, nil
}
