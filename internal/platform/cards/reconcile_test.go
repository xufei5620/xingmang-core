package cards

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

var reconcileNow = time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)

func unknownOp(startedAgo time.Duration) Operation {
	return Operation{
		IdempotencyKey: "op-1",
		Kind:           OpIssue,
		State:          StateUnknown,
		Alias:          "xm-abc123",
		StartedAt:      reconcileNow.Add(-startedAgo),
	}
}

// 对账的正常出口：按 alias 精确匹配到一张卡，说明开卡其实成功了。
func TestReconcileIssueConvergesWhenAliasMatchesOneCard(t *testing.T) {
	d := ReconcileIssue(unknownOp(time.Minute), []infini.Card{{ID: "card_9", Alias: "xm-abc123"}}, reconcileNow, 30*time.Minute)

	if d.NewState != StateSucceeded {
		t.Fatalf("NewState = %q, want succeeded", d.NewState)
	}
	if d.CardID != "card_9" {
		t.Fatalf("CardID = %q, want card_9", d.CardID)
	}
	if d.NeedsHumanReview {
		t.Fatal("匹配成功不该需要人工")
	}
}

// 宽限期内查不到：上游可能还没把卡建出来，继续等，但**不允许重试**。
func TestReconcileIssueKeepsWaitingWithinGrace(t *testing.T) {
	d := ReconcileIssue(unknownOp(5*time.Minute), nil, reconcileNow, 30*time.Minute)

	if d.NewState != StateUnknown {
		t.Fatalf("NewState = %q, want unknown（还在等）", d.NewState)
	}
	if d.NeedsHumanReview {
		t.Fatal("宽限期内不该惊动人")
	}
	if d.RetryAllowed {
		t.Fatal("不确定态下绝不允许自动重试——重试可能开出第二张卡")
	}
}

// 超出宽限期仍查不到：不能再等下去，也不能自己决定「那就是没开成」——
// 亮红条要人工确认，同时锁死同参数重试。
func TestReconcileIssueEscalatesAfterGrace(t *testing.T) {
	d := ReconcileIssue(unknownOp(31*time.Minute), nil, reconcileNow, 30*time.Minute)

	if !d.NeedsHumanReview {
		t.Fatal("超出宽限期必须亮红条要人工")
	}
	if d.NewState != StateUnknown {
		t.Fatalf("NewState = %q, want unknown（人工确认前不改判）", d.NewState)
	}
	if d.RetryAllowed {
		t.Fatal("人工确认前不许重试")
	}
}

// 按 alias 查到多于一张：说明重复开卡已经发生了，这是资损，
// 必须最高优先级报人工，绝不能自动挑一张认下。
func TestReconcileIssueFlagsDuplicateCards(t *testing.T) {
	d := ReconcileIssue(unknownOp(time.Minute), []infini.Card{
		{ID: "card_9", Alias: "xm-abc123"},
		{ID: "card_10", Alias: "xm-abc123"},
	}, reconcileNow, 30*time.Minute)

	if !d.NeedsHumanReview {
		t.Fatal("重复开卡必须报人工")
	}
	if d.NewState == StateSucceeded {
		t.Fatal("重复开卡不能自动认成成功")
	}
	if !strings.Contains(d.Reason, "重复") {
		t.Fatalf("原因里要写清楚是重复开卡，got %q", d.Reason)
	}
}

// alias 不匹配的卡不算数：上游可能返回了别的卡（过滤没生效、分页串了），
// 拿它认账等于把另一张卡的 id 记到这次操作头上。
func TestReconcileIssueIgnoresCardsWithDifferentAlias(t *testing.T) {
	d := ReconcileIssue(unknownOp(31*time.Minute), []infini.Card{{ID: "card_x", Alias: "someone-else"}}, reconcileNow, 30*time.Minute)

	if d.NewState == StateSucceeded {
		t.Fatal("alias 对不上的卡不能认成本次开卡的结果")
	}
	if !d.NeedsHumanReview {
		t.Fatal("超时且无匹配，仍要报人工")
	}
}

// 只有确定失败的操作才允许重试。这是整个幂等方案的最后一道闸。
func TestRetryAllowedOnlyForDefinitelyFailed(t *testing.T) {
	cases := []struct {
		state OperationState
		want  bool
	}{
		{StateFailed, true},
		{StateUnknown, false},
		{StatePending, false},
		{StateSucceeded, false},
	}

	for _, tc := range cases {
		op := Operation{State: tc.state}
		if got := op.RetryAllowed(); got != tc.want {
			t.Fatalf("状态 %q 的 RetryAllowed = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// 幂等信标由幂等键派生，必须确定性、定长、不含业务信息。
// 含业务信息（比如持卡人邮箱）会把它送进上游的一个我们不控制的字段里。
func TestAliasForIsDeterministicAndOpaque(t *testing.T) {
	a1 := AliasFor("issue:ops@example.com:2026-09-04:1")
	a2 := AliasFor("issue:ops@example.com:2026-09-04:1")

	if a1 != a2 {
		t.Fatalf("同一幂等键必须派生出同一 alias: %q vs %q", a1, a2)
	}
	if a1 == AliasFor("issue:ops@example.com:2026-09-04:2") {
		t.Fatal("不同幂等键必须派生出不同 alias")
	}
	if strings.Contains(a1, "example.com") || strings.Contains(a1, "ops") {
		t.Fatalf("alias 不该含业务信息: %q", a1)
	}
	if len(a1) > 32 {
		t.Fatalf("alias 过长可能被上游截断，len=%d: %q", len(a1), a1)
	}
	if !strings.HasPrefix(a1, "xm-") {
		t.Fatalf("alias 应带平台前缀便于在上游后台辨认: %q", a1)
	}
}
