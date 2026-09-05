package sms

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// FakeAdapter 的扩展能力（XM_SMS_MODE=fake）。
//
// 与主链路的 fake 同一个理由：**演示与联调不该花真钱**。每个新页面（历史、
// 统计、目录、租用、邮箱、收藏）都要能在没有供应商账号的情况下走通一遍。
// 数据是明显的演示值（「演示」字样、203.0.113.x 这类文档保留段），
// 不会被当成真的。
//
// 62 替身只实现 SMS62Extras，Hero 替身只实现 HeroExtras——类型断言在
// Service.Hero() / SMS62() 里做，装错供应商会得到 ErrExtrasNotSupported，
// 与真实适配器的行为一致。

// ---- Hero ----

func (f *FakeAdapter) ListOTPs(ctx context.Context, r Resource) ([]Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	num, ok := f.nums[r.ExternalID]
	if !ok || num.fetches < num.codeAfter {
		return nil, nil
	}
	return []Code{{ID: "otp-" + r.ExternalID, Provider: f.provider, ResourceID: r.ID,
		Code: num.code, Sender: "演示发件方", ReceivedAt: f.now()}}, nil
}

func (f *FakeAdapter) ExtendOptions(ctx context.Context, r Resource, kind string) ([]HeroExtendOption, error) {
	if kind != KindProlong && kind != KindReactivate {
		return nil, ErrActionNotSupported
	}
	return []HeroExtendOption{
		{Duration: 4, Unit: "hour", Price: "0.50"},
		{Duration: 12, Unit: "hour", Price: "1.20"},
		{Duration: 24, Unit: "hour", Price: "2.00"},
	}, nil
}

func (f *FakeAdapter) ProlongHistory(ctx context.Context, r Resource) ([]HeroProlongRecord, error) {
	return []HeroProlongRecord{{Duration: 4, Unit: "hour", Price: "0.50",
		CreatedAt: f.now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)}}, nil
}

func (f *FakeAdapter) History(ctx context.Context, q HeroHistoryQuery) (HeroHistoryPage, error) {
	return HeroHistoryPage{
		Items: []HeroHistoryItem{
			{ID: "9001", CreateDate: q.From + " 10:00:00", Service: "go", Country: 12, Phone: "7999000000", Cost: "0.35", Status: 6, PhoneCode: "7", Currency: 840},
			{ID: "9002", CreateDate: q.From + " 11:30:00", Service: "tg", Country: 1, Phone: "1555000000", Cost: "0.62", Status: 8, PhoneCode: "1", Currency: 840},
		},
		TotalSum: "0.97", SuccessCount: 1, Page: 1, Size: 20, Total: 2,
	}, nil
}

func (f *FakeAdapter) Stats(ctx context.Context, date string) ([]HeroStatsEntry, error) {
	return []HeroStatsEntry{
		{Country: "12", Service: "go", Count: 3, Sum: "1.05"},
		{Country: "1", Service: "tg", Count: 1, Sum: "0.62"},
	}, nil
}

func (f *FakeAdapter) CustomDurations(ctx context.Context) (map[string]map[string]int64, error) {
	return map[string]map[string]int64{"go": {"12": 72}, "tg": {"0": 24}}, nil
}

func (f *FakeAdapter) Balance(ctx context.Context) (string, error) {
	return "12.50", nil
}

func (f *FakeAdapter) Countries(ctx context.Context) ([]HeroCountry, error) {
	return []HeroCountry{
		{ID: 1, NameEN: "USA", NameCN: "美国（演示）", Visible: true, Retry: true},
		{ID: 12, NameEN: "Russia", NameCN: "俄罗斯（演示）", Visible: true, Retry: true},
		{ID: 44, NameEN: "UK", NameCN: "英国（演示）", Visible: true},
	}, nil
}

func (f *FakeAdapter) Services(ctx context.Context, country int64, lang string) ([]HeroServiceEntry, error) {
	return []HeroServiceEntry{{Code: "go", Name: "Google（演示）"}, {Code: "tg", Name: "Telegram（演示）"}, {Code: "op", Name: "OpenAI（演示）"}}, nil
}

func (f *FakeAdapter) Operators(ctx context.Context, country int64) (map[string][]string, error) {
	return map[string][]string{"12": {"mts", "beeline"}, "1": {"any"}}, nil
}

func (f *FakeAdapter) Prices(ctx context.Context, service string, country int64) (map[string]map[string]HeroPriceCell, error) {
	return map[string]map[string]HeroPriceCell{
		"12": {"go": {Cost: "0.35", Count: 120, PhysicalCount: 80}},
		"1":  {"go": {Cost: "0.55", Count: 30, PhysicalCount: 30}, "tg": {Cost: "0.62", Count: 38}},
	}, nil
}

func (f *FakeAdapter) TopCountries(ctx context.Context, service string, freePrice, byRank bool) ([]HeroTopCountry, error) {
	return []HeroTopCountry{{Country: 12, Price: "0.35", RetailPrice: "0.45", Count: 120}, {Country: 1, Price: "0.55", RetailPrice: "0.70", Count: 30}}, nil
}

func (f *FakeAdapter) RentOffers(ctx context.Context, country, hours int64) (HeroRentOffers, error) {
	return HeroRentOffers{
		Operators: map[string]string{"any": "any", "mts": "MTS"},
		Services:  map[string]HeroRentOffer{"go": {Service: "go", Quantity: 5, Price: "1.50", RetailPrice: "2.00"}},
	}, nil
}

func (f *FakeAdapter) RentCount(ctx context.Context, service string, country int64, operator string) (map[string]map[string]HeroPriceCell, error) {
	return map[string]map[string]HeroPriceCell{"12": {"4": {Cost: "1.50", Count: 5}, "24": {Cost: "5.00", Count: 2}}}, nil
}

func (f *FakeAdapter) Rent(ctx context.Context, in HeroRentInput) (Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	phone := fmt.Sprintf("+7999%07d", f.seq)
	external := fmt.Sprintf("fake-rent-%d", f.seq)
	f.nums[external] = &fakeNumber{phone: phone, codeAfter: 2, code: fmt.Sprintf("%06d", 200000+f.seq)}
	return Resource{
		Provider: f.provider, ExternalID: external, Phone: phone, PhoneMask: MaskPhone(phone),
		Service: in.Service, Country: strconv.FormatInt(in.Country, 10), Status: "1",
		Operator: in.Operator, PriceText: "1.50", VerificationType: "sms", Subtype: SubtypeRent, State: StateWaitingCode,
		UpstreamCreatedAt: f.now(), ExpiresAt: f.now().Add(time.Duration(in.DurationHours) * time.Hour),
		SyncedAt: f.now(),
	}, nil
}

func (f *FakeAdapter) SetFavorite(ctx context.Context, service string, country int64, operator string) (HeroFavorite, error) {
	return HeroFavorite{ID: "fav-1", Service: service, Country: country, CountryName: "演示国家", ServiceName: "演示服务", Operator: operator, IsFavorite: true}, nil
}

func (f *FakeAdapter) RemoveFavorite(ctx context.Context, service string, country int64) error {
	return nil
}

func (f *FakeAdapter) EmailDomains(ctx context.Context, site string) ([]HeroEmailDomain, error) {
	return []HeroEmailDomain{{Name: "demo-mail.example", Cost: "0.20", Count: 100}, {Name: "fast-mail.example", Cost: "0.35", Count: 12}}, nil
}

func (f *FakeAdapter) ListEmailsUpstream(ctx context.Context, q HeroEmailListQuery) ([]Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Email, 0, len(f.emails))
	for _, e := range f.emails {
		out = append(out, e.rec)
	}
	return out, nil
}

func (f *FakeAdapter) GetEmailUpstream(ctx context.Context, externalID string) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.emails[externalID]
	if !ok {
		return Email{}, fmt.Errorf("演示邮箱 %s 不存在", externalID)
	}
	// 第二次刷新才有验证内容：把「还没到」那条路径演示出来。
	e.refreshes++
	if e.refreshes >= 2 && e.rec.Value == "" {
		e.rec.Value = "DEMO-" + externalID
		e.rec.Status = "SUCCESS"
	}
	return e.rec, nil
}

func (f *FakeAdapter) PurchaseEmail(ctx context.Context, site, domain string) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newFakeEmail(site, domain).rec, nil
}

func (f *FakeAdapter) PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]HeroEmailBatchItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]HeroEmailBatchItem, 0, count)
	for i := 0; i < count; i++ {
		e := f.newFakeEmail(site, domain)
		out = append(out, HeroEmailBatchItem{Site: site, Email: e.rec.Email, Status: e.rec.Status, Cost: e.rec.CostText, Domain: domain})
	}
	return out, nil
}

func (f *FakeAdapter) CancelEmail(ctx context.Context, externalID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.emails[externalID]; ok {
		e.rec.Status = "CANCEL"
	}
	return nil
}

func (f *FakeAdapter) ReorderEmail(ctx context.Context, externalID string) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.emails[externalID]
	if !ok {
		return Email{}, fmt.Errorf("演示邮箱 %s 不存在", externalID)
	}
	e.rec.Status = "WAIT"
	e.rec.Value = ""
	e.refreshes = 0
	return e.rec, nil
}

type fakeEmail struct {
	rec       Email
	refreshes int
}

func (f *FakeAdapter) newFakeEmail(site, domain string) *fakeEmail {
	if f.emails == nil {
		f.emails = map[string]*fakeEmail{}
	}
	f.seq++
	id := strconv.Itoa(9000 + f.seq)
	e := &fakeEmail{rec: Email{
		Provider: f.provider, ExternalID: id, Site: site,
		Email: fmt.Sprintf("demo%d@%s", f.seq, domain), Status: "WAIT",
		CostText: "0.20", Currency: 840, UpstreamDate: f.now(), SyncedAt: f.now(),
	}}
	f.emails[id] = e
	return e
}

// ---- 62 ----

func (f *FakeAdapter) GoodsDetail(ctx context.Context, goodsID string) (SMS62GoodsDetail, error) {
	return SMS62GoodsDetail{ID: goodsID, Name: "演示商品 " + goodsID, Price: "0.35", Country: "1", Stock: 120, Durations: []int64{1, 7, 30}}, nil
}

func (f *FakeAdapter) Orders(ctx context.Context, page, size int) (SMS62OrdersPage, error) {
	return SMS62OrdersPage{Page: page, PageSize: size, Total: 2, Orders: []SMS62OrderSummary{
		{OrderID: "4213", GoodsID: "12-1-7", Quantity: 2, Status: 3, StatusText: "完成", AmountText: "0.70", CreatedAt: f.now().Add(-24 * time.Hour).Unix()},
		{OrderID: "4214", GoodsID: "1-2-3", Quantity: 1, Status: 1, StatusText: "进行中", AmountText: "0.35", CreatedAt: f.now().Add(-time.Hour).Unix()},
	}}, nil
}
