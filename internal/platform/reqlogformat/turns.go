package reqlogformat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Turn 是从请求正文解析出的一条对话轮次。
type Turn struct {
	Role string
	Text string
}

func jstr(m map[string]interface{}, k string) string {
	s, _ := m[k].(string)
	return s
}

// textOfContent 把 Anthropic/OpenAI Responses 风格的分块 content
// （字符串或 []block）拍平成一段可读文本，工具调用/工具结果/思考块
// 转成带标注的占位文本。
//
// 与桌面端原型 viewer.go 的同名函数逐字段一致（含 emoji 标注）——那不是
// 装饰，是详情页的人在一段纯文本气泡里区分"这段是工具调用"还是"这段是
// 模型自己说的话"的唯一线索，改标注格式属于产品行为变化，不在本次收编
// 范围内。
func textOfContent(v interface{}) string {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		var b strings.Builder
		for _, it := range c {
			m, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			switch jstr(m, "type") {
			case "text", "input_text", "output_text":
				b.WriteString(jstr(m, "text"))
			case "image", "input_image", "image_url":
				b.WriteString("\n[图片]\n")
			case "document":
				b.WriteString("\n[文档]\n")
			case "tool_use":
				inp, _ := json.Marshal(m["input"])
				b.WriteString(fmt.Sprintf("\n🔧 [工具调用 %s] %s", jstr(m, "name"), Truncate(string(inp), 2000)))
			case "tool_result":
				rc, _ := json.Marshal(m["content"])
				b.WriteString(fmt.Sprintf("\n📥 [工具结果] %s\n", Truncate(string(rc), 2000)))
			case "thinking":
				b.WriteString("\n💭 [思考] " + Truncate(jstr(m, "thinking"), 1500) + "\n")
			}
		}
		return b.String()
	}
	return ""
}

// ParseTurns 把一次请求的原始 JSON 正文解析成分角色对话轮次。
//
// 识别三种上游请求形状（同一份逻辑，逐字段照搬自桌面端原型 viewer.go）：
//   - OpenAI Responses API：`instructions` / `system` / `input`（字符串或
//     分块数组，元素可以是 message / function_call / function_call_output /
//     reasoning）；
//   - Chat Completions 风格：`messages` 数组；
//   - 两者都缺时返回 nil——调用方据此判断"这条请求体不是可识别的对话形状"
//     （embedding 请求就是典型例子：只有 model/input=[]string，没有上面任何
//     一种结构）。
//
// 解析失败（不是合法 JSON）同样返回 nil，与"没有可识别字段"是同一种调用方
// 处置（原文仍然可读，只是不渲染成对话气泡），不用两套返回值区分——契约层
// 的 MessagesParsed 由调用方按 `len(turns) > 0` 推导即可，这与本包自身的
// 参考实现（fake.go 的样本）逐条核对过：唯一一条"有已知字段但未产出轮次"
// 的样本（embedding 请求的 `input:[]string`）在契约里就是 messagesSeen=false。
func ParseTurns(reqBody string) []Turn {
	var d map[string]interface{}
	if json.Unmarshal([]byte(reqBody), &d) != nil {
		return nil
	}
	var turns []Turn
	if ins := jstr(d, "instructions"); ins != "" {
		turns = append(turns, Turn{"system", ins})
	}
	if sys, ok := d["system"]; ok {
		if s := textOfContent(sys); s != "" {
			turns = append(turns, Turn{"system", s})
		}
	}
	addItem := func(m map[string]interface{}) {
		switch jstr(m, "type") {
		case "function_call":
			turns = append(turns, Turn{"assistant", fmt.Sprintf("🔧 [工具调用 %s] %s", jstr(m, "name"), Truncate(jstr(m, "arguments"), 2000))})
		case "function_call_output":
			out := jstr(m, "output")
			if out == "" {
				b, _ := json.Marshal(m["output"])
				out = string(b)
			}
			turns = append(turns, Turn{"tool", "📥 [工具结果] " + Truncate(out, 2000)})
		case "reasoning":
			if s := textOfContent(m["summary"]); s != "" {
				turns = append(turns, Turn{"assistant", "💭 [推理摘要] " + s})
			}
		default: // message 或无 type
			role := jstr(m, "role")
			if role == "" {
				role = "user"
			}
			if s := textOfContent(m["content"]); s != "" {
				turns = append(turns, Turn{role, s})
			}
		}
	}
	if in, ok := d["input"]; ok {
		switch iv := in.(type) {
		case string:
			turns = append(turns, Turn{"user", iv})
		case []interface{}:
			for _, item := range iv {
				if m, ok := item.(map[string]interface{}); ok {
					addItem(m)
				}
			}
		}
	}
	if msgs, ok := d["messages"].([]interface{}); ok {
		for _, item := range msgs {
			if m, ok := item.(map[string]interface{}); ok {
				addItem(m)
			}
		}
	}
	return turns
}
