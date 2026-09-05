package sms

import (
	"context"
	"fmt"
	"strings"
)

// ImportUpstream 把一笔在供应商后台下的单（62 的订单 / Hero 的 activation）
// 读进平台，让它的号码出现在「号码」页、能取码。
//
// **只读，绝不购买。** 产品负责人 2026-09-06 在生产上问「那我们购买的号码接码
// 问题呢」——62 那几十个号是在 62 后台买的，平台的号码页只装通过平台买的号，
// 而 ImportByUpstreamID 此前只在人工核对 unknown 那条路里用。这一条把它变成
// 一个正经入口。
//
// 不写操作台账：它不花钱，也没有「不知道成没成」的第三态需要收敛；
// 「谁在什么时候导入了哪一单」由 Action 内核的审计负责。
func (s *Service) ImportUpstream(ctx context.Context, provider, upstreamRef string) ([]Resource, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return nil, err
	}
	if err := s.requireVerified(ctx, provider); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(upstreamRef)
	if ref == "" {
		return nil, fmt.Errorf("%w: 上游订单 / activation ID 为空", ErrActionNotSupported)
	}
	imported, err := adapter.ImportByUpstreamID(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(imported) == 0 {
		return nil, fmt.Errorf("上游 %s 的 %s 里没有号码", provider, ref)
	}

	// 62 是订单制：先落订单行，号码指回它——号码页与人工核对都靠这条关联。
	orderID := ""
	if provider == ProviderSMS62 {
		id, err := s.store.UpsertOrder(ctx, Order{
			Provider: provider, ProviderOrderID: ref,
			Quantity: len(imported), Status: "imported",
		})
		if err != nil {
			return nil, fmt.Errorf("落导入的订单: %w", err)
		}
		orderID = id
	}

	out := make([]Resource, 0, len(imported))
	for _, r := range imported {
		r.Provider = provider
		r.OrderID = orderID
		r.SyncedAt = s.now()
		id, err := s.store.UpsertResource(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("落导入的号码: %w", err)
		}
		r.ID = id
		out = append(out, r)
	}
	return out, nil
}
