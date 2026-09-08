package alerts

// cards.sync.failed 的判定用例（XM-CARD-VISIBILITY）。

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// cardSyncSample 造一条卡片同步观测样本。
//
// 形状与 cards.RoundResult.Summary() 逐字对齐；两边漂开的话
// TestCardSyncMetricKeyMatchesJob 抓不到，抓它的是本文件末尾那条
// 「解析器认得真实形状」的用例。
func cardSyncSample(syncedAt time.Time, failed bool, steps ...map[string]any) ops.Observation {
	o := ops.Observation{
		MetricKey: DefaultCardSyncMetricKey, Source: "card_sync", Environment: testEnv,
		SyncedAt: syncedAt, Status: ops.SyncOK,
		StalenessThresholdSeconds: 600,
	}
	if failed {
		o.Status = ops.SyncFailed
		o.LastErrorCode = "rejected"
	}
	byAccount := map[string][]any{}
	var order []string
	for _, s := range steps {
		account, _ := s["account"].(string)
		if _, ok := byAccount[account]; !ok {
			order = append(order, account)
		}
		entry := map[string]any{"step": s["step"], "ok": s["ok"]}
		if v, ok := s["skipped"]; ok {
			entry["skipped"] = v
		}
		if v, ok := s["kind"]; ok {
			entry["kind"] = v
		}
		if v, ok := s["detail"]; ok {
			entry["detail"] = v
		}
		byAccount[account] = append(byAccount[account], entry)
	}
	accounts := make([]any, 0, len(order))
	for _, id := range order {
		accounts = append(accounts, map[string]any{"account": id, "steps": byAccount[id]})
	}
	o.Value = map[string]any{"account_count": len(order), "accounts": accounts}
	return o
}

func failedStep(account, step string) map[string]any {
	return map[string]any{
		"account": account, "step": step, "ok": false,
		"kind":   "rejected",
		"detail": "账号 " + account + " 批量查状态: rejected: infini POST /v2/cards/status/batch (upstream code 40004: card_id not in batch scope)",
	}
}

func okStep(account, step string) map[string]any {
	return map[string]any{"account": account, "step": step, "ok": true}
}

func pausedStep(account, step string) map[string]any {
	return map[string]any{"account": account, "step": step, "ok": true, "skipped": true}
}

// cardSyncSource 造一个只装卡片同步观测与样本的来源。
func cardSyncSource(current ops.Observation, samples ...ops.Observation) *fakeMetricSource {
	return &fakeMetricSource{
		observations: []ops.Observation{current},
		samples:      map[string][]ops.Observation{DefaultCardSyncMetricKey: samples},
	}
}

// 连续 N 轮才开：N-1 轮不报，第 N 轮恰好一条。
func TestCardSyncFailedNeedsNConsecutiveRounds(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultConsecutiveFailureThreshold

	var samples []ops.Observation
	for i := 0; i < threshold-1; i++ {
		samples = append(samples, cardSyncSample(
			now.Add(-time.Duration(threshold-i)*5*time.Minute), true,
			failedStep("LINFENG", "batch_status")))
	}
	current := cardSyncSample(now, true, failedStep("LINFENG", "batch_status"))

	if got := countFor(evaluate(t, cardSyncSource(current, samples...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("只有 %d 轮时不该报，实际 %d 条", threshold-1, got)
	}

	samples = append(samples, cardSyncSample(now.Add(-5*time.Minute), true,
		failedStep("LINFENG", "batch_status")))
	samples = append(samples, current)

	findings := evaluate(t, cardSyncSource(current, samples...), now)
	if got := countFor(findings, RuleCardSyncFailed); got != 1 {
		t.Fatalf("满 %d 轮应恰好报 1 条，实际 %d 条: %+v", threshold, got, findings)
	}
	f, _ := findingFor(findings, RuleCardSyncFailed)
	if f.Severity != SeverityCritical {
		t.Fatalf("严重度 = %q, want critical", f.Severity)
	}
	if !strings.Contains(f.DedupKey, "LINFENG/batch_status") {
		t.Fatalf("去重键要带账号与步骤，实际 %q", f.DedupKey)
	}
	// **本片的收口**：把连接器取出来的上游原话真的送到人眼前。
	for _, want := range []string{"LINFENG", "批量查卡状态", "40004", "card_id not in batch scope"} {
		if !strings.Contains(f.Detail, want) {
			t.Fatalf("告警正文应含 %q，实际 %q", want, f.Detail)
		}
	}
}

// 两个账号同时坏 → 两条不同的 finding，不塌成一条。
func TestCardSyncFailedSplitsByAccountAndStep(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultConsecutiveFailureThreshold

	// 同一个账号的两种失败也必须分开：只带账号的去重键会让它们塌成一条，
	// 于是静默批量查状态的同时把拉明文的失败也一起静默了。
	round := func(at time.Time) ops.Observation {
		return cardSyncSample(at, true,
			failedStep("LINFENG", "batch_status"),
			failedStep("LINFENG", "fetch_secrets"))
	}
	var samples []ops.Observation
	for i := 0; i < threshold; i++ {
		samples = append(samples, round(now.Add(-time.Duration(threshold-1-i)*5*time.Minute)))
	}
	current := samples[len(samples)-1]

	findings := evaluate(t, cardSyncSource(current, samples...), now)
	if got := countFor(findings, RuleCardSyncFailed); got != 2 {
		t.Fatalf("两个账号各自成一条，实际 %d 条: %+v", got, findings)
	}
	keys := map[string]bool{}
	for _, f := range findings {
		if f.RuleKey == RuleCardSyncFailed {
			keys[f.DedupKey] = true
		}
	}
	if len(keys) != 2 {
		t.Fatalf("去重键必须不同，实际 %v", keys)
	}
}

// 步骤交替失败不算连续：判据是「同一账号同一步骤」，不是「失败了 N 次」。
func TestCardSyncFailedRequiresSameStep(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultConsecutiveFailureThreshold

	steps := []string{"batch_status", "fetch_secrets", "batch_status", "fetch_secrets"}
	var samples []ops.Observation
	for i := 0; i < threshold; i++ {
		samples = append(samples, cardSyncSample(
			now.Add(-time.Duration(threshold-1-i)*5*time.Minute), true,
			failedStep("LINFENG", steps[i%len(steps)])))
	}
	current := samples[len(samples)-1]

	if got := countFor(evaluate(t, cardSyncSource(current, samples...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("交替失败凑不成连续串，实际 %d 条", got)
	}
}

// 迟滞：一轮走运的成功不清告警，连续两轮成功才清。
func TestCardSyncFailedHasRecoveryHysteresis(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultConsecutiveFailureThreshold

	base := func(n int) []ops.Observation {
		var out []ops.Observation
		for i := 0; i < n; i++ {
			out = append(out, cardSyncSample(
				now.Add(-time.Duration(n+2-i)*5*time.Minute), true,
				failedStep("LINFENG", "batch_status")))
		}
		return out
	}

	// 失败 N 轮 + 成功 1 轮：**仍然报**。一个还在 80% 失败率上的账号
	// 很容易撞上一轮走运，1 轮就清会让告警在开与关之间抖动。
	current := cardSyncSample(now, true, failedStep("LINFENG", "batch_status"))
	oneOK := append(base(threshold), cardSyncSample(now.Add(-5*time.Minute), false,
		okStep("LINFENG", "batch_status")))
	if got := countFor(evaluate(t, cardSyncSource(current, oneOK...), now), RuleCardSyncFailed); got != 1 {
		t.Fatalf("只成功 1 轮不算恢复，应仍报 1 条，实际 %d 条", got)
	}

	// 失败 N 轮 + 成功 2 轮：清掉。
	twoOK := append(oneOK, cardSyncSample(now, false, okStep("LINFENG", "batch_status")))
	if got := countFor(evaluate(t, cardSyncSource(current, twoOK...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("连续成功 %d 轮应算恢复，实际 %d 条", cardSyncRecoverySamples, got)
	}
}

// 被暂停的账号一条都不报。
//
// 否则运营刚把一个坏账号停掉，紧接着收到一条永远清不掉的告警，
// 结果一定是把整条规则静默——那会连带丢掉其余账号的信号。
func TestCardSyncFailedSuppressesPausedAccounts(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultConsecutiveFailureThreshold

	var samples []ops.Observation
	for i := 0; i < threshold; i++ {
		samples = append(samples, cardSyncSample(
			now.Add(-time.Duration(threshold-i)*5*time.Minute), true,
			failedStep("LINFENG", "batch_status")))
	}
	// 最新一轮：账号已被暂停。刻意让这一轮**同时**有一条跳过与一条失败——
	// 判据是「这个**账号**被暂停了」而不是「这一条步骤被跳过了」。
	// 只按步骤判的话，将来任何一个忘了检查暂停开关的新步骤都会把一个
	// 明明已经停掉的账号重新叫醒；而运营被一条清不掉的告警骚扰之后，
	// 一定会把整条规则静默——那会连带丢掉其余账号的信号。
	current := cardSyncSample(now, false,
		pausedStep("LINFENG", "discover"),
		failedStep("LINFENG", "batch_status"))
	samples = append(samples, current)

	if got := countFor(evaluate(t, cardSyncSource(current, samples...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("暂停的账号不该报，实际 %d 条", got)
	}
}

// 当前这一轮没有失败时**完全不回看历史样本**：成本纪律，照抄 R4。
func TestCardSyncFailedDoesNotReadSamplesWhenHealthy(t *testing.T) {
	now := time.Now().UTC()
	current := cardSyncSample(now, false, okStep("LINFENG", "batch_status"))
	src := cardSyncSource(current)

	if got := countFor(evaluate(t, src, now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("这一轮成功了不该报，实际 %d 条", got)
	}
	if src.sampleCalls != 0 {
		t.Fatalf("健康时不该回看历史样本, sampleCalls=%d", src.sampleCalls)
	}
}

// 规则键必须是**已注册**的，否则运营建静默窗口时选不到它，
// 等于永远静默零条。
func TestCardSyncRuleKeyIsSilenceable(t *testing.T) {
	if !KnownRuleKey(RuleCardSyncFailed) {
		t.Fatalf("%q 不在 RuleKeys() 里：静默窗口选不到它", RuleCardSyncFailed)
	}
}

// 解析器必须认得 cards.RoundResult.Summary() 的真实形状。
//
// 这条是防「两边形状漂开」的：解析器与写它的那一侧各写一份键名，
// 漂开时不会报错，只会让规则安静地一条都数不到。真正的端到端锚点在
// jobs 的 TestCardSyncWorkerWritesObservationWithPerAccountDetail
// （它写的是真的 Summary()），这里守的是解析这一半。
func TestCardSyncStepParsingHandlesRealShape(t *testing.T) {
	value := map[string]any{
		"account_count": 1,
		"accounts": []any{
			map[string]any{
				"account": "LINFENG",
				"steps": []any{
					map[string]any{"step": "batch_status", "ok": false, "skipped": false,
						"kind": "rejected", "detail": "upstream code 40004"},
					map[string]any{"step": "batch_status_fallback", "ok": true,
						"skipped": false, "recovered": float64(12)},
				},
			},
		},
	}
	steps := cardSyncSteps(value)
	got, ok := steps[cardSyncStepKey{account: "LINFENG", step: "batch_status"}]
	if !ok {
		t.Fatalf("没解出 batch_status: %+v", steps)
	}
	if got.ok || got.kind != "rejected" || !strings.Contains(got.detail, "40004") {
		t.Fatalf("解析结果不对: %+v", got)
	}
	if len(cardSyncPausedAccounts(value)) != 0 {
		t.Fatal("没有 skipped 的步骤时不该判成暂停")
	}
}
