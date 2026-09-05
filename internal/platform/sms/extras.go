package sms

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/connectors/sms62"
)

// 供应商专属能力（XM-SMS1）。
//
// 两家官方文档补齐后，各自多出一批**只有这家有**的东西：Hero 的历史/统计/
// 目录/租用/邮箱/收藏，62 的商品详情/订单列表。它们不进统一的 Adapter 接口
// ——那个接口只装七态推进要用的主链路；把二十几个单边方法塞进去，会让 62
// 的适配器背上一堆只能返回「不支持」的空实现，而那种空实现最容易在页面上
// 变成一个灰按钮。
//
// 所以这里按供应商分成两个能力接口，Service 用 Hero() / SMS62() 取，
// 拿不到就是 ErrExtrasNotSupported。**花钱的三个**（租用、买邮箱、邮箱重下单）
// 不直接暴露给调用方，而是走本文件末尾那几个带七态台账的 Service 方法。

// 领域层复用连接器的只读类型，不再抄一遍：它们只是视图，没有领域规则。
type (
	HeroHistoryQuery   = herosms.HistoryQuery
	HeroHistoryPage    = herosms.HistoryPage
	HeroHistoryItem    = herosms.HistoryItem
	HeroProlongRecord  = herosms.ProlongRecord
	HeroStatsEntry     = herosms.StatsEntry
	HeroExtendOption   = herosms.ExtendOption
	HeroCountry        = herosms.Country
	HeroServiceEntry   = herosms.ServiceEntry
	HeroPriceCell      = herosms.PriceCell
	HeroTopCountry     = herosms.TopCountry
	HeroRentOffers     = herosms.RentOffers
	HeroRentOffer      = herosms.RentOffer
	HeroRentInput      = herosms.RentInput
	HeroFavorite       = herosms.Favorite
	HeroEmailDomain    = herosms.EmailDomain
	HeroEmailListQuery = herosms.EmailListQuery
	HeroEmailBatchItem = herosms.EmailBatchItem
	SMS62GoodsDetail   = sms62.GoodsDetail
	SMS62OrdersPage    = sms62.OrdersPage
	SMS62OrderSummary  = sms62.OrderSummary
)

// HeroExtras 是 Hero 独有的能力。
type HeroExtras interface {
	// 只读。
	ListOTPs(ctx context.Context, r Resource) ([]Code, error)
	ExtendOptions(ctx context.Context, r Resource, kind string) ([]HeroExtendOption, error)
	ProlongHistory(ctx context.Context, r Resource) ([]HeroProlongRecord, error)
	History(ctx context.Context, q HeroHistoryQuery) (HeroHistoryPage, error)
	Stats(ctx context.Context, date string) ([]HeroStatsEntry, error)
	CustomDurations(ctx context.Context) (map[string]map[string]int64, error)
	Balance(ctx context.Context) (string, error)
	Countries(ctx context.Context) ([]HeroCountry, error)
	Services(ctx context.Context, country int64, lang string) ([]HeroServiceEntry, error)
	Operators(ctx context.Context, country int64) (map[string][]string, error)
	Prices(ctx context.Context, service string, country int64) (map[string]map[string]HeroPriceCell, error)
	TopCountries(ctx context.Context, service string, freePrice, byRank bool) ([]HeroTopCountry, error)
	RentOffers(ctx context.Context, country, hours int64) (HeroRentOffers, error)
	RentCount(ctx context.Context, service string, country int64, operator string) (map[string]map[string]HeroPriceCell, error)
	EmailDomains(ctx context.Context, site string) ([]HeroEmailDomain, error)
	ListEmailsUpstream(ctx context.Context, q HeroEmailListQuery) ([]Email, error)
	GetEmailUpstream(ctx context.Context, externalID string) (Email, error)
	// 不花钱的写。
	SetFavorite(ctx context.Context, service string, country int64, operator string) (HeroFavorite, error)
	RemoveFavorite(ctx context.Context, service string, country int64) error
	CancelEmail(ctx context.Context, externalID string) error
	// **花钱的写**——只由 Service 的台账方法调用。
	Rent(ctx context.Context, in HeroRentInput) (Resource, error)
	PurchaseEmail(ctx context.Context, site, domain string) (Email, error)
	PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]HeroEmailBatchItem, error)
	ReorderEmail(ctx context.Context, externalID string) (Email, error)
}

// SMS62Extras 是 62 独有的能力。
type SMS62Extras interface {
	GoodsDetail(ctx context.Context, goodsID string) (SMS62GoodsDetail, error)
	Orders(ctx context.Context, page, size int) (SMS62OrdersPage, error)
}

// Hero 取 Hero 的专属能力。**要求这家开着且验证过**——这些调用都会真打上游。
func (s *Service) Hero(ctx context.Context, provider string) (HeroExtras, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return nil, err
	}
	extras, ok := adapter.(HeroExtras)
	if !ok {
		return nil, fmt.Errorf("%w: %s 没有 Hero 能力", ErrExtrasNotSupported, provider)
	}
	if err := s.requireVerified(ctx, provider); err != nil {
		return nil, err
	}
	return extras, nil
}

// SMS62 取 62 的专属能力。
func (s *Service) SMS62(ctx context.Context, provider string) (SMS62Extras, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return nil, err
	}
	extras, ok := adapter.(SMS62Extras)
	if !ok {
		return nil, fmt.Errorf("%w: %s 没有 62 能力", ErrExtrasNotSupported, provider)
	}
	if err := s.requireVerified(ctx, provider); err != nil {
		return nil, err
	}
	return extras, nil
}

// ---- 花钱的三条链，全部走七态台账 ----

// Rent 租一个号（Hero 兼容层 getRentNumber）。**花钱。**
//
// 与 Purchase 同一套闸，顺序也一样：供应商 → 支持性 → 已验证 → 指纹 →
// 未决防重 → prepared → submitted → 打上游。租到的号落 sms_resource，
// subtype=2。
func (s *Service) Rent(ctx context.Context, operationID, provider string, in HeroRentInput) (Operation, error) {
	extras, err := s.Hero(ctx, provider)
	if err != nil {
		return Operation{}, err
	}
	if !SupportsAction(provider, KindRent) {
		return Operation{}, fmt.Errorf("%w: %s 不支持 rent", ErrActionNotSupported, provider)
	}
	params := map[string]string{
		"service":  strings.ToLower(strings.TrimSpace(in.Service)),
		"country":  strconv.FormatInt(in.Country, 10),
		"hours":    strconv.FormatInt(in.DurationHours, 10),
		"operator": strings.TrimSpace(in.Operator),
		"currency": strconv.FormatInt(in.Currency, 10),
	}
	op := s.newOperation(operationID, provider, KindRent, params)
	return s.runLedger(ctx, op, func() (ledgerResult, error) {
		resource, err := extras.Rent(ctx, in)
		if err != nil {
			return ledgerResult{}, err
		}
		return ledgerResult{resources: []Resource{resource}, providerRef: resource.ExternalID}, nil
	})
}

// PurchaseEmails 买邮箱（Hero Emails 组）。**花钱。**
//
// count==1 走单买（响应带 id）；count>1 走批量——**批量响应里没有 id**
// （官方 schema 如此），要再读一次列表才拿得到，与 62 买号同一个形态：
// 那次补读失败时钱已经花了，必须落 unknown。
func (s *Service) PurchaseEmails(ctx context.Context, operationID, provider, site, domain string, count int, service string) (Operation, error) {
	extras, err := s.Hero(ctx, provider)
	if err != nil {
		return Operation{}, err
	}
	if count < 1 {
		count = 1
	}
	params := map[string]string{
		"site": strings.TrimSpace(site), "domain": strings.TrimSpace(domain),
		"count": strconv.Itoa(count), "service": strings.TrimSpace(service),
	}
	op := s.newOperation(operationID, provider, KindEmailPurchase, params)
	return s.runLedger(ctx, op, func() (ledgerResult, error) {
		if count == 1 {
			email, err := extras.PurchaseEmail(ctx, site, domain)
			if err != nil {
				return ledgerResult{}, err
			}
			return ledgerResult{emails: []Email{email}, providerRef: email.ExternalID}, nil
		}
		items, err := extras.PurchaseEmailBatch(ctx, site, domain, count, service)
		if err != nil {
			return ledgerResult{}, err
		}
		// 补读：按邮箱地址把 id 找回来。找不全就是不确定——钱已经花了。
		listed, err := extras.ListEmailsUpstream(ctx, HeroEmailListQuery{Size: 100, SortDesc: true})
		if err != nil {
			return ledgerResult{}, &ProtocolError{Kind: "邮箱批量购买后补读列表失败：" + err.Error()}
		}
		byAddress := make(map[string]Email, len(listed))
		for _, e := range listed {
			byAddress[e.Email] = e
		}
		emails := make([]Email, 0, len(items))
		for _, it := range items {
			e, ok := byAddress[it.Email]
			if !ok {
				return ledgerResult{}, &ProtocolError{Kind: "邮箱批量购买后补读缺少 " + it.Email}
			}
			emails = append(emails, e)
		}
		return ledgerResult{emails: emails, providerRef: strconv.Itoa(len(emails)) + " 个邮箱"}, nil
	})
}

// EmailAction 对一个邮箱做取消或重下单。重下单**可能花钱**，取消不花。
func (s *Service) EmailAction(ctx context.Context, operationID, kind, emailID string) (Operation, error) {
	email, err := s.store.GetEmail(ctx, emailID)
	if err != nil {
		return Operation{}, err
	}
	extras, err := s.Hero(ctx, email.Provider)
	if err != nil {
		return Operation{}, err
	}
	if kind != KindEmailCancel && kind != KindEmailReorder {
		return Operation{}, fmt.Errorf("%w: 邮箱只支持 email_cancel / email_reorder", ErrActionNotSupported)
	}
	params := map[string]string{"email": email.ExternalID}
	op := s.newOperation(operationID, email.Provider, kind, params)
	op.EmailID = email.ID
	return s.runLedger(ctx, op, func() (ledgerResult, error) {
		if kind == KindEmailCancel {
			if err := extras.CancelEmail(ctx, email.ExternalID); err != nil {
				return ledgerResult{}, err
			}
			// 成功后**不自行改本地状态**：上游只回了 204，下一次刷新会读回真状态。
			return ledgerResult{providerRef: email.ExternalID}, nil
		}
		updated, err := extras.ReorderEmail(ctx, email.ExternalID)
		if err != nil {
			return ledgerResult{}, err
		}
		return ledgerResult{emails: []Email{updated}, providerRef: updated.ExternalID}, nil
	})
}

// RefreshEmail 从上游读回一个邮箱的最新状态（含收到的验证内容）并落库。
//
// 与取码同一条纪律：**人发起，不是后台轮询**。
func (s *Service) RefreshEmail(ctx context.Context, emailID string) (Email, error) {
	email, err := s.store.GetEmail(ctx, emailID)
	if err != nil {
		return Email{}, err
	}
	extras, err := s.Hero(ctx, email.Provider)
	if err != nil {
		return Email{}, err
	}
	updated, err := extras.GetEmailUpstream(ctx, email.ExternalID)
	if err != nil {
		return Email{}, err
	}
	updated.Provider = email.Provider
	updated.SyncedAt = s.now()
	if _, err := s.store.UpsertEmail(ctx, updated); err != nil {
		return Email{}, err
	}
	// 收到验证内容时推一次，与手机验证码同一条通道。
	if updated.Value != "" && email.Value == "" && s.notifier != nil {
		_ = s.notifier.NotifyCode(ctx, email.Provider, updated.Email, updated.Value)
	}
	return s.store.GetEmail(ctx, emailID)
}

// SetFavorite / RemoveFavorite 不花钱，也不进七态台账（一次 PUT 是幂等的，
// 没有「不知道成没成」的第三态需要收敛）。审计由 Action 内核负责。
func (s *Service) SetFavorite(ctx context.Context, provider, service string, country int64, operator string) (HeroFavorite, error) {
	extras, err := s.Hero(ctx, provider)
	if err != nil {
		return HeroFavorite{}, err
	}
	return extras.SetFavorite(ctx, service, country, operator)
}

func (s *Service) RemoveFavorite(ctx context.Context, provider, service string, country int64) error {
	extras, err := s.Hero(ctx, provider)
	if err != nil {
		return err
	}
	return extras.RemoveFavorite(ctx, service, country)
}

// ---- 台账运行器 ----

// ledgerResult 是一次花钱操作成功后要落库的东西。
type ledgerResult struct {
	resources   []Resource
	emails      []Email
	providerRef string
}

// ProtocolError 是领域层自己的「响应不符合预期」——归为不确定。
type ProtocolError struct{ Kind string }

func (e *ProtocolError) Error() string { return "sms: 协议错误（" + e.Kind + "）" }

func (s *Service) newOperation(operationID, provider, kind string, params map[string]string) Operation {
	return Operation{
		ID:            operationID,
		Provider:      provider,
		Kind:          kind,
		State:         StatePrepared,
		RequestHash:   CanonicalRequestHash(provider, kind, params),
		ParamsSummary: summarize(params),
		StartedAt:     s.now(),
		UpdatedAt:     s.now(),
	}
}

// runLedger 把一次上游写操作包进七态台账。
//
// 与 Purchase / ExecuteAction 里那段一模一样的纪律，抽出来是因为这次多了
// 三条花钱的链（租用、买邮箱、邮箱重下单）：每条各抄一遍，迟早有一条漏掉
// 「落库失败要落 unknown」那一行。
func (s *Service) runLedger(ctx context.Context, op Operation, do func() (ledgerResult, error)) (Operation, error) {
	if err := s.store.PrepareOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := s.store.MarkSubmitted(ctx, op.ID, s.now()); err != nil {
		return Operation{}, err
	}
	op.State = StateSubmitted

	result, err := do()
	if err != nil {
		return s.settleFailure(ctx, op, err)
	}
	op.ProviderRef = result.providerRef

	// 上游成功后落库失败 → unknown + 人工。钱已经花了，此时报错会诱使重试。
	for _, r := range result.resources {
		r.Provider = op.Provider
		r.SyncedAt = s.now()
		id, err := s.store.UpsertResource(ctx, r)
		if err != nil {
			return s.settleUnknownAfterSuccess(ctx, op, "资源落库失败："+err.Error())
		}
		if op.ResourceID == "" {
			op.ResourceID = id
		}
	}
	for _, e := range result.emails {
		e.Provider = op.Provider
		e.SyncedAt = s.now()
		id, err := s.store.UpsertEmail(ctx, e)
		if err != nil {
			return s.settleUnknownAfterSuccess(ctx, op, "邮箱落库失败："+err.Error())
		}
		if op.EmailID == "" {
			op.EmailID = id
		}
	}
	op.State = StateSucceeded
	op.UpdatedAt = s.now()
	if err := s.store.ResolveOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Service) settleUnknownAfterSuccess(ctx context.Context, op Operation, reason string) (Operation, error) {
	op.State = StateUnknown
	op.NeedsHumanReview = true
	op.FailureReason = "上游已成功但" + reason
	op.UpdatedAt = s.now()
	_ = s.store.ResolveOperation(ctx, op)
	return op, nil
}
