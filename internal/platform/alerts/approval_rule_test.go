package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// approvalObservation 造一条审批队列观测。
//
// value 用 map[string]any 而不是先 Marshal 再 Unmarshal：**这是刻意的**，
// 因为生产里这条观测经过一次 JSON 往返（写库再读回），整数会变成 float64。
// 下面单独有一条用例走真实的 JSON 往返，钉住 numericField 认得那种形态。
func approvalObservation(now time.Time, pending int64, ageSeconds int64) ops.Observation {
	o := freshObservation(DefaultApprovalQueueMetricKey, now)
	o.Value = map[string]any{
		"pending_count":              pending,
		"oldest_pending_age_seconds": ageSeconds,
		"expired_this_round":         int64(0),
	}
	return o
}

func TestApprovalPendingTooLongFiresAtThreshold(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	threshold := int64(DefaultApprovalPendingThreshold.Seconds())

	t.Run("超过门槛就响", func(t *testing.T) {
		src := &fakeMetricSource{observations: []ops.Observation{
			approvalObservation(now, 2, threshold+60),
		}}
		f, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong)
		if !ok {
			t.Fatal("应当命中")
		}
		if f.Severity != SeverityWarning {
			t.Fatalf("严重度应当是 warning，got %s", f.Severity)
		}
		// 去重键只到队列，不到单号：一条「有人在等」就够了。
		if f.DedupKey != RuleApprovalPendingTooLong+":"+testEnv+":queue" {
			t.Fatalf("去重键不对：%q", f.DedupKey)
		}
		// 文案里要说清「几张」和「多久」，还要指向 Runbook——收到告警的人
		// 需要知道下一步做什么。
		if !strings.Contains(f.Detail, "2 张") || !strings.Contains(f.Detail, "APPROVAL-QUEUE.md") {
			t.Fatalf("文案缺少可处置信息：%s", f.Detail)
		}
	})

	t.Run("恰好等于门槛也响", func(t *testing.T) {
		src := &fakeMetricSource{observations: []ops.Observation{
			approvalObservation(now, 1, threshold),
		}}
		if _, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong); !ok {
			t.Fatal("判据是 >=，恰好到点应当响")
		}
	})

	t.Run("差一秒不响", func(t *testing.T) {
		src := &fakeMetricSource{observations: []ops.Observation{
			approvalObservation(now, 1, threshold-1),
		}}
		if _, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong); ok {
			t.Fatal("没到门槛不该响")
		}
	})
}

// TestApprovalPendingSkipsEmptyQueue：队列为空时不响。
//
// 这不是「顺便」：pending_count 为 0 时 age 必然是 0，而如果有人把门槛配成
// 0（或负数），没有这道判断就会对着一个空队列天天报警。
func TestApprovalPendingSkipsEmptyQueue(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{observations: []ops.Observation{approvalObservation(now, 0, 0)}}
	if _, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong); ok {
		t.Fatal("空队列不该响")
	}
	// 对照：队列非空且超时就响——确认拦住的是「空」。
	src2 := &fakeMetricSource{observations: []ops.Observation{
		approvalObservation(now, 1, int64(DefaultApprovalPendingThreshold.Seconds())+1),
	}}
	if _, ok := findingFor(evaluate(t, src2, now), RuleApprovalPendingTooLong); !ok {
		t.Fatal("非空且超时应当响")
	}
}

// TestApprovalPendingIgnoresMissingFields：观测残缺时不响。
//
// 判据只有一道（pending_count > 0）：缺字段解出 0，与空队列走同一条出口。
// **写这条测试时我先多加了一道 `oldest_pending_age_seconds` 缺失判断，
// 变异验证把它照出来是死代码**——缺字段解出 0，而 0 低于任何合法门槛，
// 那道判断永远不改变结果。删掉之后这里的每一个子用例仍然全绿，因为它们
// 断言的是**行为**（不响），而不是某一行代码存在。
func TestApprovalPendingIgnoresMissingFields(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	for name, value := range map[string]map[string]any{
		"两个字段都没有":    {},
		"只有 pending": {"pending_count": int64(3)},
		"只有 age":     {"oldest_pending_age_seconds": int64(99999)},
		"age 是 nil":  {"pending_count": int64(3), "oldest_pending_age_seconds": nil},
		"age 不是数字":   {"pending_count": int64(3), "oldest_pending_age_seconds": []any{1}},
	} {
		t.Run(name, func(t *testing.T) {
			o := freshObservation(DefaultApprovalQueueMetricKey, now)
			o.Value = value
			src := &fakeMetricSource{observations: []ops.Observation{o}}
			if _, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong); ok {
				t.Fatal("字段不全时不该响")
			}
		})
	}
}

// TestApprovalPendingSurvivesJSONRoundTrip：经过库里那一趟 JSON 往返之后
// 仍然认得出数字。
//
// 生产里这条观测是写进 value_json 再读回来的，整数会变成 float64。上面那些
// 用例直接塞 int64，**测不到这一点**——这一条专门走真实形态。
func TestApprovalPendingSurvivesJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(map[string]any{
		"pending_count":              2,
		"oldest_pending_age_seconds": int64(DefaultApprovalPendingThreshold.Seconds()) + 600,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// 先确认往返之后真的不是 int64 了，否则这条用例测的还是上面那种形态。
	if _, isInt := decoded["pending_count"].(int64); isInt {
		t.Fatal("往返之后仍是 int64——这条用例没有测到真实形态")
	}
	o := freshObservation(DefaultApprovalQueueMetricKey, now)
	o.Value = decoded
	src := &fakeMetricSource{observations: []ops.Observation{o}}
	if _, ok := findingFor(evaluate(t, src, now), RuleApprovalPendingTooLong); !ok {
		t.Fatal("JSON 往返之后应当照常命中")
	}
}

// TestApprovalPendingSkipsFailedObservation：观测失败时不判。
//
// 失败观测里的 value_json 是上一轮的数字，拿它算「等了多久」会报出一个早已
// 不成立的事实。这一类由既有的「指标同步失败」规则负责。
func TestApprovalPendingSkipsFailedObservation(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	o := approvalObservation(now, 5, int64(DefaultApprovalPendingThreshold.Seconds())+3600)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "approval_queue_stats_failed"
	src := failedEnough(&fakeMetricSource{observations: []ops.Observation{o}},
		DefaultApprovalQueueMetricKey, now)
	findings := evaluate(t, src, now)

	if _, ok := findingFor(findings, RuleApprovalPendingTooLong); ok {
		t.Fatal("失败观测不该拿来判审批积压")
	}
	// 但**必须有人报这件事**：同步失败那条要响，否则一次静默的观测故障会让
	// 审批告警整个哑掉而没人知道。
	if _, ok := findingFor(findings, RuleMetricSyncFailed); !ok {
		t.Fatal("失败观测应当由「同步失败」规则报出来")
	}
}

// TestApprovalPendingThresholdNormalization：门槛配成 0 或负数时回落到默认值。
// 零值等于「立刻告警」——每一张刚提交的单都会当场响一次。
func TestApprovalPendingThresholdNormalization(t *testing.T) {
	for _, bad := range []time.Duration{0, -time.Hour} {
		got := RuleConfig{ApprovalPendingThreshold: bad}.normalized().ApprovalPendingThreshold
		if got != DefaultApprovalPendingThreshold {
			t.Fatalf("门槛 %s 应当回落到默认值，got %s", bad, got)
		}
	}
	// 合法值原样保留——否则这条归一化就成了「永远用默认值」。
	if got := (RuleConfig{ApprovalPendingThreshold: 30 * time.Minute}).normalized().ApprovalPendingThreshold; got != 30*time.Minute {
		t.Fatalf("合法门槛被改掉了：%s", got)
	}
}

// TestApprovalRuleIsDeclared：规则声明本身要在 Rules() 里，且九项俱全的检查
// 由 TestRulesDeclareAllNineSpecFields 覆盖；这里只钉「门槛进了 Condition 文案」——
// 规则清单是给人看的，一条不说门槛是多少的规则等于没说清楚。
func TestApprovalRuleIsDeclared(t *testing.T) {
	cfg := DefaultRuleConfig()
	cfg.ApprovalPendingThreshold = 90 * time.Minute
	for _, r := range Rules(cfg) {
		if r.Key != RuleApprovalPendingTooLong {
			continue
		}
		if !strings.Contains(r.Condition, "1h30m0s") {
			t.Fatalf("Condition 里应当写明生效的门槛：%q", r.Condition)
		}
		if !strings.Contains(r.Source, DefaultApprovalQueueMetricKey) {
			t.Fatalf("Source 里应当写明数据来源：%q", r.Source)
		}
		return
	}
	t.Fatal("Rules() 里没有审批规则")
}
