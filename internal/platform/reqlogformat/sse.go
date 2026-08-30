package reqlogformat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SSEEvents 逐条解出 `data: {...}` 形态的 SSE 事件并调用 fn，跳过
// `data: [DONE]` 哨兵与空行。返回值表示是否至少解出一条事件。
func SSEEvents(body string, fn func(map[string]interface{})) bool {
	found := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(payload), &ev) == nil {
			found = true
			fn(ev)
		}
	}
	return found
}

// IsSSE 粗略判断一段响应正文是不是 SSE 事件流（而不是单个 JSON 对象）。
func IsSSE(body string) bool {
	bt := strings.TrimSpace(body)
	return strings.Contains(bt, "data:") && (strings.Contains(bt, "event:") || strings.HasPrefix(bt, "data:"))
}

func fnum(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

// ExtractFinalText 把一次响应正文（SSE 事件流或单个 JSON 对象）装配成
// 最终回复文本，工具调用同样转成带标注的占位文本插入正文中。
//
// 覆盖的响应形状（与桌面端原型 viewer.go 逐字段一致）：
//   - Anthropic Messages（`content_block_start/delta/stop`，SSE）与非流式
//     `content`；
//   - OpenAI Chat Completions（SSE `choices[0].delta.content` 与非流式
//     `choices[0].message`，含 `tool_calls`）；
//   - OpenAI Responses API（SSE `response.output_text.delta` /
//     `response.output_item.done` 与非流式 `output[]`）；
//   - 错误响应体（`error.message`，前缀 `[错误]`）。
//
// 未识别的形状返回空串——调用方（连接器）据此判断"装配不出内容"，
// 原始载荷仍然可读，不伪造一段看似成功的回复。
func ExtractFinalText(body string) string {
	if IsSSE(body) {
		var b strings.Builder
		toolArgs := map[int]*strings.Builder{} // anthropic tool input 拼接
		SSEEvents(body, func(ev map[string]interface{}) {
			switch jstr(ev, "type") {
			case "content_block_start": // anthropic
				if cb, ok := ev["content_block"].(map[string]interface{}); ok && jstr(cb, "type") == "tool_use" {
					idx := int(fnum(ev["index"]))
					b.WriteString(fmt.Sprintf("\n🔧 [工具调用 %s] ", jstr(cb, "name")))
					toolArgs[idx] = &strings.Builder{}
				}
			case "content_block_delta":
				if dl, ok := ev["delta"].(map[string]interface{}); ok {
					switch jstr(dl, "type") {
					case "text_delta":
						b.WriteString(jstr(dl, "text"))
					case "input_json_delta":
						idx := int(fnum(ev["index"]))
						if sb := toolArgs[idx]; sb != nil && sb.Len() < 2000 {
							sb.WriteString(jstr(dl, "partial_json"))
						}
					case "thinking_delta":
						// 思考不进最终文本
					}
				}
			case "content_block_stop":
				idx := int(fnum(ev["index"]))
				if sb := toolArgs[idx]; sb != nil {
					b.WriteString(Truncate(sb.String(), 2000) + "\n")
					delete(toolArgs, idx)
				}
			case "response.output_text.delta": // openai responses
				if s, ok := ev["delta"].(string); ok {
					b.WriteString(s)
				}
			case "response.output_item.done": // responses 工具调用
				if item, ok := ev["item"].(map[string]interface{}); ok && jstr(item, "type") == "function_call" {
					b.WriteString(fmt.Sprintf("\n🔧 [工具调用 %s] %s\n", jstr(item, "name"), Truncate(jstr(item, "arguments"), 2000)))
				}
			case "": // openai chat chunk
				if chs, ok := ev["choices"].([]interface{}); ok && len(chs) > 0 {
					if ch, ok := chs[0].(map[string]interface{}); ok {
						if dl, ok := ch["delta"].(map[string]interface{}); ok {
							b.WriteString(jstr(dl, "content"))
						}
					}
				}
			}
		})
		return b.String()
	}
	var d map[string]interface{}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &d) != nil {
		return ""
	}
	if c, ok := d["content"]; ok { // anthropic
		if s := textOfContent(c); s != "" {
			return s
		}
	}
	if chs, ok := d["choices"].([]interface{}); ok && len(chs) > 0 { // chat
		if ch, ok := chs[0].(map[string]interface{}); ok {
			if m, ok := ch["message"].(map[string]interface{}); ok {
				var b strings.Builder
				b.WriteString(jstr(m, "content"))
				if tcs, ok := m["tool_calls"].([]interface{}); ok {
					for _, tc := range tcs {
						if t, ok := tc.(map[string]interface{}); ok {
							if f, ok := t["function"].(map[string]interface{}); ok {
								b.WriteString(fmt.Sprintf("\n🔧 [工具调用 %s] %s", jstr(f, "name"), Truncate(jstr(f, "arguments"), 2000)))
							}
						}
					}
				}
				return b.String()
			}
		}
	}
	if out, ok := d["output"].([]interface{}); ok { // responses
		var b strings.Builder
		for _, o := range out {
			if m, ok := o.(map[string]interface{}); ok {
				switch jstr(m, "type") {
				case "message":
					b.WriteString(textOfContent(m["content"]))
				case "function_call":
					b.WriteString(fmt.Sprintf("\n🔧 [工具调用 %s] %s\n", jstr(m, "name"), Truncate(jstr(m, "arguments"), 2000)))
				case "reasoning":
					if s := textOfContent(m["summary"]); s != "" {
						b.WriteString("💭 " + s + "\n")
					}
				}
			}
		}
		return b.String()
	}
	if e, ok := d["error"].(map[string]interface{}); ok {
		return "[错误] " + jstr(e, "message")
	}
	return ""
}
