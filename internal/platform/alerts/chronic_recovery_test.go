package alerts

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// R4（metric.sync.consecutive_failed）的**关**那一半。
//
// 审稿抓到的是一个「声明与判定不符」的洞：规则声明写着「窗口内失败次数回落到
// K 次以下（一次成功不再清零计数）」，README 与运营看的 notify/CATALOG.md 各
// 抄了一份；而判定里 R4 只在 f.State == StateFailed 时产出 Finding，
// Reconciler 又对本轮没再命中的活跃告警一律 Resolve——**任何一轮采集成功都会
// 把 R4 关掉**，窗口里还有 9 条失败也照关。
//
// 两个后果同时成立：
//   - 运营读到的恢复条件与系统实际行为不符（2026-09-08 报告 §四点名的那类病，
//     只不过这次是新写的副本一出生就是错的）；
//   - FFFSFFFSFFFS 这种上游劣化形态下，R4 每隔几轮 open→resolved→reopened
//     一次，每次都是新行、重新投递、critical——R1 的迟滞刚压住的抖动，换个
//     rule_key 从 R4 原样冒出来。
//
// 旧的 TestChronicFailureRuleDeclarationMatchesTheJudgement 抓不到它：那条只
// 断言 Recovery 里**没有**旧短语（缺席型断言），从不驱动一轮真正的恢复。
//
// 本文件里一律不写 W / K 的字面量，它们从 DefaultRuleConfig() 取。

func chronicWindow() (window, threshold int) {
	cfg := DefaultRuleConfig()
	return cfg.ChronicWindowSamples, cfg.ChronicFailureThreshold
}

func chronicKeyFor(metricKey string) string {
	return dedupKey(RuleSyncConsecutiveFailed, testEnv, metricKey)
}

// chronicPatterns 造三段共用的样本序列，长度恒为窗口长度 W。
//
//	failingNow：窗口内恰好 K 次失败，最新一轮**正在失败** → 该开。
//	recovered ：窗口内仍有 K 次失败，最新一轮**已经成功** → 该继续挂着。
//	cleared   ：窗口内只剩 K-1 次失败 → 该关。
func chronicPatterns() (failingNow, recovered, cleared string) {
	w, k := chronicWindow()
	failingNow = strings.Repeat("F", k-1) + strings.Repeat("S", w-k) + "F"
	recovered = strings.Repeat("F", k) + strings.Repeat("S", w-k)
	cleared = strings.Repeat("F", k-1) + strings.Repeat("S", w-k+1)
	return
}

// TestChronicFailureStaysOpenUntilTheWindowClears：声明里的恢复条件必须是真的。
//
// 旧实现下第 2 段是红的：最新一轮采集成功，R4 的 Finding 直接消失，
// 于是 Reconciler 下一步就把它 Resolve 了。
func TestChronicFailureStaysOpenUntilTheWindowClears(t *testing.T) {
	w, k := chronicWindow()
	now := time.Now().UTC()
	failingNow, recovered, cleared := chronicPatterns()

	// 用例自身的前提：三段序列长度都等于窗口，失败次数分别是 k / k / k-1。
	for name, tc := range map[string]struct {
		pattern string
		wantF   int
	}{
		"failingNow": {failingNow, k},
		"recovered":  {recovered, k},
		"cleared":    {cleared, k - 1},
	} {
		if len(tc.pattern) != w {
			t.Fatalf("用例自身构造错了：%s 长度 %d, want %d", name, len(tc.pattern), w)
		}
		if got := strings.Count(tc.pattern, "F"); got != tc.wantF {
			t.Fatalf("用例自身构造错了：%s 失败 %d 次, want %d（%s）", name, got, tc.wantF, tc.pattern)
		}
	}

	active := map[string]struct{}{chronicKeyFor(revenueMetric): {}}

	// --- 1）开：当前正在失败，窗口内 K 次失败 ---
	res := evaluateWith(t, runSource(revenueMetric, now, failingNow), &fakeAckSource{}, now, nil)
	f, ok := findingFor(res.Findings, RuleSyncConsecutiveFailed)
	if !ok {
		t.Fatalf("窗口内 %d 次失败且当前正在失败，应命中 R4：%s", k, failingNow)
	}
	if res.ChronicHeld != 0 {
		t.Fatalf("当前正在失败不算「被保持」：%+v", res)
	}
	if !strings.Contains(f.Detail, "最新错误码") {
		t.Fatalf("当前失败时详情要给出错误码：%s", f.Detail)
	}

	// --- 2）继续挂着：最新一轮已经成功，但窗口内仍有 K 次失败 ---
	src := runSource(revenueMetric, now, recovered)
	res = evaluateWith(t, src, &fakeAckSource{}, now, active)
	f, ok = findingFor(res.Findings, RuleSyncConsecutiveFailed)
	if !ok {
		t.Fatalf("窗口内仍有 %d 次失败，R4 不该因为最新一轮成功就关掉：%s", k, recovered)
	}
	if res.ChronicHeld != 1 {
		t.Fatalf("被保持的 R4 必须计数可见（否则它是一个不留痕的决定）：%+v", res)
	}
	// 详情必须说清「为什么最新一轮成功了它还挂着」，否则只能靠读代码回答。
	if !strings.Contains(f.Detail, "最新一轮采集已经成功") {
		t.Fatalf("详情要说清此刻在恢复的那一半：%s", f.Detail)
	}
	if !strings.Contains(f.Detail, fmt.Sprintf("回落到 %d 次以下", k)) {
		t.Fatalf("详情要写出还差什么才关：%s", f.Detail)
	}
	// 最新一轮已经成功时不该再报错误码——那说的是上一次失败的事，
	// 放在「已恢复」旁边会让人以为现在还在报这个错。
	if strings.Contains(f.Detail, "最新错误码") {
		t.Fatalf("已恢复的那一轮不该带错误码：%s", f.Detail)
	}
	// 这一轮 R1 已经关了（连续 N 轮恢复），所以它不是 R1 顺带把 R4 带出来的。
	//
	// 前提是 recovered 序列尾部的成功数（W-K）够 R1 的迟滞轮数 N。今天
	// W-K=3、N=3 恰好成立；哪天常量调得不满足了，这条断言就失去意义，
	// 与其让它变成一条恒假的红，不如在这里显式跳过——**并说出为什么**。
	if w-k >= DefaultRuleConfig().SyncFailedHysteresisRounds {
		if countFor(res.Findings, RuleMetricSyncFailed) != 0 {
			t.Fatalf("这一段应只剩 R4：%+v", res.Findings)
		}
	}
	// 成本纪律：R1 与 R4 共用同一次历史样本查询。
	if src.sampleCalls != 1 {
		t.Fatalf("一轮一条指标只该查一次历史样本，实际 %d 次", src.sampleCalls)
	}

	// --- 3）关：窗口内失败次数回落到 K 以下 ---
	res = evaluateWith(t, runSource(revenueMetric, now, cleared), &fakeAckSource{}, now, active)
	if countFor(res.Findings, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("窗口内只剩 %d 次失败，R4 应关闭：%+v", k-1, res.Findings)
	}
	if res.ChronicHeld != 0 {
		t.Fatalf("已关闭就不该再算作被保持：%+v", res)
	}
}

// TestChronicFailureDoesNotOpenWhileHealthy：**开**那一半的判据没有被放松。
//
// 关那一半改成「只看窗口计数」之后，最容易顺手犯的错是把开也改成只看窗口——
// 那样一条已经恢复、但窗口里还留着旧失败的链路会被**重新开**一条 critical，
// 而且每次窗口翻页都可能再来一次。
func TestChronicFailureDoesNotOpenWhileHealthy(t *testing.T) {
	now := time.Now().UTC()
	_, recovered, _ := chronicPatterns()

	// 1）同一段序列（窗口内仍有 K 次失败、最新一轮已成功），**没有任何活跃告警**。
	// 这一支连历史样本都不会查，所以它只证明外层那道取数闸还在。
	src := runSource(revenueMetric, now, recovered)
	res := evaluateWith(t, src, &fakeAckSource{}, now, nil)
	if countFor(res.Findings, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("当前不在失败、也没有活跃告警时不该新开 R4：%+v", res.Findings)
	}
	if res.ChronicHeld != 0 {
		t.Fatalf("没开过就谈不上被保持：%+v", res)
	}
	if src.sampleCalls != 0 {
		t.Fatalf("健康且没有任何活跃告警时不该查历史样本，实际 %d 次", src.sampleCalls)
	}

	// 2）**活跃的是 R1，不是 R4**：历史样本这一轮确实被取了（R1 要判自己该不该
	// 关），窗口里也确实有 K 次失败——但 R4 此前从没开过，就不该被这一轮开出来。
	//
	// 这一支才是「开还要求当前正在失败」的真正判据。少了它，把 R4 的门槛放宽成
	// 「只要取到样本就按窗口计数判」的写法照样能过上一支——因为上一支根本没
	// 进到那段代码里。
	src = runSource(revenueMetric, now, recovered)
	res = evaluateWith(t, src, &fakeAckSource{}, now,
		map[string]struct{}{syncFailedKeyFor(revenueMetric): {}})
	if src.sampleCalls != 1 {
		t.Fatalf("R1 还开着时必须回看历史（否则下面的断言是恒真的），实际 %d 次", src.sampleCalls)
	}
	if countFor(res.Findings, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("R4 没开过、当前也不在失败，不该被 R1 的那次取数顺带开出来：%+v", res.Findings)
	}
}

// TestReconcilerKeepsChronicAlertAcrossASuccessfulRound 是这条修复唯一真正
// 算数的测试：active 集合来自 store.ListActive，不是测试自己造的。
//
// 在 Evaluator 层，active 是测试自己伪造的入参，所以「继续挂着」在那一层可能
// 恒真（memory：中间层伪造被校验的入参会让判定恒真）。这里从编排层打进来：
// 删掉 Evaluate 里那个 chronicActive 分支，本测试当场红。
func TestReconcilerKeepsChronicAlertAcrossASuccessfulRound(t *testing.T) {
	w, k := chronicWindow()
	now := time.Now().UTC()
	_, recovered, cleared := chronicPatterns()

	existing := []Alert{{
		ID:       uuid.New(),
		RuleKey:  RuleSyncConsecutiveFailed,
		DedupKey: chronicKeyFor(revenueMetric),
		Status:   StatusOpen,
	}}

	store := newRecordingStore()
	store.active = existing
	store.existing[existing[0].DedupKey] = true

	res, err := newTestReconciler(store, runSource(revenueMetric, now, recovered), nil, now).
		Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(store.resolved) != 0 {
		t.Fatalf("窗口内还有 %d/%d 次失败，不该被解决：%+v", k, w, store.resolved)
	}
	if res.ChronicHeld != 1 {
		t.Fatalf("应有一条 R4 被窗口计数保持：%+v", res)
	}
	if res.Merged != 1 {
		t.Fatalf("仍然命中的告警应合并进已有那条：%+v", res)
	}

	// 窗口清空之后，同一份编排下它就该被解决——这一半证明上一半不是恒真。
	store2 := newRecordingStore()
	store2.active = existing
	res2, err := newTestReconciler(store2, runSource(revenueMetric, now, cleared), nil, now).
		Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(store2.resolved) != 1 {
		t.Fatalf("窗口内失败回落到 %d 次以下应被解决：%+v", k, store2.resolved)
	}
	if res2.ChronicHeld != 0 {
		t.Fatalf("已解决就不该再算作被保持：%+v", res2)
	}
}
