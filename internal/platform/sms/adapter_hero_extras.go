package sms

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
)

// HeroExtrasClient 是 HeroExtras 需要的客户端能力（*herosms.Client 满足它）。
//
// 与 HeroClient 分开声明：主链路的替身不必为了编译而实现二十几个空方法。
// 适配器在运行时做类型断言，断不到就是 ErrExtrasNotSupported。
type HeroExtrasClient interface {
	ListOTPs(ctx context.Context, activationID string) ([]herosms.OTP, error)
	GetReactivateOptions(ctx context.Context, activationID string) ([]herosms.ExtendOption, error)
	GetProlongOptions(ctx context.Context, activationID string) ([]herosms.ExtendOption, error)
	GetProlongHistory(ctx context.Context, activationID string) ([]herosms.ProlongRecord, error)
	ListHistory(ctx context.Context, q herosms.HistoryQuery) (herosms.HistoryPage, error)
	GetStats(ctx context.Context, date string) ([]herosms.StatsEntry, error)
	GetCustomDurations(ctx context.Context) (map[string]map[string]int64, error)
	GetBalance(ctx context.Context) (string, error)
	GetCountries(ctx context.Context) ([]herosms.Country, error)
	GetServicesList(ctx context.Context, country int64, lang string) ([]herosms.ServiceEntry, error)
	GetOperators(ctx context.Context, country int64) (map[string][]string, error)
	GetPrices(ctx context.Context, service string, country int64) (map[string]map[string]herosms.PriceCell, error)
	GetTopCountriesByService(ctx context.Context, service string, freePrice, byRank bool) ([]herosms.TopCountry, error)
	GetRentOffers(ctx context.Context, country, durationHours int64) (herosms.RentOffers, error)
	ServiceCountRent(ctx context.Context, service string, country int64, operator string) (map[string]map[string]herosms.PriceCell, error)
	GetRentNumber(ctx context.Context, in herosms.RentInput) (herosms.Activation, error)
	SetFavorite(ctx context.Context, service string, country int64, operator string) (herosms.Favorite, error)
	RemoveFavorite(ctx context.Context, service string, country int64) error
	ListEmails(ctx context.Context, q herosms.EmailListQuery) ([]herosms.Email, error)
	PurchaseEmail(ctx context.Context, site, domain string) (herosms.Email, error)
	PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]herosms.EmailBatchItem, error)
	GetEmail(ctx context.Context, emailID string) (herosms.Email, error)
	CancelEmail(ctx context.Context, emailID string) error
	ReorderEmail(ctx context.Context, emailID string) (herosms.Email, error)
	ListEmailDomains(ctx context.Context, site string) ([]herosms.EmailDomain, error)
}

func (a *HeroAdapter) extras() (HeroExtrasClient, error) {
	c, ok := a.client.(HeroExtrasClient)
	if !ok {
		return nil, fmt.Errorf("%w: Hero 客户端没有扩展能力", ErrExtrasNotSupported)
	}
	return c, nil
}

func (a *HeroAdapter) ListOTPs(ctx context.Context, r Resource) ([]Code, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	otps, err := c.ListOTPs(ctx, r.ExternalID)
	if err != nil {
		return nil, err
	}
	out := make([]Code, 0, len(otps))
	for _, o := range otps {
		received, _ := time.Parse(time.RFC3339Nano, o.ReceivedAt)
		// 正文不带出去，与 FetchCode 同一条纪律。
		out = append(out, Code{
			ID: o.ID, Provider: ProviderHero, ResourceID: r.ID,
			Code: o.Code, Sender: o.Sender, ReceivedAt: received.UTC(),
		})
	}
	return out, nil
}

func (a *HeroAdapter) ExtendOptions(ctx context.Context, r Resource, kind string) ([]HeroExtendOption, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindProlong:
		return c.GetProlongOptions(ctx, r.ExternalID)
	case KindReactivate:
		return c.GetReactivateOptions(ctx, r.ExternalID)
	default:
		return nil, fmt.Errorf("%w: 档位只有 prolong / reactivate 两种", ErrActionNotSupported)
	}
}

func (a *HeroAdapter) ProlongHistory(ctx context.Context, r Resource) ([]HeroProlongRecord, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetProlongHistory(ctx, r.ExternalID)
}

func (a *HeroAdapter) History(ctx context.Context, q HeroHistoryQuery) (HeroHistoryPage, error) {
	c, err := a.extras()
	if err != nil {
		return HeroHistoryPage{}, err
	}
	return c.ListHistory(ctx, q)
}

func (a *HeroAdapter) Stats(ctx context.Context, date string) ([]HeroStatsEntry, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetStats(ctx, date)
}

func (a *HeroAdapter) CustomDurations(ctx context.Context) (map[string]map[string]int64, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetCustomDurations(ctx)
}

func (a *HeroAdapter) Balance(ctx context.Context) (string, error) {
	c, err := a.extras()
	if err != nil {
		return "", err
	}
	return c.GetBalance(ctx)
}

func (a *HeroAdapter) Countries(ctx context.Context) ([]HeroCountry, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetCountries(ctx)
}

func (a *HeroAdapter) Services(ctx context.Context, country int64, lang string) ([]HeroServiceEntry, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetServicesList(ctx, country, lang)
}

func (a *HeroAdapter) Operators(ctx context.Context, country int64) (map[string][]string, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetOperators(ctx, country)
}

func (a *HeroAdapter) Prices(ctx context.Context, service string, country int64) (map[string]map[string]HeroPriceCell, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetPrices(ctx, service, country)
}

func (a *HeroAdapter) TopCountries(ctx context.Context, service string, freePrice, byRank bool) ([]HeroTopCountry, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.GetTopCountriesByService(ctx, service, freePrice, byRank)
}

func (a *HeroAdapter) RentOffers(ctx context.Context, country, hours int64) (HeroRentOffers, error) {
	c, err := a.extras()
	if err != nil {
		return HeroRentOffers{}, err
	}
	return c.GetRentOffers(ctx, country, hours)
}

func (a *HeroAdapter) RentCount(ctx context.Context, service string, country int64, operator string) (map[string]map[string]HeroPriceCell, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.ServiceCountRent(ctx, service, country, operator)
}

// Rent 租号。**花钱。** 租到的号是 subtype=2 的资源。
func (a *HeroAdapter) Rent(ctx context.Context, in HeroRentInput) (Resource, error) {
	c, err := a.extras()
	if err != nil {
		return Resource{}, err
	}
	activation, err := c.GetRentNumber(ctx, in)
	if err != nil {
		return Resource{}, err
	}
	r := a.toResource(activation)
	if r.Subtype == 0 {
		r.Subtype = SubtypeRent
	}
	return r, nil
}

func (a *HeroAdapter) SetFavorite(ctx context.Context, service string, country int64, operator string) (HeroFavorite, error) {
	c, err := a.extras()
	if err != nil {
		return HeroFavorite{}, err
	}
	return c.SetFavorite(ctx, service, country, operator)
}

func (a *HeroAdapter) RemoveFavorite(ctx context.Context, service string, country int64) error {
	c, err := a.extras()
	if err != nil {
		return err
	}
	return c.RemoveFavorite(ctx, service, country)
}

func (a *HeroAdapter) EmailDomains(ctx context.Context, site string) ([]HeroEmailDomain, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.ListEmailDomains(ctx, site)
}

func (a *HeroAdapter) ListEmailsUpstream(ctx context.Context, q HeroEmailListQuery) ([]Email, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	emails, err := c.ListEmails(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]Email, 0, len(emails))
	for _, e := range emails {
		out = append(out, a.toEmail(e))
	}
	return out, nil
}

func (a *HeroAdapter) GetEmailUpstream(ctx context.Context, externalID string) (Email, error) {
	c, err := a.extras()
	if err != nil {
		return Email{}, err
	}
	e, err := c.GetEmail(ctx, externalID)
	if err != nil {
		return Email{}, err
	}
	return a.toEmail(e), nil
}

func (a *HeroAdapter) PurchaseEmail(ctx context.Context, site, domain string) (Email, error) {
	c, err := a.extras()
	if err != nil {
		return Email{}, err
	}
	e, err := c.PurchaseEmail(ctx, site, domain)
	if err != nil {
		return Email{}, err
	}
	return a.toEmail(e), nil
}

func (a *HeroAdapter) PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]HeroEmailBatchItem, error) {
	c, err := a.extras()
	if err != nil {
		return nil, err
	}
	return c.PurchaseEmailBatch(ctx, site, domain, count, service)
}

func (a *HeroAdapter) CancelEmail(ctx context.Context, externalID string) error {
	c, err := a.extras()
	if err != nil {
		return err
	}
	return c.CancelEmail(ctx, externalID)
}

func (a *HeroAdapter) ReorderEmail(ctx context.Context, externalID string) (Email, error) {
	c, err := a.extras()
	if err != nil {
		return Email{}, err
	}
	e, err := c.ReorderEmail(ctx, externalID)
	if err != nil {
		return Email{}, err
	}
	return a.toEmail(e), nil
}

func (a *HeroAdapter) toEmail(e herosms.Email) Email {
	date, _ := time.Parse(time.RFC3339Nano, e.Date)
	return Email{
		Provider:     ProviderHero,
		ExternalID:   e.ID,
		Site:         e.Site,
		Email:        e.Email,
		Status:       e.Status,
		Value:        e.Value,
		CostText:     e.Cost,
		Currency:     e.Currency,
		UpstreamDate: date.UTC(),
		Message:      e.Message,
		SyncedAt:     a.now(),
	}
}

// toResource 补上官方新字段（在 adapter_hero.go 的版本之上）。
//
// 放在这里而不是改那边：那边的函数体被主链路测试钉着，这里只是叠加。
func (a *HeroAdapter) enrich(r Resource, v herosms.Activation) Resource {
	r.Operator = v.Operator
	r.PriceText = v.Price
	r.VerificationType = v.VerificationType
	r.Subtype = v.Subtype
	// 统一状态由官方状态码映射；上游原话留在 r.Status。
	r.State = MapHeroStatus(v.Status)
	if v.CountryPhoneCode > 0 {
		r.CountryPhoneCode = strconv.FormatInt(v.CountryPhoneCode, 10)
	}
	return r
}
