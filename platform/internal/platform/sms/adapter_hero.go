package sms

import (
	"context"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
)

// HeroClient 是本适配器需要的 Hero 客户端能力。
type HeroClient interface {
	TestConnection(ctx context.Context) error
	ListOffers(ctx context.Context, services, countries string) ([]herosms.Offer, error)
	PurchaseActivations(ctx context.Context, in herosms.PurchaseInput) ([]herosms.Activation, error)
	LookupActivation(ctx context.Context, activationID string) (herosms.Activation, error)
	GetLastOTP(ctx context.Context, activationID string) (herosms.OTP, bool, error)
	Cancel(ctx context.Context, activationID string) error
	Finish(ctx context.Context, activationID string) error
	Replace(ctx context.Context, activationID string) (herosms.Activation, error)
	Reactivate(ctx context.Context, activationID string, duration int) (herosms.Activation, error)
	Prolong(ctx context.Context, activationID string, duration int) (herosms.Activation, error)
}

// HeroAdapter 把 Hero 的 activation 制协议翻成领域层的统一形状。
type HeroAdapter struct {
	client HeroClient
	now    func() time.Time
}

func NewHeroAdapter(client HeroClient, now func() time.Time) *HeroAdapter {
	if now == nil {
		now = time.Now
	}
	return &HeroAdapter{client: client, now: now}
}

// TestConnection 只验证密钥能过鉴权。
//
// **返回的出口 IP 是空的**：这家没有 info 端点，编一个 IP 出来会让人以为
// 我们知道上游看到的是哪个地址。空就是空。
func (a *HeroAdapter) TestConnection(ctx context.Context) (string, error) {
	return "", a.client.TestConnection(ctx)
}

func (a *HeroAdapter) ListCatalog(ctx context.Context, filter CatalogFilter) ([]CatalogItem, error) {
	offers, err := a.client.ListOffers(ctx, filter.Service, filter.Country)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogItem, 0, len(offers))
	for _, o := range offers {
		out = append(out, CatalogItem{
			ID:      o.Service + "/" + strconv.FormatInt(o.Country, 10),
			Service: o.Service,
			Country: strconv.FormatInt(o.Country, 10),
			// 三个价格分开保留：它们是**不同的事实**，把 default 当成
			// 「本次成交价」会让人按一个不成立的数字做预算。
			DefaultPrice: o.DefaultPrice,
			RetailPrice:  o.RetailPrice,
			MinimumPrice: o.MinimumPrice,
			Available:    o.AvailableCount,
		})
	}
	return out, nil
}

// Purchase 买号。**一次调用就拿到号码**——没有 62 那个补读窗口。
func (a *HeroAdapter) Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error) {
	activations, err := a.client.PurchaseActivations(ctx, herosms.PurchaseInput{
		Service:  in.Service,
		Country:  in.Country,
		Amount:   in.Quantity,
		Operator: in.Operator,
		MaxPrice: in.MaxPrice,
		Duration: in.Duration,
	})
	if err != nil {
		return PurchaseOutcome{}, err
	}
	return PurchaseOutcome{Resources: a.toResources(activations)}, nil
}

// ImportByUpstreamID 按 activation ID 只读反查。
func (a *HeroAdapter) ImportByUpstreamID(ctx context.Context, activationID string) ([]Resource, error) {
	activation, err := a.client.LookupActivation(ctx, activationID)
	if err != nil {
		return nil, err
	}
	return a.toResources([]herosms.Activation{activation}), nil
}

func (a *HeroAdapter) FetchCode(ctx context.Context, r Resource) (Code, error) {
	otp, found, err := a.client.GetLastOTP(ctx, r.ExternalID)
	if err != nil {
		return Code{}, err
	}
	if !found {
		return Code{}, ErrCodeNotAvailable
	}
	received, _ := time.Parse(time.RFC3339Nano, otp.ReceivedAt)
	// 正文不带出去，与 62 同一条纪律。
	return Code{Code: otp.Code, Sender: otp.Sender, ReceivedAt: received.UTC()}, nil
}

// ExecuteAction 执行生命周期动作。
//
// cancel / finish 成功后**返回原资源、不自行改状态**：上游只回了一个 204，
// 没说这个号变成了什么。替它说一个「已取消」，页面上就会显示一个上游并不
// 认可的状态，而下一次同步又会把它变回去。
func (a *HeroAdapter) ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error) {
	switch kind {
	case KindCancel:
		if err := a.client.Cancel(ctx, r.ExternalID); err != nil {
			return Resource{}, err
		}
		return Resource{}, nil
	case KindFinish:
		if err := a.client.Finish(ctx, r.ExternalID); err != nil {
			return Resource{}, err
		}
		return Resource{}, nil
	case KindReplace:
		// **可能返回新的 activation ID**：新资源要落库，而操作台账里仍保留
		// 原目标——那笔操作针对的是「那张旧号」。
		activation, err := a.client.Replace(ctx, r.ExternalID)
		if err != nil {
			return Resource{}, err
		}
		return a.toResource(activation), nil
	case KindReactivate:
		activation, err := a.client.Reactivate(ctx, r.ExternalID, opt.Duration)
		if err != nil {
			return Resource{}, err
		}
		return a.toResource(activation), nil
	case KindProlong:
		activation, err := a.client.Prolong(ctx, r.ExternalID, opt.Duration)
		if err != nil {
			return Resource{}, err
		}
		return a.toResource(activation), nil
	default:
		return Resource{}, ErrActionNotSupported
	}
}

func (a *HeroAdapter) toResources(activations []herosms.Activation) []Resource {
	out := make([]Resource, 0, len(activations))
	for _, v := range activations {
		out = append(out, a.toResource(v))
	}
	return out
}

func (a *HeroAdapter) toResource(v herosms.Activation) Resource {
	created, _ := time.Parse(time.RFC3339Nano, v.CreatedAt)
	expires, _ := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	return a.enrich(Resource{
		Provider: ProviderHero,
		// 身份就是 activation ID。**这家不存 token**——迁移里的 CHECK
		// 约束会挡住误存。
		ExternalID:        v.ID,
		Phone:             v.Phone,
		PhoneMask:         MaskPhone(v.Phone),
		Service:           v.Service,
		Country:           strconv.FormatInt(v.Country, 10),
		Status:            v.Status,
		UpstreamCreatedAt: created.UTC(),
		ExpiresAt:         expires.UTC(),
		SyncedAt:          a.now(),
	}, v)
}
