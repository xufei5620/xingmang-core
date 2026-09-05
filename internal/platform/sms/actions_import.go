package sms

import (
	"context"
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 取码与导入（XM-SMS1 补正，2026-09-06）。
//
// 产品负责人在生产上问「那我们购买的号码接码问题呢」，一查发现两件事：
//   - Service.FetchCode 从 SMS0 起就存在，但**没有任何入口在调它**——页面上的
//     「验证码」只读本地库，而本地库里的码要靠这个方法从上游拉；
//   - 在供应商后台下的单没法进平台，ImportByUpstreamID 只在人工核对那条路里用。
//
// 两个都是「后端有、入口没有」，所以补两个 Action。
const (
	ActionCodeFetch   = "sms.code.fetch"
	ActionOrderImport = "sms.order.import"
)

func codeFetchDef() action.Definition {
	return action.Definition{
		ID: ActionCodeFetch, Version: actionVersion,
		// 取码用 sms.read：它是拿到号之后每天要做的事，不花钱；
		// 码本身在返回体里，能不能看由 sms.reveal 决定（见 handler）。
		RiskLevel: action.L1, Permission: PermissionRead,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "resource_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// codeFetchHandler 向上游取一次码。
//
// 「还没有码」是**正常状态**（码要几秒到几十秒才来），不是失败：返回
// received=false，Action 算成功，页面据此继续等而不是报错。
func codeFetchHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		resourceID := stringParam(params, "resource_id")
		action.RecordResource(ctx, "sms_resource", resourceID)
		code, err := svc.FetchCode(ctx, resourceID)
		if errors.Is(err, ErrCodeNotAvailable) {
			action.RecordAfter(ctx, map[string]any{"received": false})
			return map[string]any{"received": false}, nil
		}
		if err != nil {
			return nil, err
		}
		// 审计摘要**不带码**：摘要会被广泛展示。
		action.RecordAfter(ctx, map[string]any{"received": true, "code_id": code.ID})
		return map[string]any{"received": true, "code_id": code.ID}, nil
	}
}

func orderImportDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionOrderImport, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			providerField(providers),
			// 62 是订单 ID（orders 页签里那一列），Hero 是 activation ID。
			{Name: "upstream_ref", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func orderImportHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		provider := stringParam(params, "provider")
		ref := stringParam(params, "upstream_ref")
		action.RecordResource(ctx, "sms_order", provider+"/"+ref)
		resources, err := svc.ImportUpstream(ctx, provider, ref)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(resources))
		for _, r := range resources {
			ids = append(ids, r.ID)
		}
		action.RecordAfter(ctx, map[string]any{"provider": provider, "upstream_ref": ref, "imported": len(ids)})
		return map[string]any{"provider": provider, "upstream_ref": ref, "imported": len(ids), "resource_ids": ids}, nil
	}
}
