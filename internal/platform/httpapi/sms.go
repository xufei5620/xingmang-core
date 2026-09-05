package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// SMSQuerier 是接码页需要的读能力。
type SMSQuerier interface {
	ListProviderStatus(ctx context.Context) ([]sms.ProviderStatus, error)
	ListResources(ctx context.Context, provider string, limit int) ([]sms.Resource, error)
	ListOperations(ctx context.Context, provider string, limit int) ([]sms.Operation, error)
	ListCodes(ctx context.Context, resourceID string, limit int) ([]sms.Code, error)
	// ListRoutingRules 是路由规则页的读端点（XM-SMS2 #5）。
	ListRoutingRules(ctx context.Context) ([]sms.RoutingRule, error)
	// LatestBalanceSnapshots 是巡检抓到的最新余额（XM-SMS2 #7）。
	LatestBalanceSnapshots(ctx context.Context) ([]sms.BalanceSnapshot, error)
	// AggregateCostsByDay 是成本统计页签的读端点（XM-SMS3 #3）。
	AggregateCostsByDay(ctx context.Context, from, to time.Time) ([]sms.CostAggregate, error)
	// 告警与阈值（XM-SMS2 #8）。**只读**：写走 sms.alert.* Action。
	ListOpenAlertEvents(ctx context.Context) ([]sms.AlertEvent, error)
	ListBalanceThresholds(ctx context.Context) ([]sms.BalanceThreshold, error)
}

// SMSCatalogReader 读库存。**实时上游调用，不是投影**——库存变化频繁，
// 而且只在人要挑商品时才有用，落库只会让页面显示一份过期的价格。
type SMSCatalogReader interface {
	ListCatalog(ctx context.Context, provider string, filter sms.CatalogFilter) ([]sms.CatalogItem, error)
	Providers() []string
}

type smsProviderItem struct {
	Provider string `json:"provider"`
	// Label / Capabilities 来自注册表（ADR-022）。页面按能力渲染按钮，
	// 不再按供应商名字判断；标签也不再在前端另写一份。
	Label        string   `json:"label"`
	Capabilities []string `json:"capabilities"`
	// Enabled 是**运营在后台开的开关**，与 Verified 分开报。
	//
	// 页面要能同时说清两件事：这家开没开（运营的意愿），以及凭据能不能用
	// （连接测试的事实）。合成一个「可用」布尔值，就分不出该去打开它还是
	// 该去修密钥了。
	Enabled    bool   `json:"enabled"`
	Verified   bool   `json:"verified"`
	VerifiedAt string `json:"verified_at,omitempty"`
	// ClientIP 是供应商观察到的我方出口 IP（只有 62 会回）。
	// 上游若做 IP 白名单，这个值对不上就是后续全部 403 的原因。
	ClientIP  string `json:"client_ip,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// SupportsLifecycle 说明这家有没有取消/延长那五个动作。
	// 页面据此决定显不显示那几个按钮——而不是按 provider 名字硬判。
	SupportsLifecycle bool `json:"supports_lifecycle"`
}

type smsResourceItem struct {
	ID       string `json:"resource_id"`
	Provider string `json:"provider"`
	// Phone 是完整号码，**只回给持有 sms.reveal 的调用方**。
	//
	// 号码在库里是明文列（本仓没有列加密工具，而卡面 PAN/CVV 已经是明文
	// 列），这道权限闸是「谁能看号码」剩下的唯一约束。
	Phone string `json:"phone,omitempty"`
	// 以下来自官方 ActivationSchema（XM-SMS1）。62 全部为空/0。
	Operator         string `json:"operator,omitempty"`
	PriceText        string `json:"price_text,omitempty"`
	VerificationType string `json:"verification_type,omitempty"`
	// Subtype：1 = 普通激活，2 = 租用。页面据此区分 20 分钟号与按小时租的号。
	Subtype          int64  `json:"subtype,omitempty"`
	CountryPhoneCode string `json:"country_phone_code,omitempty"`
	// State 是统一状态（waiting_code / code_received / finished / cancelled /
	// expired）；EffectiveState 把「待收码但已过期」算成 expired。Status 仍是
	// 上游原话，页面悬停时看。
	State          string `json:"state,omitempty"`
	EffectiveState string `json:"effective_state,omitempty"`
	// OperationID 是买下它的那笔操作（迁移 000044）；导入的号为空。
	OperationID string `json:"operation_id,omitempty"`
	PhoneMask   string `json:"phone_mask"`
	Service     string `json:"service,omitempty"`
	Country     string `json:"country,omitempty"`
	Status      string `json:"status,omitempty"`
	// **provider_token 任何情况下都不出现在这里。** 它是取码凭证，
	// 只在服务端取码那条路上被读一次。
	LastCodeAt string `json:"last_code_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	SyncedAt   string `json:"synced_at,omitempty"`
}

type smsOperationItem struct {
	ID       string `json:"operation_id"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	// ProviderRef 是上游订单/activation ID。unknown 时它是人去供应商侧
	// 对账的唯一抓手，所以哪怕操作失败也要回给页面。
	ProviderRef   string `json:"provider_ref,omitempty"`
	ResourceID    string `json:"resource_id,omitempty"`
	ParamsSummary string `json:"params_summary,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	NeedsReview   bool   `json:"needs_review"`
	ResolveNote   string `json:"resolve_note,omitempty"`
	// RetryAllowed 由**服务端**判定。前端不要按 state 自己推：
	// 迟早会推出一个「看起来该能重试」的不确定态，而重试就是再买一次。
	RetryAllowed bool   `json:"retry_allowed"`
	StartedAt    string `json:"started_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type smsCodeItem struct {
	ID string `json:"code_id"`
	// Code 只回给持有 sms.reveal 的调用方，与卡片 3DS 验证码同档：
	// 它是一次性密钥，拿到就能替人完成一次验证。
	Code       string `json:"code,omitempty"`
	ResourceID string `json:"resource_id"`
	Sender     string `json:"sender,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// ListSMSProvidersHandler 返回各家的验证状态与能力。
func ListSMSProvidersHandler(store SMSQuerier, configured []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListProviderStatus(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		byProvider := make(map[string]sms.ProviderStatus, len(rows))
		for _, s := range rows {
			byProvider[s.Provider] = s
		}

		// 以**已装配的清单**为准而不是以库里有的行为准：没做过连接测试的
		// 那家库里根本没有行，而它恰恰是最需要显示出来的——页面要告诉人
		// 「这家还没验证」，而不是当它不存在。
		out := make([]smsProviderItem, 0, len(configured))
		for _, id := range configured {
			st := byProvider[id]
			item := smsProviderItem{
				Provider:          id,
				Label:             sms.Label(id),
				Capabilities:      sms.Capabilities(id),
				Enabled:           st.Enabled,
				Verified:          st.Verified(),
				ClientIP:          st.ClientIP,
				LastError:         st.LastError,
				SupportsLifecycle: sms.SupportsAction(id, sms.KindCancel),
			}
			if st.Verified() {
				item.VerifiedAt = st.VerifiedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListSMSResourcesHandler 返回号码清单。
func ListSMSResourcesHandler(store SMSQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		maySeePhone := p.HasScope(sms.PermissionReveal)

		rows, err := store.ListResources(r.Context(), strings.TrimSpace(r.URL.Query().Get("provider")), 200)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsResourceItem, 0, len(rows))
		for _, res := range rows {
			item := smsResourceItem{
				ID: res.ID, Provider: res.Provider, PhoneMask: res.PhoneMask,
				Service: res.Service, Country: res.Country, Status: res.Status,
				LastCodeAt: formatUTC(res.LastCodeAt),
				ExpiresAt:  formatUTC(res.ExpiresAt),
				SyncedAt:   formatUTC(res.SyncedAt),
				Operator:   res.Operator, PriceText: res.PriceText,
				VerificationType: res.VerificationType, Subtype: res.Subtype,
				CountryPhoneCode: res.CountryPhoneCode,
				State:            string(res.State),
				EffectiveState:   string(res.EffectiveState(time.Now())),
				OperationID:      res.OperationID,
			}
			if maySeePhone {
				item.Phone = res.Phone
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListSMSOperationsHandler 返回操作台账。
func ListSMSOperationsHandler(store SMSQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListOperations(r.Context(), strings.TrimSpace(r.URL.Query().Get("provider")), 100)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsOperationItem, 0, len(rows))
		for _, op := range rows {
			out = append(out, smsOperationItem{
				ID: op.ID, Provider: op.Provider, Kind: op.Kind, State: string(op.State),
				ProviderRef: op.ProviderRef, ResourceID: op.ResourceID,
				ParamsSummary: op.ParamsSummary, FailureReason: op.FailureReason,
				NeedsReview: op.NeedsHumanReview, ResolveNote: op.ResolveNote,
				RetryAllowed: op.RetryAllowed(),
				StartedAt:    formatUTC(op.StartedAt), UpdatedAt: formatUTC(op.UpdatedAt),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListSMSCodesHandler 返回验证码。
func ListSMSCodesHandler(store SMSQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		// 与卡片 3DS 验证码同一条纪律：只有 sms.reveal 看得到码本身；
		// 只有 sms.read 的调用方看得到「这个号收到过几条」，看不到内容。
		maySeeCode := p.HasScope(sms.PermissionReveal)

		rows, err := store.ListCodes(r.Context(), strings.TrimSpace(chi.URLParam(r, "resourceID")), 50)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsCodeItem, 0, len(rows))
		for _, c := range rows {
			item := smsCodeItem{
				ID: c.ID, ResourceID: c.ResourceID, Sender: c.Sender,
				ReceivedAt: formatUTC(c.ReceivedAt), CreatedAt: formatUTC(c.CreatedAt),
			}
			if maySeeCode {
				item.Code = c.Code
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListSMSCatalogHandler 读库存。**实时上游调用。**
func ListSMSCatalogHandler(reader SMSCatalogReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		provider := strings.TrimSpace(query.Get("provider"))
		if provider == "" {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "必须指定 provider", nil))
			return
		}
		items, err := reader.ListCatalog(r.Context(), provider, sms.CatalogFilter{
			PlatformID: strings.TrimSpace(query.Get("platform_id")),
			Country:    strings.TrimSpace(query.Get("country")),
			Service:    strings.TrimSpace(query.Get("service")),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, map[string]any{
				"id": it.ID, "name": it.Name, "service": it.Service, "country": it.Country,
				// 三个价格分开回：它们是**不同的事实**（Hero 的
				// default/retail/min），合并成一个「价格」会让人按一个
				// 不成立的数字做预算。缺失就是缺失，前端显示「—」。
				"default_price": it.DefaultPrice,
				"retail_price":  it.RetailPrice,
				"minimum_price": it.MinimumPrice,
				"available":     it.Available,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ---- 路由规则（XM-SMS2 #5）----

type smsRoutingRuleItem struct {
	ID      string `json:"rule_id"`
	Service string `json:"service"`
	Country string `json:"country"`
	// Providers 是优先级顺序。
	Providers []string `json:"providers"`
	// MaxUnitPrice 是十进制文本；空 = 不限。按各家自己的币种比较。
	MaxUnitPrice string `json:"max_unit_price,omitempty"`
	Enabled      bool   `json:"enabled"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// ListSMSRoutingRulesHandler 返回本环境全部路由规则（含停用的）以及没有规则时的
// 默认顺序——页面要能告诉人「现在没规则，要号会按这个顺序试」。
func ListSMSRoutingRulesHandler(store SMSQuerier, configured []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := store.ListRoutingRules(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsRoutingRuleItem, 0, len(rules))
		for _, rule := range rules {
			item := smsRoutingRuleItem{
				ID: rule.ID, Service: rule.Service, Country: rule.Country,
				Providers:    append([]string{}, rule.Providers...),
				MaxUnitPrice: rule.MaxUnitPriceText, Enabled: rule.Enabled,
			}
			if !rule.UpdatedAt.IsZero() {
				item.UpdatedAt = rule.UpdatedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items":         out,
			"default_order": append([]string{}, configured...),
			"wildcard":      sms.RouteAny,
		})
	}
}

// ---- 余额快照（XM-SMS2 #7）----

type smsBalanceItem struct {
	Provider string `json:"provider"`
	// Amount 是十进制文本（金额不过 float）。
	Amount string `json:"amount"`
	// Currency 空 = 上游没说。分币种不折算。
	Currency string `json:"currency,omitempty"`
	TakenAt  string `json:"taken_at"`
}

// ListSMSBalancesHandler 返回每家最新一条余额快照。
//
// **是快照不是实时值**：它由 platform-worker 每 10 分钟抓一次。页面必须把抓取
// 时间一起显示出来——一个不知道什么时候抓的余额，会让人以为刚刚还有钱。
func ListSMSBalancesHandler(store SMSQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.LatestBalanceSnapshots(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsBalanceItem, 0, len(rows))
		for _, row := range rows {
			out = append(out, smsBalanceItem{
				Provider: row.Provider, Amount: row.AmountText, Currency: row.Currency,
				TakenAt: row.TakenAt.UTC().Format(time.RFC3339),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ---- 告警与阈值（XM-SMS2 #8）----

type smsAlertItem struct {
	ID       string `json:"alert_id"`
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	// FirstSeenAt 是「这件事从什么时候开始的」——判断它拖了多久的唯一依据。
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

type smsThresholdItem struct {
	Provider string `json:"provider"`
	// MinAmount 是十进制文本，按该家自己的币种比较、不折算。
	MinAmount string `json:"min_amount"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ListSMSAlertsHandler 返回还开着的告警与已配的余额阈值。
//
// 一次返回两样：页面顶部的红条要显示告警，而供应商卡片上的阈值输入框要显示
// 当前配的值——两次请求没有意义，它们总是一起用。
//
// **不外发**：这里只有读。投递等另一条线的通知规范定稿。
func ListSMSAlertsHandler(store SMSQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, err := store.ListOpenAlertEvents(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		thresholds, err := store.ListBalanceThresholds(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items := make([]smsAlertItem, 0, len(events))
		for _, ev := range events {
			items = append(items, smsAlertItem{
				ID: ev.ID, Kind: ev.Kind, Provider: ev.Provider, Subject: ev.Subject,
				Severity: ev.Severity, Summary: ev.Summary,
				FirstSeenAt: ev.FirstSeenAt.UTC().Format(time.RFC3339),
				LastSeenAt:  ev.LastSeenAt.UTC().Format(time.RFC3339),
			})
		}
		rows := make([]smsThresholdItem, 0, len(thresholds))
		for _, t := range thresholds {
			row := smsThresholdItem{Provider: t.Provider, MinAmount: t.MinAmountText}
			if !t.UpdatedAt.IsZero() {
				row.UpdatedAt = t.UpdatedAt.UTC().Format(time.RFC3339)
			}
			rows = append(rows, row)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items, "thresholds": rows})
	}
}

// ---- 成本统计（XM-SMS3 #3）----

type smsCostRow struct {
	Day      string `json:"day"`
	Provider string `json:"provider"`
	// Currency 空 = 上游没说。**分币种不折算**：两种币种加到一起得到的数字
	// 看起来像总成本，其实什么都不是。
	Currency string `json:"currency,omitempty"`
	Service  string `json:"service,omitempty"`
	// Amount 是十进制文本（退款为负）。
	Amount string `json:"amount"`
	Count  int    `json:"count"`
	// UnknownCount 是金额未知的笔数。前端必须显示：一份「这个月花了 X」的
	// 报表，背后如果有二十笔金额不明，那个 X 是下限不是花费。
	UnknownCount int `json:"unknown_count"`
}

// 统计窗口的上限：一次最多回 366 天。
//
// 不是性能顾虑（聚合在库里做），是**页面可读性**：一张摊开三年的表没人看得懂，
// 而真要那么长的区间应该走导出而不是页面。
const maxSMSCostWindowDays = 366

// ListSMSCostsHandler 返回按供应商 × 币种 × 服务 × 天聚合的成本。
//
// 聚合在库里做（服务端整表聚合）：一个跑了半年的环境有几十万行成本事件，
// 拉到浏览器里再算既慢又会把「这个月花了多少」变成一个前端 bug。
func ListSMSCostsHandler(store SMSQuerier, now func() time.Time) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
	return func(w http.ResponseWriter, r *http.Request) {
		to := now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
		from := to.AddDate(0, 0, -30)
		if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
			parsed, err := time.Parse("2006-01-02", raw)
			if err != nil {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "from 要是 YYYY-MM-DD", err))
				return
			}
			from = parsed.UTC()
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
			parsed, err := time.Parse("2006-01-02", raw)
			if err != nil {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "to 要是 YYYY-MM-DD", err))
				return
			}
			// 右端点含当天：人说的「到 9 月 6 日」包括 9 月 6 日。
			to = parsed.UTC().AddDate(0, 0, 1)
		}
		if !to.After(from) {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "to 要不早于 from", nil))
			return
		}
		if to.Sub(from) > maxSMSCostWindowDays*24*time.Hour {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"一次最多查 366 天；更长的区间请分段查", nil))
			return
		}

		rows, err := store.AggregateCostsByDay(r.Context(), from, to)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]smsCostRow, 0, len(rows))
		for _, row := range rows {
			out = append(out, smsCostRow{
				Day: row.Day, Provider: row.Provider, Currency: row.Currency, Service: row.Service,
				Amount: row.SumText, Count: row.Count, UnknownCount: row.UnknownCount,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out,
			"from":  from.Format("2006-01-02"),
			// 回给页面的 to 是**含当天**的那一天，与人说的日期一致。
			"to": to.AddDate(0, 0, -1).Format("2006-01-02"),
		})
	}
}
