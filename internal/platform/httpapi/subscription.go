package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 订阅成本批次与代理资产的只读端点（XM-0037c，设计稿 §2.5/§3.5 的 Query 侧）。
//
// **没有写路径**：登记、退款、终止都是 L1 Action，走
// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决（宪法 2 条）。

// SubscriptionLister 是订阅付款的只读查询能力（*finance.SubscriptionStore 满足）。
//
// 两个方法各自回 truncated：区间被悄悄截断会被读成「这个账号只有这几笔付款」
// （宪法 12 条，同 ProfitDailyLister 的理由）。
type SubscriptionLister interface {
	ListBatches(ctx context.Context, q finance.SubscriptionBatchQuery) ([]finance.BatchWithLoss, bool, error)
	ListProxies(ctx context.Context, environment string, limit int32) ([]finance.ProxyWithLoss, bool, error)
}

// amortizationLossItem 是已结转的损失（§12 拍板的单列科目）。
type amortizationLossItem struct {
	// Amount 有符号：负值 = 终止后又收到大额退款产生的贷记，不是错误。
	Amount moneyItem `json:"amount"`
	// BookedOn 是结转日（YYYY-MM-DD）= 主体的 terminated_on。
	BookedOn string `json:"booked_on"`
}

// subscriptionBatchItem 是一笔订阅批次的对外形状。
type subscriptionBatchItem struct {
	ID                string `json:"id"`
	UpstreamAccountID string `json:"upstream_account_id"`

	// 三个原始金额，形状是 UI 交接 §13 的 Money（amount_minor 是字符串）。
	Paid      moneyItem `json:"paid"`
	Surcharge moneyItem `json:"surcharge"`
	Refunded  moneyItem `json:"refunded"`

	// CostBasis = 实付 + 附加 − 退款（§3.5 的摊销基础）。
	//
	// 后端算好而不是让前端减三个数：那个减法一旦散到几个页面，
	// 迟早有一处忘了减退款，而结果看起来完全正常。
	CostBasis moneyItem `json:"cost_basis"`
	// AccountShare 是**本账号**要承担的那一份（基础 ÷ account_count，半进）。
	AccountShare moneyItem `json:"account_share"`

	// DailyAmortization 是**今天**该摊的金额；今天不在期内时为 null。
	//
	// 由后端算而不是让前端按公式复现（§3.5 有两次除法、末日吸收、
	// 退款分段三条口径）：两处实现迟早在某一天差一个微单位，
	// 而那种差异既不报错也查不出来。
	//
	// 「今天」按平台默认的 CST +08:00 算（★口径常量 §4）。逐账号的
	// business_day_tz 可以不同，但那是台账入账时用的；这个字段是列表页
	// 的一个即时读数，用平台默认足够，且它是**派生值不是台账**。
	DailyAmortization *moneyItem `json:"daily_amortization"`

	Currency string `json:"currency"`

	// StartsOn / ExpiresOn 是自然日（YYYY-MM-DD），**含两端**（§12 拍板）。
	// 不是 RFC3339 时刻：渲染成带时区的时间戳会让前端按浏览器时区再解释一次。
	StartsOn  string `json:"starts_on"`
	ExpiresOn string `json:"expires_on"`
	// EffectiveDays = ExpiresOn − StartsOn + 1。显式给出来，
	// 免得前端自己减一遍时忘了那个 +1（§12 拍板的含两端）。
	EffectiveDays int `json:"effective_days"`

	// RefundedOn / TerminatedOn 为 null 表示「没有」，不是空串。
	RefundedOn   *string `json:"refunded_on"`
	TerminatedOn *string `json:"terminated_on"`

	AccountCount int `json:"account_count"`
	// ProxyAssetID 为 null = 这批订阅不走代理，代理成本为 0（§2.5）。
	ProxyAssetID *string `json:"proxy_asset_id"`

	// AmortizationLoss 为 null = 尚未终止，没有结转过损失。
	//
	// null 而不是一个 0：一个「损失恰好为 0」的批次（在到期前一天终止、
	// 末日金额为 0）与一个还在正常摊销的批次，在报表上不是一回事。
	AmortizationLoss *amortizationLossItem `json:"amortization_loss"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// proxyAssetItem 是一份代理资产的对外形状。
type proxyAssetItem struct {
	ID string `json:"id"`

	Paid              moneyItem  `json:"paid"`
	Surcharge         moneyItem  `json:"surcharge"`
	Refunded          moneyItem  `json:"refunded"`
	CostBasis         moneyItem  `json:"cost_basis"`
	AccountShare      moneyItem  `json:"account_share"`
	DailyAmortization *moneyItem `json:"daily_amortization"`

	Currency string `json:"currency"`

	OpenedOn      string  `json:"opened_on"`
	ExpiresOn     string  `json:"expires_on"`
	EffectiveDays int     `json:"effective_days"`
	RefundedOn    *string `json:"refunded_on"`
	TerminatedOn  *string `json:"terminated_on"`

	SharedAccountCount int `json:"shared_account_count"`

	BuyPlatform string `json:"buy_platform"`
	BuyAddress  string `json:"buy_address"`
	// CredentialRef 是**引用**不是凭据（ADR-014、UI 交接 §14.2）。
	// 代理的账号密码从不进库，更不会出现在这里。
	CredentialRef string `json:"credential_ref"`

	// Mounted=false ⇒ DailyAmortization 是**已知的 0**（§10.3），不是未知。
	// 两个字段一起给，前端才说得出「这份代理今天没在服务」而不是「没算出来」。
	Mounted bool `json:"mounted"`

	Environment string `json:"environment"`

	AmortizationLoss *amortizationLossItem `json:"amortization_loss"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type subscriptionBatchPage struct {
	Items     []subscriptionBatchItem `json:"items"`
	Truncated bool                    `json:"truncated"`
	Limit     int32                   `json:"limit"`
	// AsOf 是 DailyAmortization 所指的那个业务日，回显出来才可解释
	// （宪法 12 条：一个没有日期的「今日成本」是个裸数字）。
	AsOf string `json:"as_of"`
}

type proxyAssetPage struct {
	Items     []proxyAssetItem `json:"items"`
	Truncated bool             `json:"truncated"`
	Limit     int32            `json:"limit"`
	AsOf      string           `json:"as_of"`
}

func moneyValue(minor int64, currency string) moneyItem {
	return moneyItem{
		AmountMinor: strconv.FormatInt(minor, 10),
		Currency:    currency,
		Scale:       financeAmountScale,
	}
}

func dayPtr(d time.Time) *string {
	if d.IsZero() {
		return nil
	}
	s := d.Format(finance.ProfitBusinessDayLayout)
	return &s
}

func lossItem(minor *int64, bookedOn time.Time, currency string) *amortizationLossItem {
	if minor == nil {
		return nil
	}
	return &amortizationLossItem{
		Amount:   moneyValue(*minor, currency),
		BookedOn: bookedOn.Format(finance.ProfitBusinessDayLayout),
	}
}

// dailyAmortizationItem 算某一天该摊的金额；那天不在期内时返回 nil。
//
// 算不出来（字段自相矛盾，理论上被库层 CHECK 挡住了）时同样返回 nil：
// 一个列表端点不该因为某一行的历史数据有问题就整页 500，
// 但它也绝不能编一个数出来——空位是诚实的（宪法 12 条）。
func dailyAmortizationItem(
	term finance.AmortizationTerm, day time.Time, currency string,
) *moneyItem {
	amount, covered, err := term.DailyMinor(day)
	if err != nil || !covered {
		return nil
	}
	item := moneyValue(amount, currency)
	return &item
}

func batchToItem(b finance.BatchWithLoss, today time.Time) subscriptionBatchItem {
	term := b.Batch.Term()
	basis, _ := term.NetBasisMinor()
	share, _ := term.ShareMinor()

	item := subscriptionBatchItem{
		ID:                b.Batch.ID.String(),
		UpstreamAccountID: b.Batch.UpstreamAccountID.String(),
		Paid:              moneyValue(b.Batch.PaidMinor, b.Batch.Currency),
		Surcharge:         moneyValue(b.Batch.SurchargeMinor, b.Batch.Currency),
		Refunded:          moneyValue(b.Batch.RefundedMinor, b.Batch.Currency),
		CostBasis:         moneyValue(basis, b.Batch.Currency),
		AccountShare:      moneyValue(share, b.Batch.Currency),
		DailyAmortization: dailyAmortizationItem(term, today, b.Batch.Currency),
		Currency:          b.Batch.Currency,
		StartsOn:          b.Batch.StartsOn.Format(finance.ProfitBusinessDayLayout),
		ExpiresOn:         b.Batch.ExpiresOn.Format(finance.ProfitBusinessDayLayout),
		EffectiveDays:     term.TotalDays(),
		RefundedOn:        dayPtr(b.Batch.RefundedOn),
		TerminatedOn:      dayPtr(b.Batch.TerminatedOn),
		AccountCount:      b.Batch.AccountCount,
		AmortizationLoss:  lossItem(b.LossMinor, b.LossBookedOn, b.Batch.Currency),
		CreatedAt:         b.Batch.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         b.Batch.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if b.Batch.ProxyAssetID != uuid.Nil {
		id := b.Batch.ProxyAssetID.String()
		item.ProxyAssetID = &id
	}
	return item
}

func proxyToItem(p finance.ProxyWithLoss, today time.Time) proxyAssetItem {
	term := p.Proxy.Term()
	basis, _ := term.NetBasisMinor()
	share, _ := term.ShareMinor()

	// 走 ProxyAsset.DailyMinor 而不是 term.DailyMinor：只有前者知道
	// 「未挂载 = 已知的 0」（§10.3）。用后者会让一份没挂上的代理
	// 在列表里显示成正常摊销中。
	var daily *moneyItem
	if amount, covered, err := p.Proxy.DailyMinor(today); err == nil && covered {
		item := moneyValue(amount, p.Proxy.Currency)
		daily = &item
	}

	return proxyAssetItem{
		ID:                 p.Proxy.ID.String(),
		Paid:               moneyValue(p.Proxy.PaidMinor, p.Proxy.Currency),
		Surcharge:          moneyValue(p.Proxy.SurchargeMinor, p.Proxy.Currency),
		Refunded:           moneyValue(p.Proxy.RefundedMinor, p.Proxy.Currency),
		CostBasis:          moneyValue(basis, p.Proxy.Currency),
		AccountShare:       moneyValue(share, p.Proxy.Currency),
		DailyAmortization:  daily,
		Currency:           p.Proxy.Currency,
		OpenedOn:           p.Proxy.OpenedOn.Format(finance.ProfitBusinessDayLayout),
		ExpiresOn:          p.Proxy.ExpiresOn.Format(finance.ProfitBusinessDayLayout),
		EffectiveDays:      term.TotalDays(),
		RefundedOn:         dayPtr(p.Proxy.RefundedOn),
		TerminatedOn:       dayPtr(p.Proxy.TerminatedOn),
		SharedAccountCount: p.Proxy.SharedAccountCount,
		BuyPlatform:        p.Proxy.BuyPlatform,
		BuyAddress:         p.Proxy.BuyAddress,
		CredentialRef:      p.Proxy.CredentialRef,
		Mounted:            p.Proxy.Mounted,
		Environment:        p.Proxy.Environment,
		AmortizationLoss:   lossItem(p.LossMinor, p.LossBookedOn, p.Proxy.Currency),
		CreatedAt:          p.Proxy.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          p.Proxy.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ListSubscriptionBatchesHandler 返回某环境（可选：某账号）的订阅成本批次。
//
// 复用 finance.ScopeRead，不另立 scope：批次里的金额就是这条渠道成本的来源，
// 能看台账里那个成本的人已经知道它的量级，泄漏面完全相同
// （同利润台账复用 finance.read 的理由）。
func ListSubscriptionBatchesHandler(store SubscriptionLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		env, limit, err := resolveSubscriptionQuery(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		query := finance.SubscriptionBatchQuery{Environment: env, Limit: limit}
		if raw := strings.TrimSpace(r.URL.Query().Get("upstream_account_id")); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				WriteError(w, r, action.NewError(action.CodeInvalidParams,
					"upstream_account_id 不是合法 UUID", err))
				return
			}
			query.UpstreamAccountID = id
		}

		rows, truncated, err := store.ListBatches(r.Context(), query)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		today := finance.BusinessDayAt(time.Now().UTC(), finance.DefaultBusinessDayLocation())
		out := make([]subscriptionBatchItem, 0, len(rows))
		for _, row := range rows {
			out = append(out, batchToItem(row, today))
		}
		WriteJSON(w, http.StatusOK, subscriptionBatchPage{
			Items:     out,
			Truncated: truncated,
			Limit:     limit,
			AsOf:      today.Format(finance.ProfitBusinessDayLayout),
		})
	}
}

// ListProxyAssetsHandler 返回某环境的代理资产。
func ListProxyAssetsHandler(store SubscriptionLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		env, limit, err := resolveSubscriptionQuery(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		rows, truncated, err := store.ListProxies(r.Context(), env, limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		today := finance.BusinessDayAt(time.Now().UTC(), finance.DefaultBusinessDayLocation())
		out := make([]proxyAssetItem, 0, len(rows))
		for _, row := range rows {
			out = append(out, proxyToItem(row, today))
		}
		WriteJSON(w, http.StatusOK, proxyAssetPage{
			Items:     out,
			Truncated: truncated,
			Limit:     limit,
			AsOf:      today.Format(finance.ProfitBusinessDayLayout),
		})
	}
}

// resolveSubscriptionQuery 解析两个端点共用的环境与条数上限。
//
// 环境由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致——
// **不默认生产，也不允许跨环境读取**（宪法 15 条）。
func resolveSubscriptionQuery(r *http.Request) (string, int32, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return "", 0, action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	env, err := resolveEnvironment(r, p)
	if err != nil {
		return "", 0, err
	}
	limit, err := parseSubscriptionLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return "", 0, err
	}
	return string(env), limit, nil
}

// parseSubscriptionLimit 解析 limit 参数。
//
// 非法值一律 400 而不是悄悄取默认：`limit=abc` 静默变成 200 会让前端
// 拿着一份自以为完整的数据（同 parseProfitLimit 的理由）。
func parseSubscriptionLimit(raw string) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return finance.DefaultSubscriptionListLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是整数", err)
	}
	if value <= 0 || int32(value) > finance.MaxSubscriptionListLimit {
		return 0, action.NewError(action.CodeInvalidParams,
			"limit 必须在 1 与 "+strconv.Itoa(int(finance.MaxSubscriptionListLimit))+" 之间", nil)
	}
	return int32(value), nil
}
