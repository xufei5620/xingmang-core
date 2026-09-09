package sms

import (
	"errors"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/connectors/sms62"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// definitelyRejected 判断一个上游错误是否**确定没有花钱**。
//
// 这是整套幂等设计的支点。判错一边的后果不对称：
//
//	把「确定失败」误判成不确定 → 多一条要人工核对的记录（烦，但不丢钱）
//	把「不确定」误判成确定失败 → 页面告诉人可以重来 → **重复买号**
//
// 所以这个函数**只在有明确依据时返回 true**，其余一律当不确定。
// 默认分支是 false，这是刻意的。
func definitelyRejected(err error) bool {
	if err == nil {
		return false
	}

	// 62：HTTP 2xx + 业务码非 1 = 上游明确拒绝了这次请求（余额不足、
	// 商品下架、参数不对）。这类拒绝发生在扣费之前。
	var business *sms62.BusinessError
	if errors.As(err, &business) {
		// 但 5xx 上的业务错误仍算不确定：那时上游自己也不知道处理到哪儿了。
		return business.HTTPStatus < 500
	}

	// Hero：4xx 与 5xx 已经在连接器里落到不同类型上了。
	var rejected *herosms.RejectedError
	if errors.As(err, &rejected) {
		return true
	}
	var unknownOutcome *herosms.UnknownOutcomeError
	if errors.As(err, &unknownOutcome) {
		return false
	}

	// 本地参数校验失败：**根本没发出去**，当然没花钱。
	var connErr *connector.Error
	if errors.As(err, &connErr) {
		switch connErr.Kind {
		case connector.KindRejected, connector.KindAuth, connector.KindNotSupported,
			connector.KindMethodNotAllowed, connector.KindForbiddenTarget, connector.KindIPNotAllowed:
			return true
		}
	}

	// 协议错误、传输超时、以及任何没见过的错误：**一律不确定**。
	// 一个解不开的响应说明不了上游做了什么。
	return false
}
