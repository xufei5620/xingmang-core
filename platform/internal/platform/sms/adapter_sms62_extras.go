package sms

import (
	"context"
	"fmt"

	"github.com/xufei5620/xingmang-platform/connectors/sms62"
)

// SMS62ExtrasClient 是 SMS62Extras 需要的客户端能力（*sms62.Client 满足它）。
type SMS62ExtrasClient interface {
	GetGoodsDetail(ctx context.Context, goodsID string) (sms62.GoodsDetail, error)
	ListOrders(ctx context.Context, page, pageSize int) (sms62.OrdersPage, error)
}

func (a *SMS62Adapter) extras() (SMS62ExtrasClient, error) {
	c, ok := a.client.(SMS62ExtrasClient)
	if !ok {
		return nil, fmt.Errorf("%w: 62 客户端没有扩展能力", ErrExtrasNotSupported)
	}
	return c, nil
}

// GoodsDetail 读商品详情（两段 ID：平台-国家）。
func (a *SMS62Adapter) GoodsDetail(ctx context.Context, goodsID string) (SMS62GoodsDetail, error) {
	c, err := a.extras()
	if err != nil {
		return SMS62GoodsDetail{}, err
	}
	return c.GetGoodsDetail(ctx, goodsID)
}

// Orders 读上游的订单分页列表。
//
// 它是**上游的说法**，与本地 sms_order 表不是一回事：本地只记我们通过平台
// 下的单，上游列表还包括在 62 后台手工下的。人工核对 unknown 时两边都要看。
func (a *SMS62Adapter) Orders(ctx context.Context, page, size int) (SMS62OrdersPage, error) {
	c, err := a.extras()
	if err != nil {
		return SMS62OrdersPage{}, err
	}
	return c.ListOrders(ctx, page, size)
}
