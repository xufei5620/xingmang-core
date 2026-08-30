package reqlogformat

import (
	"strings"
	"testing"
)

func TestSSEEventsSkipsDoneAndBlankLines(t *testing.T) {
	body := "data: {\"a\":1}\n\ndata: [DONE]\n\n  \ndata:{\"a\":2}\n\n"
	var got []float64
	found := SSEEvents(body, func(ev map[string]interface{}) {
		got = append(got, fnum(ev["a"]))
	})
	if !found {
		t.Fatal("应至少解出一条事件")
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v, want [1 2]（[DONE] 与空行必须被跳过）", got)
	}
}

func TestSSEEventsIgnoresMalformedPayload(t *testing.T) {
	body := "data: {not json}\n\ndata: {\"a\":3}\n\n"
	var got []float64
	SSEEvents(body, func(ev map[string]interface{}) { got = append(got, fnum(ev["a"])) })
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("坏行应被跳过而不是整体失败: got %v", got)
	}
}

func TestIsSSE(t *testing.T) {
	cases := map[string]bool{
		"data: {\"a\":1}\n\n":                       true,
		"event: message\ndata: {\"a\":1}\n\n":       true,
		`{"choices":[{"message":{"content":"x"}}]}`: false,
		"": false,
	}
	for body, want := range cases {
		if got := IsSSE(body); got != want {
			t.Errorf("IsSSE(%q) = %v, want %v", body, got, want)
		}
	}
}

// --- ExtractFinalText：SSE 抄录 ---

func TestExtractFinalTextAssemblesAnthropicSSE(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	got := ExtractFinalText(body)
	if got != "你好，世界" {
		t.Fatalf("ExtractFinalText = %q, want %q", got, "你好，世界")
	}
}

func TestExtractFinalTextAssemblesAnthropicToolUse(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"lookup"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"weather\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		"",
	}, "\n\n")
	got := ExtractFinalText(body)
	if !strings.Contains(got, "🔧 [工具调用 lookup]") || !strings.Contains(got, `{"q":"weather"}`) {
		t.Fatalf("ExtractFinalText 未正确装配工具调用: %q", got)
	}
}

func TestExtractFinalTextAssemblesOpenAIChatChunk(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"},"index":0}]}`,
		`data: {"choices":[{"delta":{"content":"lo"},"index":0}]}`,
		`data: {"choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	if got := ExtractFinalText(body); got != "Hello" {
		t.Fatalf("ExtractFinalText = %q, want %q", got, "Hello")
	}
}

func TestExtractFinalTextAssemblesResponsesAPIDelta(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"foo"}`,
		`data: {"type":"response.output_text.delta","delta":"bar"}`,
		"",
	}, "\n\n")
	if got := ExtractFinalText(body); got != "foobar" {
		t.Fatalf("ExtractFinalText = %q, want %q", got, "foobar")
	}
}

func TestExtractFinalTextResponsesAPIFunctionCallDone(t *testing.T) {
	body := `data: {"type":"response.output_item.done","item":{"type":"function_call","name":"lookup","arguments":"{}"}}` + "\n\n"
	got := ExtractFinalText(body)
	if !strings.Contains(got, "🔧 [工具调用 lookup]") {
		t.Fatalf("ExtractFinalText 未标注工具调用: %q", got)
	}
}

// --- ExtractFinalText：非流式 ---

func TestExtractFinalTextNonStreamAnthropic(t *testing.T) {
	body := `{"id":"x","content":[{"type":"text","text":"你好"}]}`
	if got := ExtractFinalText(body); got != "你好" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFinalTextNonStreamChatWithToolCalls(t *testing.T) {
	body := `{"choices":[{"message":{"content":"看看天气","tool_calls":[{"function":{"name":"lookup","arguments":"{}"}}]}}]}`
	got := ExtractFinalText(body)
	if !strings.HasPrefix(got, "看看天气") || !strings.Contains(got, "🔧 [工具调用 lookup]") {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFinalTextNonStreamResponsesOutput(t *testing.T) {
	body := `{"output":[{"type":"message","content":[{"type":"output_text","text":"结果"}]}]}`
	if got := ExtractFinalText(body); got != "结果" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFinalTextErrorBody(t *testing.T) {
	body := `{"error":{"message":"boom"}}`
	if got := ExtractFinalText(body); got != "[错误] boom" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFinalTextUnrecognizedShapeReturnsEmpty(t *testing.T) {
	body := `{"object":"list","data":[1,2,3]}`
	if got := ExtractFinalText(body); got != "" {
		t.Fatalf("未识别的形状应返回空串而不是伪造内容: got %q", got)
	}
	if got := ExtractFinalText("not json at all"); got != "" {
		t.Fatalf("非法 JSON 应返回空串: got %q", got)
	}
}
