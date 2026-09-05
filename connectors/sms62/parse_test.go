package sms62

import "testing"

// 正文里出现多个候选时**不猜**。
//
// 这是取码这段代码最要紧的一条。「您的验证码是 123456，订单号 998877」有两个
// 4–8 位数字；猜错一个填进付款页，代价是一次失败的注册加一个被烧掉的号。
func TestExtractCodeRefusesToGuessAmongCandidates(t *testing.T) {
	got := ExtractCode(Message{Text: "您的验证码是 123456，订单号 998877"})
	if got != "" {
		t.Fatalf("多个候选必须返回空, got %q", got)
	}
}

// 唯一候选就取它。
func TestExtractCodeTakesTheOnlyCandidate(t *testing.T) {
	if got := ExtractCode(Message{Text: "Your code is 481920. Do not share."}); got != "481920" {
		t.Fatalf("唯一候选应被取出, got %q", got)
	}
}

// 同一个数字重复出现仍是同一个候选。
//
// 「验证码 1234，请输入 1234」在真实短信里很常见，把它当成两个候选会让这条
// 短信永远取不出码。
func TestExtractCodeTreatsRepeatedDigitsAsOneCandidate(t *testing.T) {
	if got := ExtractCode(Message{Text: "验证码 1234，请输入 1234"}); got != "1234" {
		t.Fatalf("重复的同一候选应算一个, got %q", got)
	}
}

// 长号码不会被切出一段来当验证码。
//
// 朴素的 \d{4,8} 会从 13800138000 里切出 8 位。要求前后是非数字边界。
func TestExtractCodeIgnoresDigitsInsideLongerNumbers(t *testing.T) {
	if got := ExtractCode(Message{Text: "来自 13800138000 的通知"}); got != "" {
		t.Fatalf("长号码里不该切出验证码, got %q", got)
	}
}

// 上游给的结构化字段优先于正文。
func TestExtractCodePrefersStructuredField(t *testing.T) {
	m := Message{Text: "您的验证码是 123456，订单号 998877", StructuredCode: "654321"}
	if got := ExtractCode(m); got != "654321" {
		t.Fatalf("结构化字段应优先, got %q", got)
	}
}

// 结构化字段里装的不是验证码形状的东西就当没有。
//
// 把一段商户名当验证码填进去，比没有验证码糟得多。
func TestExtractCodeIgnoresNonCodeStructuredField(t *testing.T) {
	m := Message{Text: "Your code is 481920.", StructuredCode: "OPENAI"}
	if got := ExtractCode(m); got != "481920" {
		t.Fatalf("非验证码形状的结构化字段应被忽略, got %q", got)
	}
}

// 多条短信取**最新**的那条，不是第一条。
//
// 上游返回顺序没有文档保证；拿一条五分钟前的旧码去填当前验证，
// 会失败得毫无头绪。
func TestPickLatestChoosesNewestWithCode(t *testing.T) {
	msgs := []Message{
		{Text: "code 111111", ReceivedAt: 100},
		{Text: "没有码的通知"},
		{Text: "code 222222", ReceivedAt: 300},
		{Text: "code 333333", ReceivedAt: 200},
	}
	_, code, ok := PickLatest(msgs)
	if !ok || code != "222222" {
		t.Fatalf("应取最新的那条, got %q ok=%v", code, ok)
	}
}

// 一条都取不出码时明确说没有，而不是回一个空串让调用方自己猜。
func TestPickLatestReportsNotFound(t *testing.T) {
	if _, _, ok := PickLatest([]Message{{Text: "没有码"}}); ok {
		t.Fatal("取不出码时应返回 false")
	}
}
