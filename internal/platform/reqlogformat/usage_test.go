package reqlogformat

import "testing"

func TestExtractUsageAnthropicNonStream(t *testing.T) {
	body := `{"id":"x","usage":{"input_tokens":120,"output_tokens":45,"cache_read_input_tokens":80}}`
	in, out, cache := ExtractUsage(body)
	if in != 120 || out != 45 || cache != 80 {
		t.Fatalf("got in=%d out=%d cache=%d", in, out, cache)
	}
}

func TestExtractUsageOpenAIChatNonStream(t *testing.T) {
	body := `{"usage":{"prompt_tokens":30,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":10}}}`
	in, out, cache := ExtractUsage(body)
	if in != 30 || out != 12 || cache != 10 {
		t.Fatalf("got in=%d out=%d cache=%d", in, out, cache)
	}
}

func TestExtractUsageOpenAIResponsesCacheDetails(t *testing.T) {
	body := `{"usage":{"input_tokens":200,"output_tokens":50,"input_tokens_details":{"cached_tokens":150}}}`
	in, out, cache := ExtractUsage(body)
	if in != 200 || out != 50 || cache != 150 {
		t.Fatalf("got in=%d out=%d cache=%d", in, out, cache)
	}
}

func TestExtractUsageFromSSEMessageDelta(t *testing.T) {
	// Anthropic 的用量常挂在流末尾的 message_delta 事件里，usage 嵌套在 "message" 键下
	body := "data: " + `{"type":"message_delta","message":{"usage":{"input_tokens":9,"output_tokens":4}}}` + "\n\n"
	in, out, _ := ExtractUsage(body)
	if in != 9 || out != 4 {
		t.Fatalf("got in=%d out=%d", in, out)
	}
}

func TestExtractUsageFromSSEResponseUsage(t *testing.T) {
	// Responses API 有时把 usage 挂在事件的 "response" 键下
	body := "data: " + `{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n"
	in, out, _ := ExtractUsage(body)
	if in != 5 || out != 2 {
		t.Fatalf("got in=%d out=%d", in, out)
	}
}

func TestExtractUsageAccumulatesAcrossSSEEvents(t *testing.T) {
	// 用量分散在多个事件里出现时，后出现的字段覆盖先出现的（与原实现一致：
	// grab() 逐次覆盖同名字段，最后一次出现的值生效）
	body := "data: " + `{"usage":{"input_tokens":10}}` + "\n\n" +
		"data: " + `{"usage":{"output_tokens":3}}` + "\n\n"
	in, out, _ := ExtractUsage(body)
	if in != 10 || out != 3 {
		t.Fatalf("got in=%d out=%d", in, out)
	}
}

func TestExtractUsageMissingFieldsStayZero(t *testing.T) {
	// 上游没给用量字段：三项都保持 0（不是指针，契约层 TokensIn/Out/Cache
	// 的口径就是"没记到=0"，见 connectors/reqlog/contract.go）
	in, out, cache := ExtractUsage(`{"error":{"message":"rate limited"}}`)
	if in != 0 || out != 0 || cache != 0 {
		t.Fatalf("got in=%d out=%d cache=%d, want all zero", in, out, cache)
	}
	// 非法 JSON 同样保持零值，不报错
	in, out, cache = ExtractUsage("not json")
	if in != 0 || out != 0 || cache != 0 {
		t.Fatalf("非法 JSON 应静默返回零值: in=%d out=%d cache=%d", in, out, cache)
	}
}
