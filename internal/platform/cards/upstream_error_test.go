package cards

import (
	"errors"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 每一类上游错误都要翻成指向下一步的话。
//
// 不翻的话页面上只有「action cards.card.issue 执行失败」，而这句话对
// 改配置、改金额、等一等、别动等人这四种完全不同的处置一视同仁。
func TestExplainUpstreamGivesActionableMessages(t *testing.T) {
	cases := map[connector.ErrorKind][]string{
		connector.KindAuth:         {"密钥", "时钟"},
		connector.KindIPNotAllowed: {"IP", "白名单"},
		connector.KindRateLimited:  {"限流", "确定没有生效"},
		connector.KindRejected:     {"确定没有生效"},
	}
	for kind, wants := range cases {
		root := errors.New("上游原文")
		got := explainUpstream(connector.NewError(kind, "op", root))
		var ae *action.Error
		if !errors.As(got, &ae) {
			t.Fatalf("%s 应包装成 Action 错误, got %T", kind, got)
		}
		for _, want := range wants {
			if !strings.Contains(ae.Message, want) {
				t.Fatalf("%s 的文案应提到 %q, got %q", kind, want, ae.Message)
			}
		}
		// 上游原文绝不进对外文案（ADR-004）。
		if strings.Contains(ae.Message, "上游原文") {
			t.Fatalf("%s 的文案泄漏了上游原文: %q", kind, ae.Message)
		}
		// 但它必须留在 cause 链里，供服务端日志与审计追查。
		// 连接器错误的 Error() 本身按 ADR-004 不含上游原文，原文在更深一层，
		// 所以这里用 errors.Is 沿链查找而不是看某一层的文本。
		if !errors.Is(got, root) {
			t.Fatalf("%s 丢了根因", kind)
		}
	}
}

// 「可能已经生效」的两类必须明确劝阻重试——这是整套幂等设计的最后一道
// 人工闸门，措辞含糊会让人顺手再点一次。
func TestExplainUpstreamDiscouragesRetryOnUncertainOutcomes(t *testing.T) {
	for _, kind := range []connector.ErrorKind{connector.KindUnavailable, connector.KindBadResponse} {
		got := explainUpstream(connector.NewError(kind, "op", errors.New("x")))
		var ae *action.Error
		if !errors.As(got, &ae) {
			t.Fatalf("%s 应包装成 Action 错误", kind)
		}
		if !strings.Contains(ae.Message, "请勿重试") {
			t.Fatalf("%s 必须劝阻重试, got %q", kind, ae.Message)
		}
		if !strings.Contains(ae.Message, "可能已经生效") {
			t.Fatalf("%s 必须说清「可能已经生效」, got %q", kind, ae.Message)
		}
	}
}

// 领域层自己的错误不该被包装：它们本来就是中文且指向明确。
func TestExplainUpstreamLeavesDomainErrorsAlone(t *testing.T) {
	domain := ErrPerOperationExceeded
	if got := explainUpstream(domain); !errors.Is(got, ErrPerOperationExceeded) {
		t.Fatalf("领域错误不该被改写: %v", got)
	}
	if explainUpstream(nil) != nil {
		t.Fatal("nil 应原样返回")
	}
}
