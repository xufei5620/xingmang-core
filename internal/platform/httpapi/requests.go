package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

// RequestLogQuerier 是请求详情的只读能力（*requestlog.Service 满足）。
type RequestLogQuerier interface {
	List(ctx context.Context, in requestlog.ListInput) (reqlog.RequestLogPage, error)
	Content(ctx context.Context, in requestlog.ContentInput) (reqlog.RequestLogContent, error)
}

// requestSummaryItem 是一条请求元数据的对外表示。
//
// **逐字段列出，不是 reqlog.RequestLogSummary 的直接序列化**：响应体是契约，
// 不是结构体的倒影（规格 §18.4）。这条在本端点上不只是洁癖——契约层将来若
// 为了排查方便往 Summary 上加一个带正文片段的字段，直接序列化会让它一路
// 漏到只有 request.read 权限的人眼前，而这里加字段必须是一次显式动作。
type requestSummaryItem struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	OccurredAt string `json:"occurred_at"`
	// Username 为空表示令牌没映射到用户——前端必须显示成「未映射」
	// 而不是空白（空白会被读成「这条没有用户」）。
	Username    string `json:"username"`
	TokenPrefix string `json:"token_prefix"`
	Model       string `json:"model"`
	Channel     string `json:"channel"`
	Upstream    string `json:"upstream"`
	Status      int    `json:"status"`
	DurationMS  int64  `json:"duration_ms"`
	// TTFBMS 为 null 表示未记录；0 是合法观测值（缓存命中），两者不可混同。
	TTFBMS      *int64 `json:"ttfb_ms"`
	TokensIn    int64  `json:"tokens_in"`
	TokensOut   int64  `json:"tokens_out"`
	TokensCache int64  `json:"tokens_cache"`
	// BilledAmount 是向终端用户计费金额；null=未知，对象内的 0=已知零。
	BilledAmount      *requestBilledAmount `json:"billed_amount"`
	Stream            bool                 `json:"stream"`
	UpstreamRequestID string               `json:"upstream_request_id"`
	// ClientIP 已在连接器侧脱敏（reqlog.MaskIP），这里不做二次处理——
	// 二次处理会让「脱敏在哪一层做」变成两个答案。
	ClientIP string `json:"client_ip"`
}

type requestBilledAmount struct {
	AmountMinor string `json:"amount_minor"`
	Currency    string `json:"currency"`
	Scale       int32  `json:"scale"`
}

type requestStats struct {
	RequestCount      int64  `json:"request_count"`
	SuccessCount      int64  `json:"success_count"`
	FailureCount      int64  `json:"failure_count"`
	AverageDurationMS *int64 `json:"average_duration_ms"`
}

// requestPage 是一页请求元数据。
type requestPage struct {
	Items []requestSummaryItem `json:"items"`
	Stats requestStats         `json:"stats"`
	// NextCursor 为空串表示已经翻到底。
	NextCursor string `json:"next_cursor"`
	// RetentionDays 让界面能就地说清「只覆盖最近 N 天」——
	// 「那天没有请求」与「过了保留期」在屏幕上长得一模一样，处置却完全不同。
	RetentionDays int `json:"retention_days"`
	// DataSource 是回答这次读取的 reqlog 实例（fake 时为 reqlog-fake）。
	//
	// 字段名不叫 source：item 里的 `source` 已经是**平台**（sub2api/newapi），
	// 两个同名字段指两件事，读响应的人一定会搞混。这里对齐
	// /api/v1/metrics 里 source 的**语义**（哪个实例产的这批数），
	// 前端据此挂演示数据横幅。
	DataSource string `json:"data_source"`
	// Freshness 与 /api/v1/metrics 同一个形状，前端复用同一个徽章组件。
	// 不是可选字段（规格 §9.1：禁止裸数字冒充实时完整数据）。
	Freshness freshnessBody `json:"freshness"`
}

type requestMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	// OriginalBytes 让界面说得出「只显示了 X / 共 Y」。
	// 一个不声张的截断比不显示更危险——人会以为看到的就是全部。
	OriginalBytes int64 `json:"original_bytes"`
}

type requestPayload struct {
	Body          string `json:"body"`
	Truncated     bool   `json:"truncated"`
	OriginalBytes int64  `json:"original_bytes"`
	ContentType   string `json:"content_type"`
}

// requestContentBody 是单条请求的完整内容（高敏）。
type requestContentBody struct {
	Summary  requestSummaryItem `json:"summary"`
	Messages []requestMessage   `json:"messages"`
	// MessagesParsed 区分「请求体里就没有消息」与「我们没解析出来」——
	// 后者要让人去看原文，前者不用。
	MessagesParsed      bool           `json:"messages_parsed"`
	FinalReply          string         `json:"final_reply"`
	FinalReplyTruncated bool           `json:"final_reply_truncated"`
	FinalReplyBytes     int64          `json:"final_reply_bytes"`
	RawRequest          requestPayload `json:"raw_request"`
	RawResponse         requestPayload `json:"raw_response"`
	// DataSource 与列表页同义：fake 时为 reqlog-fake。详情页也要挂横幅——
	// 恰恰是这一页最容易被当成真实用户问答截图转发出去。
	DataSource string        `json:"data_source"`
	Freshness  freshnessBody `json:"freshness"`
}

// requestLogStalenessThresholdSeconds 是这条通道的新鲜度阈值。
//
// reqlog 是抄录器而不是采集任务：这里的「观测时刻」就是我们向它发问的时刻，
// 所以正常情况下 staleness 恒等于零、状态恒为 fresh。阈值仍要有且要小——
// 它守的不是上游的采集周期，而是**我们自己这条链路**：一个耗时超过一分钟
// 才返回的读取，屏幕上那批数字已经不能当「刚刚」用了。
const requestLogStalenessThresholdSeconds int32 = 60

// freshnessFromSnapshot 把连接器的快照折算成统一的新鲜度形状。
//
// 状态优先级与 ops.Observation.Freshness 保持一致（stale 先于 partial）：
// 两处若给出不同的优先级，同一个徽章在两个页面上会对同一种情况说两种话。
// 这里天然产生不了 failed / uninitialized——读失败根本不会走到这个函数
// （错误已经在 Service 层翻译成了 HTTP 错误），而「从未采集」对一次实时读取
// 不是一种可能的状态。
func freshnessFromSnapshot(snap reqlog.Snapshot, now time.Time) freshnessBody {
	observedAt := snap.ObservedAt.UTC()
	staleness := int64(now.Sub(observedAt).Seconds())
	if staleness < 0 {
		// 上游时钟比我们快时会出现负数。夹到 0 而不是原样返回：
		// 一个「负 3 秒前的数据」会让人以为界面坏了，而它其实只是时钟偏移
		staleness = 0
	}
	body := freshnessBody{
		StalenessSeconds: &staleness,
		ThresholdSeconds: requestLogStalenessThresholdSeconds,
		IsPartial:        snap.IsPartial,
		ObservedAt:       rfc3339Ptr(&observedAt),
		LastSuccess:      rfc3339Ptr(&observedAt),
	}
	switch {
	case staleness >= int64(requestLogStalenessThresholdSeconds):
		body.State = "stale"
	case snap.IsPartial:
		body.State = "partial"
	default:
		body.State = "fresh"
	}
	return body
}

func toSummaryItem(s reqlog.RequestLogSummary) (requestSummaryItem, error) {
	var billedAmount *requestBilledAmount
	if s.BilledAmount != nil {
		if err := s.BilledAmount.Validate(); err != nil {
			return requestSummaryItem{}, fmt.Errorf("invalid billed amount: %w", err)
		}
		billedAmount = &requestBilledAmount{
			AmountMinor: strconv.FormatInt(s.BilledAmount.AmountMinor, 10),
			Currency:    s.BilledAmount.Currency,
			Scale:       s.BilledAmount.Scale,
		}
	}
	return requestSummaryItem{
		ID:     s.ID,
		Source: s.Source,
		// 时间一律 UTC（宪法 14 条），本地化交给前端
		OccurredAt:        s.OccurredAt.UTC().Format(time.RFC3339),
		Username:          s.Username,
		TokenPrefix:       s.TokenPrefix,
		Model:             s.Model,
		Channel:           s.Channel,
		Upstream:          s.Upstream,
		Status:            s.Status,
		DurationMS:        s.DurationMS,
		TTFBMS:            s.TTFBMS,
		TokensIn:          s.TokensIn,
		TokensOut:         s.TokensOut,
		TokensCache:       s.TokensCache,
		BilledAmount:      billedAmount,
		Stream:            s.Stream,
		UpstreamRequestID: s.UpstreamRequestID,
		ClientIP:          s.ClientIP,
	}, nil
}

// parseTimeParam 解析 RFC3339 时间参数；缺省返回零值。
//
// 解析失败当成参数错（400）而不是静默忽略：静默忽略会把一次
// 「只看今天」的查询悄悄变成「看全部」，而返回的条数看起来完全正常。
func parseTimeParam(r *http.Request, name string) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, action.NewError(action.CodeInvalidParams,
			name+" 必须是 RFC3339 时间（如 2026-08-28T09:00:00Z）", err)
	}
	return t.UTC(), nil
}

func parseStatusParam(r *http.Request) (reqlog.StatusFilter, error) {
	raw := reqlog.StatusFilter(strings.TrimSpace(r.URL.Query().Get("status")))
	switch raw {
	case reqlog.StatusAny, reqlog.StatusSuccess, reqlog.StatusError:
		return raw, nil
	default:
		return "", action.NewError(action.CodeInvalidParams,
			"status 只接受 success / error 或留空", nil)
	}
}

func parseLimitParam(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil // 0 交给连接器用默认页大小
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是非负整数", err)
	}
	return v, nil
}

// ListPlatformRequestsHandler 列出某平台的请求**元数据**（不含正文）。
//
// 环境范围：**不接受 environment 查询参数**，一律用 Principal 的环境——
// 与审计事件同一档处理，理由也相同：这批数据逐条可还原一个人的使用轨迹，
// 多一个可写的入参就多一个将来被放宽成「跨环境看板」的口子（规格 §20.5）。
//
// 说得更具体些：reqlog 是一个部署在特定环境里的抄录器，本进程只连得到自己
// 那一个。所谓「跨环境读取」在这条链路上连物理可能性都没有，因此把
// environment 做成参数只会制造一个看起来能用、实际永远无效的旋钮。
//
// 权限（requestlog.ScopeRead）由路由上的 RequireScope 判定，不在此处重复。
func ListPlatformRequestsHandler(q RequestLogQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		status, err := parseStatusParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		since, err := parseTimeParam(r, "since")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		until, err := parseTimeParam(r, "until")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseLimitParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		page, err := q.List(r.Context(), requestlog.ListInput{
			Platform: chi.URLParam(r, "platform"),
			Username: strings.TrimSpace(r.URL.Query().Get("username")),
			Model:    strings.TrimSpace(r.URL.Query().Get("model")),
			Status:   status,
			Since:    since,
			Until:    until,
			Limit:    limit,
			Cursor:   strings.TrimSpace(r.URL.Query().Get("cursor")),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}

		items := make([]requestSummaryItem, 0, len(page.Items))
		for _, s := range page.Items {
			item, err := toSummaryItem(s)
			if err != nil {
				WriteError(w, r, action.NewError(action.CodeInternal,
					"请求计费数据格式异常", err))
				return
			}
			items = append(items, item)
		}
		WriteJSON(w, http.StatusOK, requestPage{
			Items: items,
			Stats: requestStats{
				RequestCount:      page.Stats.RequestCount,
				SuccessCount:      page.Stats.SuccessCount,
				FailureCount:      page.Stats.FailureCount,
				AverageDurationMS: page.Stats.AverageDurationMS,
			},
			NextCursor:    page.NextCursor,
			RetentionDays: page.RetentionDays,
			DataSource:    page.Instance,
			Freshness:     freshnessFromSnapshot(page.Snapshot, time.Now().UTC()),
		})
	}
}

// GetPlatformRequestContentHandler 读取单条请求的完整内容（高敏）。
//
// 三条与列表端点不同的纪律：
//
//	权限更高一档 → requestlog.ScopeContentRead（路由上判）；
//	每次落审计   → Service.Content 负责，写不进去就不返回内容；
//	不缓存       → /api/v1 整组已有 NoStore（XM-0031），本端点因此天然继承。
//	              这不是"顺带"：正文一旦进了浏览器磁盘缓存或中间代理，
//	              §9.4「默认禁止导出」就成了一句只在按钮层面成立的话。
//
// `reason` 是可选查询参数，原样写进审计事件的 reason 列。做成可选而不是必填，
// 是因为「每看一条都要打字」是一条会改变日常操作手感的产品决定
// （见 requestlog.ContentInput.Reason 的说明）。
func GetPlatformRequestContentHandler(q RequestLogQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}

		content, err := q.Content(r.Context(), requestlog.ContentInput{
			Principal: p,
			Platform:  chi.URLParam(r, "platform"),
			ID:        chi.URLParam(r, "requestID"),
			Reason:    strings.TrimSpace(r.URL.Query().Get("reason")),
			// 请求 ID 取自 RequestID 中间件，与访问日志、错误响应里的是同一个：
			// 审计事件因此能和那两处对上（交接文档 §9.4 要求记录 request_id）
			RequestID: RequestIDFrom(r.Context()),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}

		messages := make([]requestMessage, 0, len(content.Messages))
		for _, m := range content.Messages {
			messages = append(messages, requestMessage{
				Role: m.Role, Content: m.Content,
				Truncated: m.Truncated, OriginalBytes: m.OriginalBytes,
			})
		}
		summary, err := toSummaryItem(content.Summary)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInternal,
				"请求计费数据格式异常", err))
			return
		}
		WriteJSON(w, http.StatusOK, requestContentBody{
			Summary:             summary,
			Messages:            messages,
			MessagesParsed:      content.MessagesParsed,
			FinalReply:          content.FinalReply,
			FinalReplyTruncated: content.FinalReplyTruncated,
			FinalReplyBytes:     content.FinalReplyBytes,
			RawRequest:          toPayload(content.RawRequest),
			RawResponse:         toPayload(content.RawResponse),
			DataSource:          content.Instance,
			Freshness:           freshnessFromSnapshot(content.Snapshot, time.Now().UTC()),
		})
	}
}

func toPayload(p reqlog.RawPayload) requestPayload {
	return requestPayload{
		Body: p.Body, Truncated: p.Truncated,
		OriginalBytes: p.OriginalBytes, ContentType: p.ContentType,
	}
}
