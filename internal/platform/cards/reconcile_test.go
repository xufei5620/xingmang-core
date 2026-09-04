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

// 幂等信标由幂等键派生，必须确定性、不含业务信息、且短到不会被上游截断。
//
// 截断是这里唯一真正致命的失败：对账靠 alias 精确匹配，一旦上游把我们写进去
// 的值截短，返回值就与台账里存的不相等，那笔操作会永远停在不确定态——
// 而这恰恰发生在最需要对账的超时场景里。
func TestAliasForIsDeterministicAndOpaque(t *testing.T) {
	a1 := AliasFor("issue:ops@example.com:2026-09-04:1", "")
	a2 := AliasFor("issue:ops@example.com:2026-09-04:1", "")

	if a1 != a2 {
		t.Fatalf("同一幂等键必须派生出同一 alias: %q vs %q", a1, a2)
	}
	if a1 == AliasFor("issue:ops@example.com:2026-09-04:2", "") {
		t.Fatal("不同幂等键必须派生出不同 alias")
	}
	if strings.Contains(a1, "example.com") || strings.Contains(a1, "ops") {
		t.Fatalf("alias 不该含业务信息: %q", a1)
	}
	if !strings.HasPrefix(a1, "xm-") {
		t.Fatalf("没有标签时应退回平台前缀: %q", a1)
	}
}

// 带用途标签时，alias 前半段可读、后半段仍是幂等哈希。
//
// 产品负责人 2026-09-05 拍板：上游后台的「卡片名称」这一格就是这个 alias，
// 一串纯哈希在那边完全不可读（别的卡叫「Chloe J」，我们的叫 xm-e91ba7f1）。
func TestAliasForPrefixesTheLabel(t *testing.T) {
	got := AliasFor("issue-1", "测试")

	if !strings.HasPrefix(got, "测试-") {
		t.Fatalf("应以标签开头: %q", got)
	}
	if got == AliasFor("issue-2", "测试") {
		t.Fatal("标签相同但幂等键不同，alias 必须不同")
	}
	// 同一个幂等键换个标签 = 另一个 alias。这是可接受的：标签是开卡请求的
	// 一部分，同键不同标签本来就不该被当成同一次操作。
	if got == AliasFor("issue-1", "别的") {
		t.Fatal("标签参与派生，不同标签应得到不同 alias")
	}
}

// 标签再长、再脏，alias 都必须落在安全长度内且不含分隔符歧义。
func TestAliasForBoundsLabelLengthAndCharset(t *testing.T) {
	cases := []string{
		"这是一个非常非常长的用途标签会被截断掉",
		"a-very-long-ascii-label-that-keeps-going-and-going",
		"含 空格 与-连字符",
		"\n\t控制字符",
		"",
		"   ",
	}
	for _, label := range cases {
		got := AliasFor("issue-1", label)
		if len(got) > aliasMaxBytes {
			t.Fatalf("标签 %q 产出的 alias 超长 (%d 字节): %q", label, len(got), got)
		}
		for _, bad := range []rune{'\n', '\t', '\r'} {
			if strings.ContainsRune(got, bad) {
				t.Fatalf("alias 不该含控制字符: %q", got)
			}
		}
		// 无论标签是什么，末尾那段哈希必须只由幂等键决定——
		// 对账时它是唯一可信的部分。
		if !strings.HasSuffix(got, aliasHash("issue-1")) {
			t.Fatalf("标签 %q 破坏了哈希后缀: %q", label, got)
		}
	}
}
