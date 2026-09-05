package sms

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// FakeAdapter 是不触及任何真实上游的替身（XM_SMS_MODE=fake）。
//
// 存在的理由与卡片的 fake 一样：**演示与联调不该花真钱**。它把号码、验证码
// 都在内存里造出来，所以页面、台账、七态推进这些都能在没有供应商账号的
// 情况下走通一遍。
//
// 它**不模拟失败**：不确定态那些分支由单元测试用可编程替身覆盖，
// 让 fake 随机失败只会让人以为是自己配错了。
type FakeAdapter struct {
	provider string
	now      func() time.Time

	mu   sync.Mutex
	seq  int
	nums map[string]*fakeNumber
	// orders 是 62 替身的订单 → 号码映射：买完只回订单 ID，
	// 号码要靠 ImportByUpstreamID 再读一次，替身也照这个形态来，
	// 否则这条链在 fake 下根本走不到。
	orders map[string][]Resource
}

type fakeNumber struct {
	phone string
	token string
	// codeAfter 是「第几次取码开始有码」。
	//
	// 不是第一次就给：真实上游的码要几秒到几十秒才到，而页面上「还没有码，
	// 继续等」那条路径必须能被演示到——一上来就有码的替身会让人以为
	// 等待逻辑坏了。
	codeAfter int
	fetches   int
	code      string
}

func NewFakeAdapter(provider string, now func() time.Time) *FakeAdapter {
	if now == nil {
		now = time.Now
	}
	return &FakeAdapter{provider: provider, now: now, nums: map[string]*fakeNumber{}}
}

func (f *FakeAdapter) TestConnection(ctx context.Context) (string, error) {
	if f.provider == ProviderSMS62 {
		// 62 会回出口 IP；Hero 没有这个端点，回空才是实情。
		return "203.0.113.10", nil
	}
	return "", nil
}

func (f *FakeAdapter) ListCatalog(ctx context.Context, filter CatalogFilter) ([]CatalogItem, error) {
	if f.provider == ProviderSMS62 {
		return []CatalogItem{
			{ID: "1-2-3", Name: "美国号（演示）", Country: "1", DefaultPrice: "0.35", Available: 120},
			{ID: "1-2-4", Name: "英国号（演示）", Country: "44", DefaultPrice: "0.62", Available: 38},
		}, nil
	}
	return []CatalogItem{
		{ID: "op/1", Service: "op", Country: "1", DefaultPrice: "0.40",
			RetailPrice: "0.55", MinimumPrice: "0.31", Available: 210},
		{ID: "tg/7", Service: "tg", Country: "7", DefaultPrice: "0.18",
			RetailPrice: "0.25", MinimumPrice: "0.15", Available: 64},
	}, nil
}

func (f *FakeAdapter) Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	quantity := in.Quantity
	if quantity < 1 {
		quantity = 1
	}
	var resources []Resource
	for i := 0; i < quantity; i++ {
		f.seq++
		phone := fmt.Sprintf("+1555%07d", f.seq)
		token := fmt.Sprintf("fake-token-%d", f.seq)
		external := token
		if f.provider == ProviderSMS62 {
			external = TokenFingerprint(token)
		} else {
			external = fmt.Sprintf("fake-act-%d", f.seq)
		}
		num := &fakeNumber{
			phone: phone, token: token,
			// 第二次取码才给码：把「还没有码」那条路径演示出来。
			codeAfter: 2,
			code:      fmt.Sprintf("%06d", 100000+f.seq),
		}
		f.nums[external] = num

		res := Resource{
			Provider: f.provider, ExternalID: external,
			Phone: phone, PhoneMask: MaskPhone(phone),
			Service: in.Service, Country: strconv.Itoa(in.Country),
			Status: "active", SyncedAt: f.now(),
		}
		if f.provider == ProviderSMS62 {
			res.ProviderToken = token
			res.Service = in.GoodsID
		}
		resources = append(resources, res)
	}

	// 62 的形态：买完只回订单 ID，号码要靠 ImportByUpstreamID 再读一次。
	// 替身也照这个形态来，否则这条链在 fake 下根本走不到。
	if f.provider == ProviderSMS62 {
		f.seq++
		orderID := fmt.Sprintf("fake-order-%d", f.seq)
		f.pendingOrders(orderID, resources)
		return PurchaseOutcome{OrderRef: orderID, RequestID: "fake-req"}, nil
	}
	return PurchaseOutcome{Resources: resources, RequestID: "fake-req"}, nil
}

func (f *FakeAdapter) pendingOrders(orderID string, resources []Resource) {
	if f.orders == nil {
		f.orders = map[string][]Resource{}
	}
	f.orders[orderID] = resources
}

func (f *FakeAdapter) ImportByUpstreamID(ctx context.Context, upstreamID string) ([]Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if resources, ok := f.orders[upstreamID]; ok {
		return resources, nil
	}
	if num, ok := f.nums[upstreamID]; ok {
		return []Resource{{
			Provider: f.provider, ExternalID: upstreamID,
			Phone: num.phone, PhoneMask: MaskPhone(num.phone),
			Status: "active", SyncedAt: f.now(),
		}}, nil
	}
	return nil, fmt.Errorf("替身里没有 %q", upstreamID)
}

func (f *FakeAdapter) FetchCode(ctx context.Context, r Resource) (Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	num, ok := f.nums[r.ExternalID]
	if !ok {
		return Code{}, ErrCodeNotAvailable
	}
	num.fetches++
	if num.fetches < num.codeAfter {
		return Code{}, ErrCodeNotAvailable
	}
	return Code{Code: num.code, Sender: "DEMO", ReceivedAt: f.now()}, nil
}

func (f *FakeAdapter) ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error) {
	if !SupportsAction(f.provider, kind) {
		return Resource{}, ErrActionNotSupported
	}
	switch kind {
	case KindCancel, KindFinish:
		// 与真实实现一致：**不自行改状态**。上游只回 204。
		return Resource{}, nil
	case KindReplace:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		external := fmt.Sprintf("fake-act-%d", f.seq)
		phone := fmt.Sprintf("+1555%07d", f.seq)
		f.nums[external] = &fakeNumber{
			phone: phone, codeAfter: 2, code: fmt.Sprintf("%06d", 100000+f.seq),
		}
		return Resource{
			Provider: f.provider, ExternalID: external,
			Phone: phone, PhoneMask: MaskPhone(phone),
			Status: "active", SyncedAt: f.now(),
		}, nil
	default:
		r.Status = "active"
		r.SyncedAt = f.now()
		return r, nil
	}
}
