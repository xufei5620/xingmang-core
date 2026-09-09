package audit

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// TestActionSinkRedactsSummariesAtTheBoundary 回归 Codex 冷审 PR #47 第 2 条 /
// PR #43 head `0a0642c` 第 2 条：「摘要可把明文凭据直接回给前端……
// `action/audit.go` 明确把脱敏责任完全交给 Handler，现有 RedactDefault 未被强制」。
//
// 这里测的是**边界的机械保证**，不是 Handler 的自觉：入参刻意模拟一个忘了脱敏
// 的 Handler，断言凭据到不了 Event。宪法 7 条要求在对外边界 fail closed，而
// ActionSink 是所有 Action 审计事件唯一的入链口。
//
// 脱敏必须发生在**写入**侧：审计链不可篡改，明文凭据一旦进链就永远在那里——
// 改读 API 只是不再显示它，库、导出的链根、任何备份里它都还在。
func TestActionSinkRedactsSummariesAtTheBoundary(t *testing.T) {
	// 一个「忘了脱敏」的 Handler 会交出来的摘要
	leaky := action.AuditEvent{
		ActionID:      "registry.connection.create",
		ActionVersion: "1",
		Succeeded:     true,
		BeforeSummary: map[string]any{
			"instance_id": "sub2api-prod",
			"password":    "hunter2-plaintext",
		},
		AfterSummary: map[string]any{
			"instance_id":  "sub2api-prod",
			"api_key":      "sk-live-should-never-reach-the-chain",
			"Access_Token": "bearer-mixed-case-variant",
			"nested": map[string]any{
				"connector_secret": "nested-plaintext",
				"harmless":         "keep-me",
			},
			"connections": []any{
				map[string]any{"token": "inside-a-slice", "name": "primary"},
			},
		},
	}

	ev := eventFromActionEvent(leaky)

	// 顶层
	if ev.BeforeSummary["password"] != redactedPlaceholder {
		t.Fatalf("before_summary.password 未脱敏: %v", ev.BeforeSummary["password"])
	}
	if ev.AfterSummary["api_key"] != redactedPlaceholder {
		t.Fatalf("after_summary.api_key 未脱敏: %v", ev.AfterSummary["api_key"])
	}
	// 大小写变体：RedactDefault 用小写子串匹配，Access_Token 必须命中
	if ev.AfterSummary["Access_Token"] != redactedPlaceholder {
		t.Fatalf("大小写变体未脱敏: %v", ev.AfterSummary["Access_Token"])
	}
	// 嵌套 map
	nested, ok := ev.AfterSummary["nested"].(map[string]any)
	if !ok {
		t.Fatalf("嵌套结构类型漂移: %T", ev.AfterSummary["nested"])
	}
	if nested["connector_secret"] != redactedPlaceholder {
		t.Fatalf("嵌套 map 未脱敏: %v", nested["connector_secret"])
	}
	// 切片里的 map（XM-0031 补的下钻）
	list, ok := ev.AfterSummary["connections"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("切片结构漂移: %T %v", ev.AfterSummary["connections"], ev.AfterSummary["connections"])
	}
	inSlice, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("切片元素类型漂移: %T", list[0])
	}
	if inSlice["token"] != redactedPlaceholder {
		t.Fatalf("切片内的 map 未脱敏: %v", inSlice["token"])
	}

	// 非敏感字段一个都不能少：脱敏不是删摘要
	if ev.BeforeSummary["instance_id"] != "sub2api-prod" {
		t.Fatalf("非敏感字段被误伤: %v", ev.BeforeSummary["instance_id"])
	}
	if nested["harmless"] != "keep-me" {
		t.Fatalf("嵌套的非敏感字段被误伤: %v", nested["harmless"])
	}
	if inSlice["name"] != "primary" {
		t.Fatalf("切片内的非敏感字段被误伤: %v", inSlice["name"])
	}

	// 明文一个字都不许出现在整条事件的规范化字节里——
	// 那正是进哈希、进库、进导出链根的东西。
	canonical, err := ev.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for _, plaintext := range []string{
		"hunter2-plaintext",
		"sk-live-should-never-reach-the-chain",
		"bearer-mixed-case-variant",
		"nested-plaintext",
		"inside-a-slice",
	} {
		if strings.Contains(string(canonical), plaintext) {
			t.Fatalf("明文 %q 出现在进链的规范化字节里: %s", plaintext, canonical)
		}
	}
}

// TestActionSinkRedactionDoesNotMutateCaller：脱敏返回新 map，
// 调用方手里的原始摘要不被改写。
//
// 重要性不在洁癖：内核在调 AuditSink 之后可能还要用同一个 map（比如返回给
// Action 调用方或写进 action_run）。就地改写会让那些地方莫名其妙变成
// [REDACTED]，而且改动来自一个看起来只是"记日志"的调用。
func TestActionSinkRedactionDoesNotMutateCaller(t *testing.T) {
	original := map[string]any{"password": "hunter2-plaintext"}
	nested := map[string]any{"secret": "nested-plaintext"}
	originalWithNested := map[string]any{"cfg": nested}

	_ = eventFromActionEvent(action.AuditEvent{
		BeforeSummary: original,
		AfterSummary:  originalWithNested,
		Succeeded:     true,
	})

	if original["password"] != "hunter2-plaintext" {
		t.Fatalf("入参被就地改写: %v", original["password"])
	}
	if nested["secret"] != "nested-plaintext" {
		t.Fatalf("嵌套入参被就地改写: %v", nested["secret"])
	}
}

// TestActionSinkKeepsNilSummariesNil：没有前后镜像的动作（读类、被拒的执行）
// 摘要保持 nil，不被脱敏顺手变成空 map。
//
// 「没有镜像」与「记录了空镜像」是两种事实，读 API 靠它们区分 null 与 {}。
func TestActionSinkKeepsNilSummariesNil(t *testing.T) {
	ev := eventFromActionEvent(action.AuditEvent{Succeeded: false, ErrorCode: "PERMISSION_DENIED"})
	if ev.BeforeSummary != nil || ev.AfterSummary != nil {
		t.Fatalf("nil 摘要不该被脱敏变成空 map: %+v / %+v", ev.BeforeSummary, ev.AfterSummary)
	}
	if ev.Result != ResultFailed || ev.CompensationResult != "PERMISSION_DENIED" {
		t.Fatalf("失败事件的结果与错误码不对: %+v", ev)
	}
}
