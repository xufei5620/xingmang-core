package assurance

import (
	"context"
	"fmt"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// FakeFailingModelSuffix：model 名以这个后缀结尾时，FakeClient 确定性地
// 返回一次失败（而不是健康响应）。测试/演示时声明一个
// "<真实模型名>-fake-fail" 的 target.model，就能在 fake 模式下确定性地
// 验证"单个 target 失败不影响批次其余 target"这条纪律（设计稿 §3.2 步骤
// 5），不需要任何真实厂商配合。
const FakeFailingModelSuffix = "-fake-fail"

// FakeClient 是 XM-ASSURE1-core 的 fake 模式探测客户端：立即返回声明模板
// 对应的固定"健康"响应，零网络调用（设计稿 §3 步骤 3）。
//
// 每个模板的固定响应都设计成能让 templates.Assess 判定为 ResultStatusOK
// ——这是"fake 模式验证全链路"的字面意思：声明 → 探测 → 结论走完整条路径，
// 断言逻辑本身不是摆设。
type FakeClient struct{}

// NewFakeClient 构造 fake 探测客户端。
func NewFakeClient() *FakeClient { return &FakeClient{} }

// Complete 实现 ProbeClient。
func (c *FakeClient) Complete(ctx context.Context, req ProbeRequest) (ProbeResponse, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResponse{}, err
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(req.Model)), FakeFailingModelSuffix) {
		return ProbeResponse{}, connector.NewError(connector.KindUnavailable,
			"assurance.fake_probe", fmt.Errorf("模拟的探测失败（fake 模式固定失败模型 %q）", req.Model))
	}

	text, err := fakeHealthyText(req.TemplateKey, req.Model)
	if err != nil {
		return ProbeResponse{}, err
	}
	tokens := 8
	if req.MaxTokens > 0 && tokens > req.MaxTokens {
		tokens = req.MaxTokens
	}
	firstToken := int64(5)
	return ProbeResponse{
		Text:         text,
		HTTPStatus:   200,
		TokensUsed:   tokens,
		LatencyMS:    12,
		FirstTokenMS: &firstToken,
		Measured:     true,
	}, nil
}

// fakeHealthyText 按模板返回一个能让 Assess 判定为 ok 的固定响应文本。
func fakeHealthyText(templateKey, model string) (string, error) {
	switch templateKey {
	case TemplateModelFingerprint:
		return fmt.Sprintf("我是 %s（fake 模式固定响应）", expectedFingerprintFragment(model)), nil
	case TemplateBenchmarkSet:
		var b strings.Builder
		for _, q := range benchmarkQuestions {
			b.WriteString(q.Answer)
			b.WriteByte('\n')
		}
		return b.String(), nil
	case TemplateContextLength:
		return lastRunes(contextLengthFixedInput, 32), nil
	case TemplateMinViableRequest:
		return "pong", nil
	default:
		return "", fmt.Errorf("未知的 prompt_template_key %q: %w", templateKey, ErrInvalidInput)
	}
}
