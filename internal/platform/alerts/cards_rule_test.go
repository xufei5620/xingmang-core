package alerts

// cards.sync.failed 的判定用例（XM-CARD-VISIBILITY）。

import (
	"fmt"
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
//
// 夹具一律是**生产形状**：当前观测就是样本序列的最后一条。
// UpsertWithSample 在同一个事务里写最新态与样本，所以生产上不存在
// 「current 与最新样本不是同一轮」的状态——用那种夹具测出来的结论
// 在生产上不成立（迟滞那条用例就是这么假绿了三天的）。
func TestCardSyncFailedNeedsNConsecutiveRounds(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultCardSyncConsecutiveRounds

	round := func(back int) ops.Observation {
		return cardSyncSample(now.Add(-time.Duration(back)*5*time.Minute), true,
			failedStep("LINFENG", "batch_status"))
	}

	var samples []ops.Observation
	for i := threshold - 1; i >= 1; i-- {
		samples = append(samples, round(i))
	}
	current := samples[len(samples)-1]

	if got := countFor(evaluate(t, cardSyncSource(current, samples...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("只有 %d 轮时不该报，实际 %d 条", threshold-1, got)
	}

	samples = append(samples, round(0))
	current = samples[len(samples)-1]

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
	threshold := DefaultCardSyncConsecutiveRounds

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
	threshold := DefaultCardSyncConsecutiveRounds

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
//
// **夹具必须是生产形状**（current == 最新样本）。这条用例的上一版把 current
// 造成「正在失败」、却在样本末尾追一条成功——那正是 rules_cards.go 的注释
// 断言不会发生的状态（两者在同一个事务里写）。于是它绿着，而生产上迟滞
// 是死代码：只有「这一轮正在失败」的键才会去数连续串，成功一轮就没人再数它，
// Reconciler 当轮把告警恢复掉。声明写着 2 轮，实际是 1 轮。
func TestCardSyncFailedHasRecoveryHysteresis(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultCardSyncConsecutiveRounds

	// 失败 N 轮（最新的那轮在 now-2 个周期）。
	var failed []ops.Observation
	for i := threshold + 1; i >= 2; i-- {
		failed = append(failed, cardSyncSample(
			now.Add(-time.Duration(i)*5*time.Minute), true,
			failedStep("LINFENG", "batch_status")))
	}

	// 失败 N 轮 + 成功 1 轮：**仍然报**。一个还在 80% 失败率上的账号
	// 很容易撞上一轮走运，1 轮就清会让告警在开与关之间抖动，
	// 抖几次之后运维会把整条规则静默掉——那才是真正把预警关掉的方式。
	oneOK := make([]ops.Observation, 0, len(failed)+2)
	oneOK = append(oneOK, failed...)
	oneOK = append(oneOK, cardSyncSample(now.Add(-5*time.Minute), false,
		okStep("LINFENG", "batch_status")))

	findings := evaluate(t, cardSyncSource(oneOK[len(oneOK)-1], oneOK...), now)
	if got := countFor(findings, RuleCardSyncFailed); got != 1 {
		t.Fatalf("只成功 1 轮不算恢复，应仍报 1 条，实际 %d 条", got)
	}
	// 正文要说清「还差几轮」，否则运维看着一条正在恢复的告警不知道该等还是该动手。
	f, _ := findingFor(findings, RuleCardSyncFailed)
	for _, want := range []string{"刚成功 1 轮", "还差 1 轮"} {
		if !strings.Contains(f.Detail, want) {
			t.Fatalf("恢复中的告警正文应含 %q，实际 %q", want, f.Detail)
		}
	}
	// 上游原话取自**最近一次失败**那一轮：这一轮成功了，它自己没有 detail。
	if !strings.Contains(f.Detail, "40004") {
		t.Fatalf("恢复中的告警仍要带上最近一次失败时的上游答复，实际 %q", f.Detail)
	}

	// 失败 N 轮 + 成功 2 轮：清掉。
	twoOK := make([]ops.Observation, 0, len(oneOK)+1)
	twoOK = append(twoOK, oneOK...)
	twoOK = append(twoOK, cardSyncSample(now, false, okStep("LINFENG", "batch_status")))
	if got := countFor(evaluate(t, cardSyncSource(twoOK[len(twoOK)-1], twoOK...), now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("连续成功 %d 轮应算恢复，实际 %d 条", cardSyncRecoverySamples, got)
	}
}

// 被暂停的账号一条都不报。
//
// 否则运营刚把一个坏账号停掉，紧接着收到一条永远清不掉的告警，
// 结果一定是把整条规则静默——那会连带丢掉其余账号的信号。
func TestCardSyncFailedSuppressesPausedAccounts(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultCardSyncConsecutiveRounds

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

// 一轮评估**只回看一次**历史样本，不管有几个 (账号, 步骤) 在失败。
//
// 这条替掉了原来那条「健康时一次都不回看」。原来那条照抄 R4 的成本纪律，
// 但那个省法只在迟滞是死代码时才成立：迟滞要判的正是「这一轮成功了但还没
// 算恢复」，而这一轮全绿恰恰是**必须**回看的时刻。留着它就等于用一条
// 恒绿的成本断言把迟滞永远钉死在关闭状态。
//
// 真正值得钉的成本性质是「不随失败数放大」：按键去查会让一个坏掉的账号
// 在评估时打出十几次查询。
func TestCardSyncFailedReadsSamplesOncePerRound(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultCardSyncConsecutiveRounds

	// 观测状态记 ok：三步失败但整轮不算失败，正是本片写观测的生产形状
	// （writeObservation 只在整轮全砸时记 failed）。顺带让 R4 不去回看，
	// 于是 sampleCalls 数的就只有这条规则自己。
	round := func(back int) ops.Observation {
		return cardSyncSample(now.Add(-time.Duration(back)*5*time.Minute), false,
			failedStep("LINFENG", "batch_status"),
			failedStep("LINFENG", "fetch_secrets"),
			failedStep("MAIN", "batch_status"))
	}
	var samples []ops.Observation
	for i := threshold - 1; i >= 0; i-- {
		samples = append(samples, round(i))
	}
	src := cardSyncSource(samples[len(samples)-1], samples...)

	if got := countFor(evaluate(t, src, now), RuleCardSyncFailed); got != 3 {
		t.Fatalf("三个 (账号, 步骤) 各一条，实际 %d 条", got)
	}
	if src.sampleCalls != 1 {
		t.Fatalf("一轮评估只该回看一次历史, sampleCalls=%d", src.sampleCalls)
	}
}

// 窗口里从没坏过时不报，且不因为「回看了历史」就凭空造出命中。
func TestCardSyncFailedStaysQuietWhenHealthy(t *testing.T) {
	now := time.Now().UTC()
	var samples []ops.Observation
	for i := 5; i >= 0; i-- {
		samples = append(samples, cardSyncSample(
			now.Add(-time.Duration(i)*5*time.Minute), false,
			okStep("LINFENG", "batch_status")))
	}
	src := cardSyncSource(samples[len(samples)-1], samples...)

	if got := countFor(evaluate(t, src, now), RuleCardSyncFailed); got != 0 {
		t.Fatalf("一直健康不该报，实际 %d 条", got)
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

// TestCardSyncConsecutiveRoundsNormalization：轮数配成 0 或负数时回落到默认值。
//
// 这条不是补齐仪式。生产用字面量构造 RuleConfig 且**不填这个字段**
// （jobs.NewClient 只填三项），所以这条回落是它在生产上取到 3 的唯一途径。
// 少了它，卡片规则的阈值在生产上就是 0——streak.open 里 failures >= 0 恒真，
// 这条告警会对窗口里出现过的每个 (账号, 步骤) 当场开一条。
func TestCardSyncConsecutiveRoundsNormalization(t *testing.T) {
	for _, bad := range []int{0, -1} {
		got := RuleConfig{CardSyncConsecutiveRounds: bad}.normalized().CardSyncConsecutiveRounds
		if got != DefaultCardSyncConsecutiveRounds {
			t.Fatalf("轮数 %d 应当回落到默认值 %d，got %d",
				bad, DefaultCardSyncConsecutiveRounds, got)
		}
	}
	// 合法值原样保留——否则这条归一化就成了「永远用默认值」，
	// 那个旋钮就白加了。
	if got := (RuleConfig{CardSyncConsecutiveRounds: 7}).normalized().CardSyncConsecutiveRounds; got != 7 {
		t.Fatalf("合法轮数被改掉了：%d", got)
	}
}

// TestCardSyncThresholdIsItsOwnKnob：卡片阈值必须独立于 R1 的迟滞轮数。
//
// 这条钉的是集成合并 2026-09-09 的取舍。两个数默认都是 3，所以「共用一个
// 字段」与「各有一个字段」在默认配置下**表现完全相同**——只有把 R1 的 N
// 调开才分得出来。不钉住的话，将来有人图省事把 CardSyncConsecutiveRounds
// 删掉改回 SyncFailedHysteresisRounds，全仓门禁照样全绿。
func TestCardSyncThresholdIsItsOwnKnob(t *testing.T) {
	cfg := DefaultRuleConfig()
	cfg.SyncFailedHysteresisRounds = DefaultCardSyncConsecutiveRounds + 4
	cfg.CollectionInterval = time.Minute

	var found bool
	for _, r := range Rules(cfg) {
		if r.Key != RuleCardSyncFailed {
			continue
		}
		found = true
		// 文案与 For 都必须还说卡片自己的轮数，不能跟着 R1 走。
		want := fmt.Sprintf("连续 %d 轮失败", DefaultCardSyncConsecutiveRounds)
		if !strings.Contains(r.Condition, want) {
			t.Fatalf("调 R1 的迟滞轮数不该改动卡片规则的条件文案：%q（应含 %q）",
				r.Condition, want)
		}
		if got, wantFor := r.For, time.Duration(DefaultCardSyncConsecutiveRounds)*cfg.CollectionInterval; got != wantFor {
			t.Fatalf("卡片规则的 For 跟着 R1 漂了：got %s, want %s", got, wantFor)
		}
	}
	if !found {
		t.Fatal("Rules() 里没有 cards.sync.failed，这个测试就失去意义了")
	}
}
