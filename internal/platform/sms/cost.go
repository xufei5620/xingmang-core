package sms

import (
	"context"
	"strings"
	"time"
)

// 成本事件（ADR-022 决策 5，XM-SMS3 #1）。
//
// **在成功那一刻写**，而不是事后从资源表反推：延长、重激活、退款都不改资源
// 单价，只有事件能记下每一分钱的来龙去脉。这张事实表的形状就是跨平台财务的
// 输入（按供应商 × 币种 × 服务 × 天聚合），所以服务与国家要落在事件上——
// 事后从资源表 join 回去，那些被取消或过期的号会把维度带偏。
//
// **金额未知就是空（库里 NULL），不是 0**：62 买号只回订单 ID，金额要事后从
// 订单列表补；Hero 的延长写体不回价格。一个补出来的 0 会让「这个月花了多少」
// 少算一笔，而少算比缺一行更难发现——缺一行至少还能对上余额差。
const (
	CostPurchase      = "purchase"
	CostRent          = "rent"
	CostProlong       = "prolong"
	CostReactivate    = "reactivate"
	CostEmailPurchase = "email_purchase"
	CostEmailReorder  = "email_reorder"
	// CostRefund 是负数：Hero 取消会把钱退回来。
	CostRefund = "refund"
)

// 金额的来源。排查时第一个要问的问题是「这个数是谁说的」。
const (
	// CostSourceUpstreamPrice：上游在这次响应里报的价（Hero 的 activation.price、
	// 邮箱的 cost）。
	CostSourceUpstreamPrice = "upstream_price"
	// CostSourcePendingOrder：62 买号只回订单 ID，金额要事后从订单列表补。
	CostSourcePendingOrder = "pending_order_amount"
	// CostSourceRefundOfPrice：按我们当初被收的价退（上游取消不回退款额）。
	CostSourceRefundOfPrice = "refund_of_price"
	// CostSourceUpstreamSilent：上游写体根本不回价格（延长 / 重激活）。
	CostSourceUpstreamSilent = "upstream_silent"
)

// CurrencyUSD 是 62 的记账币种（ADR-022 口径）。
//
// 上游不回币种字段，这个标签是我们按平台性质定的；哪天证明它不是 USD，
// 改这一处即可（**不折算**，统计按币种分列，所以标错不会污染别的币种）。
const CurrencyUSD = "USD"

// CostEvent 是一笔花费（或退款）。
type CostEvent struct {
	ID string
	// OperationID 是产生它的那笔操作；与 Subject 一起构成幂等键。
	OperationID string
	Provider    string
	Kind        string
	// Subject 是这笔花费落在哪个东西上：号码 ID、邮箱 ID，或空（整单）。
	Subject string
	// ProviderRef 是上游引用（62 的订单号），事后补金额靠它。
	ProviderRef string
	Service     string
	Country     string
	// AmountText 是十进制文本，**空 = 上游没说**（库里 NULL），不是 0。
	// 退款是负数。
	AmountText string
	// Currency 空 = 上游没说。分币种不折算。
	Currency     string
	AmountSource string
	OccurredAt   time.Time
}

// costKindFor 把操作类型翻成成本事件类型；不花钱的动作返回空。
func costKindFor(opKind string) string {
	switch opKind {
	case KindPurchase:
		return CostPurchase
	case KindRent:
		return CostRent
	case KindProlong:
		return CostProlong
	case KindReactivate:
		return CostReactivate
	case KindEmailPurchase:
		return CostEmailPurchase
	case KindEmailReorder:
		return CostEmailReorder
	case KindCancel:
		// 取消不花钱，**退钱**：记一条负数。
		return CostRefund
	}
	return ""
}

// recordCosts 写成本事件。**只在上游成功之后调用。**
//
// 写失败只吞掉不改操作状态：钱已经花了、资源也落库了，把一笔成功的操作翻成
// 失败或 unknown 会诱使人再买一次——那是比少一行账更贵的错误。少掉的行由余额
// 对账（XM-SMS3 #2）兜底：快照差与事件和对不上就会报出来。
func (s *Service) recordCosts(ctx context.Context, events []CostEvent) {
	if len(events) == 0 {
		return
	}
	_, _ = s.store.AppendCostEvents(ctx, events)
}

// purchaseCostEvents 为一次买号 / 租用造事件。
//
// 上游逐个报价（Hero）就一个号一条：统计要按服务聚合，而同一笔操作里可以有
// 不同服务的号。上游只回订单号（62）就一条整单事件、金额留空。
func (s *Service) purchaseCostEvents(op Operation, kind string, resources []Resource, resourceIDs []string) []CostEvent {
	now := s.now()
	priced := make([]CostEvent, 0, len(resources))
	for i, r := range resources {
		if strings.TrimSpace(r.PriceText) == "" {
			continue
		}
		subject := r.ID
		if subject == "" && i < len(resourceIDs) {
			subject = resourceIDs[i]
		}
		priced = append(priced, CostEvent{
			OperationID: op.ID, Provider: op.Provider, Kind: kind, Subject: subject,
			ProviderRef: op.ProviderRef, Service: r.Service, Country: r.Country,
			AmountText: r.PriceText, Currency: currencyFor(op.Provider),
			AmountSource: CostSourceUpstreamPrice, OccurredAt: now,
		})
	}
	if len(priced) > 0 {
		return priced
	}
	// 一个价都没有：记一条整单事件，金额等对账补。
	service, country := "", ""
	if len(resources) > 0 {
		service, country = resources[0].Service, resources[0].Country
	}
	return []CostEvent{{
		OperationID: op.ID, Provider: op.Provider, Kind: kind, Subject: "",
		ProviderRef: op.ProviderRef, Service: service, Country: country,
		Currency: currencyFor(op.Provider), AmountSource: CostSourcePendingOrder, OccurredAt: now,
	}}
}

// emailCostEvents 为买邮箱 / 重下单造事件。邮箱的 cost 与 currency 都是上游给的。
func (s *Service) emailCostEvents(op Operation, kind string, emails []Email) []CostEvent {
	now := s.now()
	out := make([]CostEvent, 0, len(emails))
	for _, e := range emails {
		out = append(out, CostEvent{
			OperationID: op.ID, Provider: op.Provider, Kind: kind, Subject: e.ID,
			ProviderRef: e.ExternalID, Service: e.Site,
			AmountText: strings.TrimSpace(e.CostText), Currency: isoCurrency(e.Currency),
			AmountSource: CostSourceUpstreamPrice, OccurredAt: now,
		})
	}
	return out
}

// lifecycleCostEvent 为取消（退款）与延长 / 重激活造事件。
func (s *Service) lifecycleCostEvent(op Operation, kind string, r Resource) []CostEvent {
	ev := CostEvent{
		OperationID: op.ID, Provider: op.Provider, Kind: kind, Subject: r.ID,
		ProviderRef: r.ExternalID, Service: r.Service, Country: r.Country,
		Currency: currencyFor(op.Provider), OccurredAt: s.now(),
	}
	switch kind {
	case CostRefund:
		// 上游取消不回退款额；按我们当初被收的价退。没有价就留空——
		// 猜一个数会让「退了多少」变成一个查不出来源的数字。
		if price := strings.TrimSpace(r.PriceText); price != "" {
			ev.AmountText = "-" + price
			ev.AmountSource = CostSourceRefundOfPrice
		} else {
			ev.AmountSource = CostSourceUpstreamSilent
		}
	default:
		// 延长 / 重激活：上游写体根本不回价格。
		ev.AmountSource = CostSourceUpstreamSilent
	}
	return []CostEvent{ev}
}

// currencyFor 是这家的记账币种。Hero 不回币种（账户币种未知），留空。
func currencyFor(provider string) string {
	if provider == ProviderSMS62 {
		return CurrencyUSD
	}
	return ""
}

// isoCurrency 把官方的 ISO 数字码转成文本；0 = 上游没说。
func isoCurrency(code int64) string {
	if code <= 0 {
		return ""
	}
	return itoa64(code)
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ListCostEvents 列出成本事件（新的在前）。
func (s *Service) ListCostEvents(ctx context.Context, provider string, limit int) ([]CostEvent, error) {
	return s.store.ListCostEvents(ctx, provider, limit)
}
