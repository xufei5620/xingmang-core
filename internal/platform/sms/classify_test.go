package sms

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/connectors/sms62"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 这个分档是整套幂等设计的支点，后果不对称：
//
//	把「确定失败」误判成不确定 → 多一条要人工核对的记录（烦，但不丢钱）
//	把「不确定」误判成确定失败 → 页面告诉人可以重来 → **重复买号**
//
// 所以默认必须是「不确定」。
func TestDefinitelyRejectedOnlyWithEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"62 业务拒绝（HTTP 2xx + code≠1）钱没花", &sms62.BusinessError{HTTPStatus: 200, Code: 400}, true},
		{"62 的 5xx 业务错误仍是不确定", &sms62.BusinessError{HTTPStatus: 502, Code: 500}, false},
		{"Hero 4xx 是明确拒绝", &herosms.RejectedError{Status: http.StatusBadRequest}, true},
		{"Hero 5xx 是不确定", &herosms.UnknownOutcomeError{Status: http.StatusBadGateway}, false},
		{"62 协议错误是不确定", &sms62.ProtocolError{Kind: "响应信封"}, false},
		{"Hero 协议错误是不确定", &herosms.ProtocolError{Kind: "响应结构"}, false},
		{"本地参数校验失败：根本没发出去", connector.NewError(connector.KindRejected, "参数非法", nil), true},
		{"凭据问题：也没发出去", connector.NewError(connector.KindAuth, "密钥未配置", nil), true},
		{"传输不可用是不确定", connector.NewError(connector.KindUnavailable, "超时", nil), false},
		{"没见过的错误一律不确定", errors.New("某种没见过的错误"), false},
		{"nil 不是拒绝", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := definitelyRejected(tc.err); got != tc.want {
				t.Fatalf("definitelyRejected = %v, want %v", got, tc.want)
			}
		})
	}
}

// 包装过的错误也要认出来：Service 会把上游错误裹进自己的上下文里。
func TestDefinitelyRejectedSeesThroughWrapping(t *testing.T) {
	wrapped := errors.Join(errors.New("购买失败"), &herosms.RejectedError{Status: 422})
	if !definitelyRejected(wrapped) {
		t.Fatal("包装过的明确拒绝也应被认出")
	}
}
