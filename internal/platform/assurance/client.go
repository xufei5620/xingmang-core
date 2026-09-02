package assurance

import "context"

// ProbeRequest 是发给被探测模型的一次探测请求（OpenAI 兼容 chat/completions
// 形状的最小抽象——真正的 HTTP 请求构造在 realclient.go）。
type ProbeRequest struct {
	// TemplateKey 告诉客户端"这是哪一种探测"，fake 客户端据此挑一个确定性的
	// 健康响应；real 客户端不需要它（它只是原样把 Messages 发出去），但保留
	// 这个字段方便两侧客户端的实现与测试断言"这次调用是为了哪个模板"。
	TemplateKey string
	Model       string
	Messages    []ProbeMessage
	MaxTokens   int
}

// ProbeResponse 是一次探测的响应。
//
// **不含任何原始响应字段之外的东西**：Text 是解出来的纯文本，供
// templates.Assess 判定；不落库（probe_result 表本身也没有存原始文本的
// 字段，见 types.go Result 的文档注释，威胁模型 §7.1）。
type ProbeResponse struct {
	Text       string
	HTTPStatus int
	TokensUsed int
	LatencyMS  int64
	// FirstTokenMS / Measured：首字节延迟只在真实测到时才填（ASSURE0 的
	// MeasuredTTFB 纪律同款）。fake 模式为了让整条链路（包括
	// measured_first_token=true 这个展示态分支）在测试里可验证，会给出一个
	// 明确标注为模拟值的确定性数字，而不是省略这一整个能力。
	FirstTokenMS *int64
	Measured     bool
}

// ProbeClient 是探测客户端的最小抽象；fake.go 的 FakeClient 与
// realclient.go 的 RealClient 都实现它。
type ProbeClient interface {
	Complete(ctx context.Context, req ProbeRequest) (ProbeResponse, error)
}
