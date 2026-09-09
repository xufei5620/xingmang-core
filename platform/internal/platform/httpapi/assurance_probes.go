package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// AssuranceProbeQuerier 是"检测任务" / "主动检测历史"两个只读端点的能力
// （*assurance.Service 满足）。httpapi 包不直接持有 assurance.Store——与
// ChannelAssuranceQuerier 对 channelassurance.Service 的做法一致，路由层
// 只认窄接口。
type AssuranceProbeQuerier interface {
	ProbeList(ctx context.Context, platform, environment string) (assurance.ProbeListResult, error)
	ProbeHistory(ctx context.Context, platform, environment string, cursor *time.Time, limit int) ([]assurance.HistoryEntry, error)
}

// cannotRunReasonText 把 assurance.Reason* 的稳定枚举串翻成人话（设计稿
// §6.1）："发起检测"按钮的 tooltip 只显示这段文案，不显示原始 reason 字符串。
func cannotRunReasonText(reason string) string {
	switch reason {
	case "":
		return ""
	case assurance.ReasonDeclarationCancelled:
		return "该检测任务已被取消"
	case assurance.ReasonGlobalKillSwitchOff:
		return "全局检测开关已关闭"
	case assurance.ReasonPlatformKillSwitchOff:
		return "该平台检测未启用，请在治理段打开开关"
	case assurance.ReasonProbeCredentialMissing:
		return "探测凭据未登记"
	case assurance.ReasonTargetHostNotAllowlisted:
		return "探测目标主机不在白名单内"
	case assurance.ReasonDailyBudgetExhausted:
		return "今日探测预算已用完，明日 00:00（业务日）重置"
	case assurance.ReasonCooldownNotElapsed:
		return "冷却中，请稍后再试"
	case assurance.ReasonPlatformRunInProgress:
		return "检测进行中"
	default:
		return "暂不可发起检测"
	}
}

type probeListItemBody struct {
	DeclarationID       string   `json:"declaration_id"`
	Name                string   `json:"name"`
	ChannelIDs          []string `json:"channel_ids"`
	ChannelNames        []string `json:"channel_names"`
	TargetModels        []string `json:"target_models"`
	PolicyText          string   `json:"policy_text"`
	ScheduleCron        string   `json:"schedule_cron,omitempty"`
	LastRunAt           *string  `json:"last_run_at"`
	LastRunStatus       string   `json:"last_run_status"`
	LastRunVerdict      *string  `json:"last_run_verdict"`
	KillSwitchState     string   `json:"kill_switch_state"`
	CanRunNow           bool     `json:"can_run_now"`
	CannotRunReason     string   `json:"cannot_run_reason,omitempty"`
	CannotRunReasonText string   `json:"cannot_run_reason_text,omitempty"`
}

// probeListBody 是"检测任务"端点的响应体（设计稿 §5.1）。
type probeListBody struct {
	Platform  string              `json:"platform"`
	Probes    []probeListItemBody `json:"probes"`
	Freshness freshnessBody       `json:"freshness"`
	// AssertionDisclaimer 是设计稿 §1.2.3 要求常驻的诚实说明：检测结果是
	// 形状与延迟检测，不构成语义正确性保证——page 顶部的说明条直接取这
	// 一个字段，不在前端另写一份可能漂移的文案。
	AssertionDisclaimer string `json:"assertion_disclaimer"`
	// ChannelBreakdownSupported 恒为 true——与 XM-ASSURE0 被动指标端点的
	// 同名字段刻意呼应，但语义相反：被动统计做不到按渠道拆分（reqlog 落盘
	// 格式没有这个维度），主动探测**天生就是**按渠道声明的（declare@1 的
	// targets 数组本来就是逐 channel_id 声明），因此这里没有"做不到"这回事，
	// 恒 true 且不需要 reason 字段。前端不应把这个字段与保障概览页那个恒
	// false 的同名字段混为一谈。
	ChannelBreakdownSupported bool `json:"channel_breakdown_supported"`
}

const assuranceAssertionDisclaimer = "检测结果为形状与延迟检测，非语义正确性保证。"

func toProbeListItemBody(item assurance.ProbeListItem) probeListItemBody {
	body := probeListItemBody{
		DeclarationID:       item.DeclarationID,
		Name:                item.Name,
		ChannelIDs:          item.ChannelIDs,
		ChannelNames:        item.ChannelNames,
		TargetModels:        item.TargetModels,
		PolicyText:          item.PolicyText,
		ScheduleCron:        item.ScheduleCron,
		LastRunStatus:       item.LastRunStatus,
		LastRunVerdict:      item.LastRunVerdict,
		KillSwitchState:     item.KillSwitchState,
		CanRunNow:           item.CanRunNow,
		CannotRunReason:     item.CannotRunReason,
		CannotRunReasonText: cannotRunReasonText(item.CannotRunReason),
	}
	if item.LastRunAt != nil {
		body.LastRunAt = rfc3339Ptr(item.LastRunAt)
	}
	return body
}

// GetPlatformAssuranceProbesHandler 读取某平台的"检测任务"表（设计稿 §5.1）。
//
// 权限复用 requestlog.ScopeRead（request.read）：这批数据的敏感度与被动
// 指标同级——都是"这个平台今天调用多不多/健不健康"，不含 Prompt 具体输出
// （见设计稿 §5.1 的理由）。
func GetPlatformAssuranceProbesHandler(q AssuranceProbeQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		result, err := q.ProbeList(r.Context(), chi.URLParam(r, "platform"), p.Environment)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		now := time.Now().UTC()
		items := make([]probeListItemBody, 0, len(result.Items))
		for _, item := range result.Items {
			items = append(items, toProbeListItemBody(item))
		}
		WriteJSON(w, http.StatusOK, probeListBody{
			Platform:                  result.Platform,
			Probes:                    items,
			Freshness:                 realtimeFreshness(now),
			AssertionDisclaimer:       assuranceAssertionDisclaimer,
			ChannelBreakdownSupported: true,
		})
	}
}

type probeHistoryEntryBody struct {
	ObservedAt         string `json:"observed_at"`
	ChannelID          string `json:"channel_id"`
	ExternalChannelID  string `json:"external_channel_id,omitempty"`
	Model              string `json:"model"`
	DeclarationName    string `json:"declaration_name"`
	PromptTemplateKey  string `json:"prompt_template_key"`
	Status             string `json:"status"`
	Verdict            string `json:"verdict"`
	EvidenceRef        string `json:"evidence_ref"`
	LatencyMS          *int   `json:"latency_ms"`
	FirstTokenMS       *int   `json:"first_token_ms"`
	MeasuredFirstToken bool   `json:"measured_first_token"`
	TokensUsed         *int   `json:"tokens_used"`
	HTTPStatus         *int   `json:"http_status"`
	ErrorKind          string `json:"error_kind,omitempty"`
}

// probeHistoryBody 是"主动检测历史"端点的响应体（设计稿 §5.2）。这是与
// GetPlatformAssuranceHistoryHandler（被动近 7 天聚合）**完全独立**的端点，
// 两者各自渲染各自的卡片，不合并成一次请求（设计稿 §5.2 明确要求）。
type probeHistoryBody struct {
	Platform   string                  `json:"platform"`
	Entries    []probeHistoryEntryBody `json:"entries"`
	NextCursor *string                 `json:"next_cursor"`
	Freshness  freshnessBody           `json:"freshness"`
	// ChannelBreakdownSupported：见 probeListBody 同名字段的注释，恒 true。
	ChannelBreakdownSupported bool `json:"channel_breakdown_supported"`
}

func toProbeHistoryEntryBody(e assurance.HistoryEntry) probeHistoryEntryBody {
	return probeHistoryEntryBody{
		ObservedAt:         e.ObservedAt.UTC().Format(time.RFC3339),
		ChannelID:          e.ChannelID,
		ExternalChannelID:  e.ExternalChannelID,
		Model:              e.Model,
		DeclarationName:    e.DeclarationName,
		PromptTemplateKey:  e.PromptTemplateKey,
		Status:             e.Status,
		Verdict:            e.Verdict,
		EvidenceRef:        e.EvidenceRef,
		LatencyMS:          e.LatencyMS,
		FirstTokenMS:       e.FirstTokenMS,
		MeasuredFirstToken: e.MeasuredFirstToken,
		TokensUsed:         e.TokensUsed,
		HTTPStatus:         e.HTTPStatus,
		ErrorKind:          e.ErrorKind,
	}
}

func parseProbeHistoryCursor(r *http.Request) (*time.Time, error) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, action.NewError(action.CodeInvalidParams, "cursor 必须是 RFC3339 时间戳", err)
	}
	return &t, nil
}

func parseProbeHistoryLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return assurance.DefaultProbeHistoryLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是正整数", err)
	}
	if n > assurance.MaxProbeHistoryLimit {
		n = assurance.MaxProbeHistoryLimit
	}
	return n, nil
}

// GetPlatformAssuranceProbeHistoryHandler 读取某平台的主动探测历史，按时间
// 倒序分页（设计稿 §5.2）。同样复用 request.read。
func GetPlatformAssuranceProbeHistoryHandler(q AssuranceProbeQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		cursor, err := parseProbeHistoryCursor(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseProbeHistoryLimit(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		entries, err := q.ProbeHistory(r.Context(), chi.URLParam(r, "platform"), p.Environment, cursor, limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		bodyEntries := make([]probeHistoryEntryBody, 0, len(entries))
		for _, e := range entries {
			bodyEntries = append(bodyEntries, toProbeHistoryEntryBody(e))
		}
		var nextCursor *string
		if len(entries) == limit {
			last := entries[len(entries)-1].CreatedAt.UTC().Format(time.RFC3339)
			nextCursor = &last
		}
		WriteJSON(w, http.StatusOK, probeHistoryBody{
			Platform:                  chi.URLParam(r, "platform"),
			Entries:                   bodyEntries,
			NextCursor:                nextCursor,
			Freshness:                 realtimeFreshness(time.Now().UTC()),
			ChannelBreakdownSupported: true,
		})
	}
}

// realtimeFreshness 与 freshnessFromAssurance（assurance.go）同一条纪律：
// 这是一次实时读取，不是周期观测，State 恒 fresh、staleness 恒 0。
func realtimeFreshness(now time.Time) freshnessBody {
	staleness := int64(0)
	return freshnessBody{
		State:            "fresh",
		StalenessSeconds: &staleness,
		ThresholdSeconds: requestLogStalenessThresholdSeconds,
		ObservedAt:       rfc3339Ptr(&now),
		LastSuccess:      rfc3339Ptr(&now),
	}
}
