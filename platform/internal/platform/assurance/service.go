package assurance

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

// Service 是"检测任务"表 / "主动检测历史"两个只读 Query 的入口（设计稿
// §5.1/§5.2）。分层参照 internal/platform/channelassurance：Service 包一层
// 平台解析与聚合，把 Store 的行级读取组装成前端表格需要的形状；权限判定
// （request.read）在路由层做，不在这里重复。
type Service struct {
	store *Store
}

// NewService 构造 Query 入口。store 为 nil 时构造期即拒绝。
func NewService(store *Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("assurance: store 为空: %w", ErrInvalidInput)
	}
	return &Service{store: store}, nil
}

// ProbeListItem 是"检测任务"表的一行（设计稿 §5.1，字段名对齐设计稿的
// JSON 示例，供 httpapi 层转 DTO；XM-ASSURE1-ui 应据此构造前端类型）。
type ProbeListItem struct {
	DeclarationID string
	Name          string
	ChannelIDs    []string
	ChannelNames  []string
	TargetModels  []string
	// PolicyText 是"策略"列的展示文案。本片没有接线定时调度（团队交接明确
	// 划定 XM-ASSURE1-core 只做按需触发），因此即便声明带了 schedule_cron，
	// 文案也必须诚实地说明"未接线"，不能暗示这条声明真的会按 cron 自动触发
	// （宪法 12 条：不能让"填了但没生效"的配置看起来已经生效）。
	PolicyText   string
	ScheduleCron string

	LastRunAt *time.Time
	// LastRunStatus 是 ResultStatus* 之一，或 RunStatusRefused/RunStatusCancelled/
	// RunStatusPending/RunStatusRunning（批次尚未产出逐渠道结果时），或
	// "never_run"（从未触发过）。
	LastRunStatus  string
	LastRunVerdict *string

	// KillSwitchState 是 "enabled" | "disabled" | "not_applicable_fake"。
	KillSwitchState string
	CanRunNow       bool
	// CannotRunReason 是 Reason* 常量之一；CanRunNow=true 时恒为空串。
	// httpapi 层负责把它翻成人话文案，不直接把这个枚举串显示给运营
	// （设计稿 §6.1）。
	CannotRunReason string
}

// ProbeListResult 是"检测任务"表端点的响应形状。
type ProbeListResult struct {
	Platform string
	Items    []ProbeListItem
}

// StatusNeverRun 是 LastRunStatus 在"从未触发过"时的取值。
const StatusNeverRun = "never_run"

// ProbeList 读取某平台+环境下全部 active 声明及其最新一次批次/结果（设计稿
// §5.1）。
func (svc *Service) ProbeList(ctx context.Context, platform, environment string) (ProbeListResult, error) {
	source, err := requestlog.ResolvePlatform(platform)
	if err != nil {
		return ProbeListResult{}, err
	}
	decls, err := svc.store.ListDeclarations(ctx, source, environment, []string{DeclarationStatusActive})
	if err != nil {
		return ProbeListResult{}, err
	}
	cfg, err := svc.store.GetConnectorProbeConfig(ctx, source, environment)
	if err != nil {
		return ProbeListResult{}, err
	}
	killSwitch := killSwitchState(cfg, svc.store.globalKillSwitchEnabled)

	items := make([]ProbeListItem, 0, len(decls))
	for _, d := range decls {
		item, err := svc.probeListItemFor(ctx, d, killSwitch)
		if err != nil {
			return ProbeListResult{}, err
		}
		items = append(items, item)
	}
	return ProbeListResult{Platform: source, Items: items}, nil
}

func (svc *Service) probeListItemFor(ctx context.Context, d Declaration, killSwitch string) (ProbeListItem, error) {
	item := ProbeListItem{
		DeclarationID:   d.ID,
		Name:            d.Name,
		ScheduleCron:    d.ScheduleCron,
		KillSwitchState: killSwitch,
	}
	ids := make([]string, 0, len(d.Targets))
	models := make([]string, 0, len(d.Targets))
	seenModel := make(map[string]bool, len(d.Targets))
	for _, t := range d.Targets {
		ids = append(ids, t.ChannelID)
		if !seenModel[t.Model] {
			seenModel[t.Model] = true
			models = append(models, t.Model)
		}
	}
	item.ChannelIDs = ids
	item.TargetModels = models
	if names, err := svc.store.ChannelDisplayNames(ctx, d.Platform, d.Environment, ids); err == nil {
		item.ChannelNames = names
	} else {
		// 展示名解析失败不该让整张表 500——渠道目录读取失败是一个已知会发生
		// 的降级场景（观测尚未采集），退回显示原始 channel_id 仍然诚实。
		item.ChannelNames = ids
	}
	if d.ScheduleCron == "" {
		item.PolicyText = "按需 · 无定时"
	} else {
		item.PolicyText = "按需（已声明定时表达式，本片尚未接线自动触发）"
	}

	run, err := svc.store.LatestRunForDeclaration(ctx, d.ID)
	if err != nil {
		return ProbeListItem{}, err
	}
	if run == nil {
		item.LastRunStatus = StatusNeverRun
	} else {
		item.LastRunAt = &run.CreatedAt
		switch run.Status {
		case RunStatusRefused, RunStatusCancelled, RunStatusPending, RunStatusRunning:
			item.LastRunStatus = run.Status
		default:
			results, err := svc.store.ResultsForRun(ctx, run.ID)
			if err != nil {
				return ProbeListItem{}, err
			}
			status, verdict := rollupResults(results)
			if status == "" {
				status = run.Status
			}
			item.LastRunStatus = status
			item.LastRunVerdict = verdict
		}
	}

	gate, err := svc.store.CheckGatesReadOnly(ctx, d)
	if err != nil {
		return ProbeListItem{}, err
	}
	item.CanRunNow = gate.Allowed
	item.CannotRunReason = gate.Reason
	return item, nil
}

// killSwitchState 派生检测任务表的 kill_switch_state 列（设计稿 §5.1）。
func killSwitchState(cfg *ConnectorProbeConfig, globalEnabled bool) string {
	if cfg == nil || cfg.Mode != ModeReal {
		return "not_applicable_fake"
	}
	if cfg.ProbeEnabled && globalEnabled {
		return "enabled"
	}
	return "disabled"
}

// resultRank 给 rollupResults 排"最坏结果"用的优先级：failed/timeout 最坏，
// 其次 degraded，最好 ok。
func resultRank(status string) int {
	switch status {
	case ResultStatusFailed, ResultStatusTimeout:
		return 3
	case ResultStatusDegraded:
		return 2
	case ResultStatusOK:
		return 1
	default:
		return 0
	}
}

// rollupResults 把一个批次里逐渠道/模型的结果汇总成一个"这个批次总体怎么样"
// 的状态+结论——取最坏的那一条（与前端 pill 语义一致：一个 target 失败，
// 这一行就不该显示"一致"）。
func rollupResults(results []Result) (status string, verdict *string) {
	if len(results) == 0 {
		return "", nil
	}
	worst := results[0]
	for _, r := range results[1:] {
		if resultRank(r.Status) > resultRank(worst.Status) {
			worst = r
		}
	}
	v := worst.Verdict
	return worst.Status, &v
}

// ProbeHistory 读取某平台+环境的主动探测历史（设计稿 §5.2），分页。
func (svc *Service) ProbeHistory(
	ctx context.Context, platform, environment string, cursor *time.Time, limit int,
) ([]HistoryEntry, error) {
	source, err := requestlog.ResolvePlatform(platform)
	if err != nil {
		return nil, err
	}
	return svc.store.ListProbeHistory(ctx, source, environment, cursor, limit)
}
