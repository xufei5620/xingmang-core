package sms

import (
	"context"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sms62"
)

// SMS62Client 是本适配器需要的 62 客户端能力。
//
// 定成接口是为了让适配器能在没有网络的情况下被测到——七态推进那些分支
// 在真实环境里每复现一次就是真买一次号。
type SMS62Client interface {
	GetInfo(ctx context.Context) (sms62.Info, error)
	ListGoods(ctx context.Context, platformID, country int64) ([]sms62.GoodsItem, error)
	PurchaseNumbers(ctx context.Context, in sms62.PurchaseInput) (sms62.PurchaseResult, error)
	GetOrderTokens(ctx context.Context, orderID string) (sms62.OrderTokens, error)
	GetMessages(ctx context.Context, token string, limit int) ([]sms62.Message, error)
}

// SMS62Adapter 把 62 的订单制协议翻成领域层的统一形状。
type SMS62Adapter struct {
	client SMS62Client
	now    func() time.Time
}

func NewSMS62Adapter(client SMS62Client, now func() time.Time) *SMS62Adapter {
	if now == nil {
		now = time.Now
	}
	return &SMS62Adapter{client: client, now: now}
}

func (a *SMS62Adapter) TestConnection(ctx context.Context) (string, error) {
	info, err := a.client.GetInfo(ctx)
	if err != nil {
		return "", err
	}
	// 出口 IP 是这次测试最有价值的产出：上游若做 IP 白名单，它对不上就是
	// 后续全部 403 的原因，而那种失败从错误码上看只是「没权限」。
	return info.ClientIP, nil
}

func (a *SMS62Adapter) ListCatalog(ctx context.Context, filter CatalogFilter) ([]CatalogItem, error) {
	platformID, _ := strconv.ParseInt(filter.PlatformID, 10, 64)
	country, _ := strconv.ParseInt(filter.Country, 10, 64)
	goods, err := a.client.ListGoods(ctx, platformID, country)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogItem, 0, len(goods))
	for _, g := range goods {
		out = append(out, CatalogItem{
			ID:      g.ID,
			Name:    g.Name,
			Country: g.Country,
			// 62 只有一个价格。**不把它填进另外两个字段**：三个价格在
			// Hero 那边是不同事实，这里补齐会让页面显示出一组假的对比。
			DefaultPrice: g.Price,
			Available:    g.Stock,
		})
	}
	return out, nil
}

// Purchase 买号。
//
// **返回的 Resources 是空的**——这正是 62 的形态：买完只回订单 ID，
// 号码要靠 ImportByUpstreamID 再读一次。Service 据此判断需要补读，
// 而那次补读失败时钱已经花了，必须落 unknown。
func (a *SMS62Adapter) Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error) {
	res, err := a.client.PurchaseNumbers(ctx, sms62.PurchaseInput{
		GoodsID:       in.GoodsID,
		Num:           in.Quantity,
		FirstNumber:   in.FirstNumber,
		NoFirstNumber: in.NoFirstNumber,
	})
	if err != nil {
		return PurchaseOutcome{}, err
	}
	return PurchaseOutcome{OrderRef: res.OrderID, RequestID: res.RequestID}, nil
}

// ImportByUpstreamID 按订单 ID 只读读回号码。**绝不购买。**
func (a *SMS62Adapter) ImportByUpstreamID(ctx context.Context, orderID string) ([]Resource, error) {
	tokens, err := a.client.GetOrderTokens(ctx, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(tokens.Numbers))
	for _, n := range tokens.Numbers {
		out = append(out, Resource{
			Provider: ProviderSMS62,
			// 身份用 token 的**指纹**而不是 token 本身：ID 会进索引、日志与
			// URL，而 token 不能进这些地方。指纹是单向的，拿不去取码。
			ExternalID:    TokenFingerprint(n.Token),
			Phone:         n.Phone,
			PhoneMask:     MaskPhone(n.Phone),
			ProviderToken: n.Token,
			Service:       n.PlatformText,
			Status:        n.StatusText,
			// 62 的号买到就是待收码；它没有取消/完成的接口，状态只会被本地
			// 取码推进（见 Service.FetchCode）。
			State:    StateWaitingCode,
			SyncedAt: a.now(),
		})
	}
	return out, nil
}

// FetchCode 取码。
func (a *SMS62Adapter) FetchCode(ctx context.Context, r Resource) (Code, error) {
	messages, err := a.client.GetMessages(ctx, r.ProviderToken, 20)
	if err != nil {
		return Code{}, err
	}
	message, code, ok := sms62.PickLatest(messages)
	if !ok {
		// 还没有码：**正常状态**，不是故障。码要几秒到几十秒才来。
		return Code{}, ErrCodeNotAvailable
	}
	received := time.Time{}
	if message.ReceivedAt > 0 {
		received = time.Unix(message.ReceivedAt, 0).UTC()
	}
	// **短信正文不带出去**：正文里可能有订单号、金额、姓名等与验证无关的
	// 东西，而我们需要的只有那几位码。持久层也没有正文列。
	return Code{Code: code, Sender: message.Sender, ReceivedAt: received}, nil
}

// ExecuteAction：62 一个生命周期动作都没有。
//
// 在这里明确拒绝，而不是让它打到上游换一个含糊的 404——那种 404 看起来像
// 「号码不存在」，会把人引到完全错的方向。
func (a *SMS62Adapter) ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error) {
	return Resource{}, ErrActionNotSupported
}
