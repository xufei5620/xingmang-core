package reqlogformat

import (
	"strings"
	"testing"
)

func TestParseTurnsChatMessages(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[
		{"role":"system","content":"be terse"},
		{"role":"user","content":"hi"}
	]}`
	turns := ParseTurns(body)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].Role != "system" || turns[0].Text != "be terse" {
		t.Fatalf("turn0 = %+v", turns[0])
	}
	if turns[1].Role != "user" || turns[1].Text != "hi" {
		t.Fatalf("turn1 = %+v", turns[1])
	}
}

func TestParseTurnsResponsesAPIInstructionsAndInput(t *testing.T) {
	body := `{"instructions":"be terse","input":"hi"}`
	turns := ParseTurns(body)
	if len(turns) != 2 {
		t.Fatalf("got %d turns: %+v", len(turns), turns)
	}
	if turns[0].Role != "system" || turns[0].Text != "be terse" {
		t.Fatalf("turn0 = %+v", turns[0])
	}
	if turns[1].Role != "user" || turns[1].Text != "hi" {
		t.Fatalf("turn1 = %+v", turns[1])
	}
}

func TestParseTurnsResponsesAPIFunctionCallRoundTrip(t *testing.T) {
	body := `{"input":[
		{"type":"function_call","name":"lookup","arguments":"{\"q\":\"x\"}"},
		{"type":"function_call_output","output":"42"}
	]}`
	turns := ParseTurns(body)
	if len(turns) != 2 {
		t.Fatalf("got %d turns: %+v", len(turns), turns)
	}
	if turns[0].Role != "assistant" || turns[0].Text == "" {
		t.Fatalf("turn0 = %+v", turns[0])
	}
	if turns[1].Role != "tool" || turns[1].Text == "" {
		t.Fatalf("turn1 = %+v", turns[1])
	}
}

func TestParseTurnsAnthropicSystemBlock(t *testing.T) {
	body := `{"system":[{"type":"text","text":"be terse"}],"messages":[{"role":"user","content":"hi"}]}`
	turns := ParseTurns(body)
	if len(turns) != 2 || turns[0].Role != "system" || turns[0].Text != "be terse" {
		t.Fatalf("got %+v", turns)
	}
}

func TestParseTurnsUnrecognizedShapeReturnsNil(t *testing.T) {
	// embedding 请求：有 model/input，但 input 是纯字符串数组，不是
	// message/function_call 对象——这正是契约样本 #5（messagesSeen=false）
	// 覆盖的场景：MessagesParsed 由调用方按 len(turns)>0 推导，这里必须是 0。
	body := `{"model":"text-embedding-3-large","input":["a","b"]}`
	if turns := ParseTurns(body); len(turns) != 0 {
		t.Fatalf("got %+v, want no turns", turns)
	}
}

func TestParseTurnsInvalidJSONReturnsNil(t *testing.T) {
	if turns := ParseTurns("not json"); turns != nil {
		t.Fatalf("got %+v, want nil", turns)
	}
}

func TestParseTurnsToolUseAndThinkingBlocks(t *testing.T) {
	body := `{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"considering options"},
		{"type":"tool_use","name":"calc","input":{"a":1}}
	]}]}`
	turns := ParseTurns(body)
	if len(turns) != 1 {
		t.Fatalf("got %d turns: %+v", len(turns), turns)
	}
	text := turns[0].Text
	for _, want := range []string{"💭 [思考]", "considering options", "🔧 [工具调用 calc]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("got %q, missing %q", text, want)
		}
	}
}
