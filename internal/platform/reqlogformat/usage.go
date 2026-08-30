package reqlogformat

import (
	"encoding/json"
	"strings"
)

// ExtractUsage 从一次响应正文（SSE 事件流或单个 JSON 对象）里取出
// token 用量：输入、输出、缓存读。任一项没有对应字段时保持 0——
// 与契约层 RequestLogSummary.TokensIn/Out/Cache 的口径一致
// （上游没给就是 0，不用指针，见 connectors/reqlog/contract.go）。
//
// 兼容的字段名（同一份逻辑，逐字段照搬自桌面端原型 viewer.go）：
//   - Anthropic：`usage.input_tokens` / `usage.output_tokens` /
//     `usage.cache_read_input_tokens`；
//   - OpenAI Chat：`usage.prompt_tokens` / `usage.completion_tokens` /
//     `usage.prompt_tokens_details.cached_tokens`；
//   - OpenAI Responses：`usage.input_tokens_details.cached_tokens`；
//   - usage 对象可能挂在事件顶层、`response.usage`，或（Anthropic
//     message_delta 事件）`message.usage`。
func ExtractUsage(body string) (in, out, cache int) {
	grab := func(u map[string]interface{}) {
		if v, ok := u["input_tokens"]; ok {
			in = int(fnum(v))
		}
		if v, ok := u["prompt_tokens"]; ok {
			in = int(fnum(v))
		}
		if v, ok := u["output_tokens"]; ok {
			out = int(fnum(v))
		}
		if v, ok := u["completion_tokens"]; ok {
			out = int(fnum(v))
		}
		if v, ok := u["cache_read_input_tokens"]; ok {
			cache = int(fnum(v))
		}
		if d, ok := u["input_tokens_details"].(map[string]interface{}); ok {
			if v, ok := d["cached_tokens"]; ok {
				cache = int(fnum(v))
			}
		}
		if d, ok := u["prompt_tokens_details"].(map[string]interface{}); ok {
			if v, ok := d["cached_tokens"]; ok {
				cache = int(fnum(v))
			}
		}
	}
	pick := func(ev map[string]interface{}) {
		if u, ok := ev["usage"].(map[string]interface{}); ok {
			grab(u)
		}
		if r, ok := ev["response"].(map[string]interface{}); ok {
			if u, ok := r["usage"].(map[string]interface{}); ok {
				grab(u)
			}
		}
		if m, ok := ev["message"].(map[string]interface{}); ok {
			if u, ok := m["usage"].(map[string]interface{}); ok {
				grab(u)
			}
		}
	}
	if IsSSE(body) {
		SSEEvents(body, pick)
		return
	}
	var d map[string]interface{}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &d) == nil {
		pick(d)
	}
	return
}
