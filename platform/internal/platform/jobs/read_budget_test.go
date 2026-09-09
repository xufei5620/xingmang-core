package jobs

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 本文件钉住 XM-OPS-TRUTH 子片 A 第 2 条：
//   - 读取预算按链长推导，不是一个写死的 20s；
//   - 每一组读取有自己的 deadline，一组慢吃不到别组的份；
//   - River 的 JobTimeout 跟着一起抬（不抬的话预算改动等于没做）；
//   - 每一组打一条 upstream_read，让「预算耗尽」与「上游坏了」分得开。

// structFieldNames 返回结构体的字段名，供「名单与被计数对象同源」这类闸使用。
func structFieldNames(v any) []string {
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		out = append(out, rt.Field(i).Name)
	}
	return out
}

// TestNewAPIReadBudgetDerivesFromChainLength：预算是 f(单次超时, 组数) 的
// **纯比例函数**，不含绝对常数余量。
//
// 比例这条不是洁癖：它让单元测试能把单次超时缩到毫秒级，跑出与生产同构的
// 「预算耗尽」形态，而不是真等 100 秒。
func TestNewAPIReadBudgetDerivesFromChainLength(t *testing.T) {
	perRequest := DefaultNewAPIRequestTimeout
	steps := len(newapiReadStepNames)

	// (a) 预算不得小于「每组各跑满一次单次超时」——今天那个 20s 常量连
	// 第三组都排不进去，正是 2026-09-08 三条失败的形态。
	if got := newapiReadBudget(perRequest, steps); got < time.Duration(steps)*perRequest {
		t.Fatalf("newapiReadBudget(%v, %d) = %v, 至少要有 %v", perRequest, steps, got, time.Duration(steps)*perRequest)
	}
	// (b) 链加长一组，预算至少多一次请求的量。
	grew := newapiReadBudget(perRequest, steps+1) - newapiReadBudget(perRequest, steps)
	if grew < perRequest {
		t.Fatalf("链加长一组预算只多了 %v, 至少要多 %v", grew, perRequest)
	}
	// (c) 纯比例：缩小 500 倍的单次超时，预算也缩小 500 倍。
	if got, want := newapiReadBudget(20*time.Millisecond, steps)*500, newapiReadBudget(10*time.Second, steps); got != want {
		t.Fatalf("预算不是纯比例：%v*500 = %v, want %v", 20*time.Millisecond, got, want)
	}
	// (d) fail closed：非法组数不给零预算，否则每一轮会秒失败。
	if got := newapiReadBudget(perRequest, 0); got <= 0 {
		t.Fatalf("newapiReadBudget(_, 0) = %v, want > 0", got)
	}
	if got := newapiGroupBudget(0); got != DefaultNewAPIRequestTimeout*newapiStallAllowancePerGroup {
		t.Fatalf("零值单次超时应回落到默认: %v", got)
	}
}

// TestSub2APIReadBudgetDerivesFromChainLength 是 sub2api 侧的同型断言。
func TestSub2APIReadBudgetDerivesFromChainLength(t *testing.T) {
	perRequest := DefaultSub2APIRequestTimeout
	steps := len(sub2apiReadStepNames)
	if got := sub2apiReadBudget(perRequest, steps); got < time.Duration(steps)*perRequest {
		t.Fatalf("sub2apiReadBudget = %v, 至少要有 %v", got, time.Duration(steps)*perRequest)
	}
	if grew := sub2apiReadBudget(perRequest, steps+1) - sub2apiReadBudget(perRequest, steps); grew < perRequest {
		t.Fatalf("链加长一组预算只多了 %v", grew)
	}
	if got, want := sub2apiReadBudget(20*time.Millisecond, steps)*500, sub2apiReadBudget(10*time.Second, steps); got != want {
		t.Fatalf("预算不是纯比例: %v vs %v", got, want)
	}
	if got := sub2apiReadBudget(perRequest, 0); got <= 0 {
		t.Fatalf("sub2apiReadBudget(_, 0) = %v, want > 0", got)
	}
}

// TestSyncJobTimeoutsCoverTheReadBudget：River 默认 JobTimeout 是 1 分钟。
//
// 读预算一旦抬过它，没有 Timeout() 覆写的结果是那一轮连「同步失败」都写不
// 进库——看板从「正在失败」退化成「数据静静变旧」，比现在还糟。今天 20s 读
// + 40s 写恰好塞得下所以没人发现缺这个方法，这条就是那道闸。
func TestSyncJobTimeoutsCoverTheReadBudget(t *testing.T) {
	newapiWorker := NewNewAPISyncWorker(NewAPISyncOptions{Store: newMemoryStore(), NewClient: newapiFakeFactory(newapi.FakeOptions{})})
	sub2apiWorker := NewSub2APISyncWorker(Sub2APISyncOptions{Store: newMemoryStore(), NewClient: fakeFactory(sub2api.FakeOptions{})})

	newapiTimeout := newapiWorker.Timeout(nil)
	if newapiTimeout <= 0 {
		t.Fatal("NewAPISyncWorker 没有覆写 Timeout：River 会按默认 1 分钟掐掉整轮")
	}
	newapiRead := newapiReadBudget(DefaultNewAPIRequestTimeout, len(newapiReadStepNames))
	if newapiTimeout <= newapiRead {
		t.Fatalf("newapi JobTimeout %v 不大于读预算 %v：读完就没时间写库了", newapiTimeout, newapiRead)
	}
	if newapiTimeout >= DefaultNewAPISyncInterval {
		t.Fatalf("newapi JobTimeout %v 不小于同步周期 %v：一轮会压到下一轮", newapiTimeout, DefaultNewAPISyncInterval)
	}

	sub2apiTimeout := sub2apiWorker.Timeout(nil)
	if sub2apiTimeout <= 0 {
		t.Fatal("Sub2APISyncWorker 没有覆写 Timeout")
	}
	sub2apiRead := sub2apiReadBudget(DefaultSub2APIRequestTimeout, len(sub2apiReadStepNames))
	if sub2apiTimeout <= sub2apiRead {
		t.Fatalf("sub2api JobTimeout %v 不大于读预算 %v", sub2apiTimeout, sub2apiRead)
	}
	if sub2apiTimeout >= DefaultSub2APISyncInterval {
		t.Fatalf("sub2api JobTimeout %v 不小于同步周期 %v", sub2apiTimeout, DefaultSub2APISyncInterval)
	}

	// 注入的单次超时也要带动 JobTimeout——否则改了 XM_NEWAPI_REQUEST_TIMEOUT
	// 的部署会拿到一个与自己的读预算对不上的任务超时。
	slow := NewNewAPISyncWorker(NewAPISyncOptions{
		Store: newMemoryStore(), NewClient: newapiFakeFactory(newapi.FakeOptions{}),
		RequestTimeout: 2 * DefaultNewAPIRequestTimeout,
	})
	if slow.Timeout(nil) <= newapiTimeout {
		t.Fatalf("单次超时翻倍后 JobTimeout 没跟着涨: %v vs %v", slow.Timeout(nil), newapiTimeout)
	}
}

// TestNewAPIReadStepsCoverEveryReadErrorField：步骤名单必须与被计数的对象
// 同源（memory「闸的范围要发现不要手列」）。
//
// 往 newapiReadErrors 加一个字段却忘了加步骤名，预算就会按旧组数算，而且
// 不报错——这条让它在 CI 就红。
func TestNewAPIReadStepsCoverEveryReadErrorField(t *testing.T) {
	if got, want := len(newapiReadStepNames), len(structFieldNames(newapiReadErrors{})); got != want {
		t.Fatalf("newapiReadStepNames 有 %d 组, newapiReadErrors 有 %d 个字段: %v vs %v",
			got, want, newapiReadStepNames, structFieldNames(newapiReadErrors{}))
	}
	if got, want := len(sub2apiReadStepNames), len(structFieldNames(sub2apiReadErrors{})); got != want {
		t.Fatalf("sub2apiReadStepNames 有 %d 组, sub2apiReadErrors 有 %d 个字段: %v vs %v",
			got, want, sub2apiReadStepNames, structFieldNames(sub2apiReadErrors{}))
	}
	// 每一组都要有指标键，且并集必须**恰好**是这个连接器的全部指标键——
	// 少一条说明有指标没人喂，多一条说明名单抄错了。
	seen := map[string]bool{}
	for _, name := range newapiReadStepNames {
		keys := newapiReadStepMetricKeys[name]
		if len(keys) == 0 {
			t.Fatalf("读取组 %q 没有登记指标键", name)
		}
		for _, key := range keys {
			seen[key] = true
		}
	}
	for _, key := range newapiMetricKeys {
		if !seen[key] {
			t.Fatalf("指标 %s 不属于任何读取组", key)
		}
		delete(seen, key)
	}
	if len(seen) != 0 {
		t.Fatalf("读取组登记了不存在的指标键: %v", seen)
	}

	seen = map[string]bool{}
	for _, name := range sub2apiReadStepNames {
		keys := sub2apiReadStepMetricKeys[name]
		if len(keys) == 0 {
			t.Fatalf("读取组 %q 没有登记指标键", name)
		}
		for _, key := range keys {
			seen[key] = true
		}
	}
	for _, key := range contractMetricKeys {
		if !seen[key] {
			t.Fatalf("指标 %s 不属于任何读取组", key)
		}
		delete(seen, key)
	}
	if len(seen) != 0 {
		t.Fatalf("读取组登记了不存在的指标键: %v", seen)
	}
}

// TestNewAPIReadChainMatchesDeclaredSteps：跑一轮，从 upstream_read 日志
// 反过来钉住那份名单。
//
// 名单只用于算预算，执行走的是 read() 里那条链——两边漂开时预算会按旧组数
// 默默算，谁都不报错。这条把「跑出来的链」与「声明的名单」焊在一起。
func TestNewAPIReadChainMatchesDeclaredSteps(t *testing.T) {
	var logs bytes.Buffer
	if err := newTestNewAPISyncWorker(newMemoryStore(), newapiFakeFactory(newapi.FakeOptions{}), &logs).
		Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range logEvents(t, logs.String(), "upstream_read") {
		got = append(got, row["read_step"].(string))
	}
	if len(got) != len(newapiReadStepNames) {
		t.Fatalf("跑出来 %d 组读取, 名单声明 %d 组: %v vs %v",
			len(got), len(newapiReadStepNames), got, newapiReadStepNames)
	}
	for i := range got {
		if got[i] != newapiReadStepNames[i] {
			t.Fatalf("第 %d 组跑的是 %q, 名单说是 %q", i+1, got[i], newapiReadStepNames[i])
		}
	}

	logs.Reset()
	if err := newTestSyncWorker(newMemoryStore(), fakeFactory(sub2api.FakeOptions{}), &logs).
		Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, row := range logEvents(t, logs.String(), "upstream_read") {
		got = append(got, row["read_step"].(string))
	}
	if len(got) != len(sub2apiReadStepNames) {
		t.Fatalf("sub2api 跑出来 %d 组, 名单声明 %d 组: %v", len(got), len(sub2apiReadStepNames), got)
	}
	for i := range got {
		if got[i] != sub2apiReadStepNames[i] {
			t.Fatalf("sub2api 第 %d 组跑的是 %q, 名单说是 %q", i+1, got[i], sub2apiReadStepNames[i])
		}
	}
}

// deadlineRecordingClient 记下每一次读取拿到的 ctx.Deadline()，
// 用来验「每组各有自己的 deadline」这件事。
type deadlineRecordingClient struct {
	newapi.PaymentsReadClient
	deadlines []time.Time
}

func (c *deadlineRecordingClient) record(ctx context.Context) {
	if deadline, ok := ctx.Deadline(); ok {
		c.deadlines = append(c.deadlines, deadline)
	}
}

func (c *deadlineRecordingClient) UserStats(ctx context.Context) (newapi.UserStats, error) {
	c.record(ctx)
	return c.PaymentsReadClient.UserStats(ctx)
}

func (c *deadlineRecordingClient) DailyOrders(ctx context.Context, day string) (newapi.OrderSummary, error) {
	c.record(ctx)
	return c.PaymentsReadClient.DailyOrders(ctx, day)
}

func (c *deadlineRecordingClient) ChannelDirectory(ctx context.Context) (newapi.ChannelDirectorySnapshot, error) {
	c.record(ctx)
	return c.PaymentsReadClient.ChannelDirectory(ctx)
}

func (c *deadlineRecordingClient) ModelUsages(ctx context.Context, day string) ([]newapi.ModelUsage, error) {
	c.record(ctx)
	return c.PaymentsReadClient.ModelUsages(ctx, day)
}

func (c *deadlineRecordingClient) DailyPaymentSummary(ctx context.Context, day string) (newapi.DailyPaymentSummary, error) {
	c.record(ctx)
	return c.PaymentsReadClient.DailyPaymentSummary(ctx, day)
}

// TestNewAPIEachReadGroupGetsItsOwnDeadline：一组一个 deadline，不是五组
// 共用一个。
//
// 共用那份预算正是 2026-09-08 的失败形态：前两组慢一点，排在后面的三组还
// 没开始就被判 unavailable。deadline **严格递增**是「每组各自续期」的判据；
// 若实现退回单一整轮 deadline，五个值会完全相同，这条立刻红。
func TestNewAPIEachReadGroupGetsItsOwnDeadline(t *testing.T) {
	// 注入延迟让五组的**起点**拉开：零延迟时五个 deadline 会落在同一个
	// 毫秒里，「各自续期」与「共用一个」在时间上分不开。
	const latency = 20 * time.Millisecond
	client := &deadlineRecordingClient{
		PaymentsReadClient: newapi.NewFake(newapi.FakeOptions{
			Now: func() time.Time { return fixedNow }, Latency: latency,
		}),
	}
	var logs bytes.Buffer
	worker := NewNewAPISyncWorker(NewAPISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultNewAPIInstanceID,
		Store:       newMemoryStore(),
		NewClient: func(context.Context) (newapi.ReadClientV2, EffectiveConnectorConfig, error) {
			return client, newapiEnvFakeConfig, nil
		},
		Now: func() time.Time { return fixedNow },
	})
	start := time.Now()
	if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}

	if len(client.deadlines) != len(newapiReadStepNames) {
		t.Fatalf("拿到 %d 个 deadline, want %d", len(client.deadlines), len(newapiReadStepNames))
	}
	groupBudget := newapiGroupBudget(DefaultNewAPIRequestTimeout)
	roundBudget := newapiReadBudget(DefaultNewAPIRequestTimeout, len(newapiReadStepNames))
	for i, deadline := range client.deadlines {
		// deadline 挂在**墙钟**上，不是注入的 fixedNow（2026-08-27）：
		// 后者会让每一轮秒超时。
		if !deadline.After(start) {
			t.Fatalf("第 %d 组的 deadline %v 不在将来（start=%v）——是不是把注入的时钟接上去了？",
				i+1, deadline, start)
		}
		// 每组拿到的是「组预算」而不是「整轮预算」。容差 5s 够宽，
		// 但远小于 groupBudget(20s) 与 roundBudget(100s) 的差距。
		if got := deadline.Sub(start); got > groupBudget+5*time.Second {
			t.Fatalf("第 %d 组的预算 %v 超过一组的量 %v：像是五组共用了整轮 deadline",
				i+1, got, groupBudget)
		}
		if deadline.Sub(start) > roundBudget {
			t.Fatalf("第 %d 组的 deadline 超出了整轮预算 %v", i+1, roundBudget)
		}
		if i > 0 && deadline.Before(client.deadlines[i-1]) {
			t.Fatalf("第 %d 组的 deadline 早于上一组: %v < %v", i+1, deadline, client.deadlines[i-1])
		}
	}
	// 每组各自续期的判据：最后一组的 deadline 比第一组晚了大约「前四组的
	// 执行时间」。若实现退回单一整轮 deadline，五个值会完全相同，这里为 0。
	spread := client.deadlines[len(client.deadlines)-1].Sub(client.deadlines[0])
	if minSpread := time.Duration(len(client.deadlines)-1) * latency / 2; spread < minSpread {
		t.Fatalf("首尾两组的 deadline 只差 %v（至少该有 %v）：像是五组共用了同一个 deadline，没有各自续期",
			spread, minSpread)
	}
}

// TestNewAPISyncLogsPerReadElapsed：每一组打一条 upstream_read，字段齐全。
func TestNewAPISyncLogsPerReadElapsed(t *testing.T) {
	var logs bytes.Buffer
	// Latency 让每组都有可测的耗时；Fake 的 wait 尊重 ctx 取消。
	factory := newapiFakeFactory(newapi.FakeOptions{Latency: 25 * time.Millisecond})
	if err := newTestNewAPISyncWorker(newMemoryStore(), factory, &logs).
		Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}

	rows := logEvents(t, logs.String(), "upstream_read")
	if len(rows) != len(newapiReadStepNames) {
		t.Fatalf("upstream_read 行数 = %d, want %d", len(rows), len(newapiReadStepNames))
	}
	seenStep := map[string]bool{}
	for i, row := range rows {
		for _, key := range []string{"read_step", "metric_keys", "elapsed_ms", "status", "error_code", "group_budget_ms", "round_remaining_ms", "sequence"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("upstream_read 缺字段 %s: %v", key, row)
			}
		}
		step := row["read_step"].(string)
		if seenStep[step] {
			t.Fatalf("read_step %q 出现了两次", step)
		}
		seenStep[step] = true
		wantField(t, row, "sequence", float64(i+1))
		wantField(t, row, "status", "ok")
		wantField(t, row, "error_code", "")
		// elapsed_ms 必须是数值而不是字符串，且只设下界（不设上界，不会 flake）。
		elapsed, ok := row["elapsed_ms"].(float64)
		if !ok {
			t.Fatalf("elapsed_ms 不是数值: %v", row["elapsed_ms"])
		}
		if elapsed < 20 {
			t.Fatalf("%s 的 elapsed_ms = %v, 注入 25ms 延迟后至少该有 20", step, elapsed)
		}
		// 上界抓的是「把整轮累计耗时抄了五遍」这种实现：五组各 25ms，
		// 累计计时会让第 4、5 组变成 100ms / 125ms。75ms 对 Windows 上
		// 15.6ms 的定时器粒度仍有余量，但远小于第 4 组的累计值。
		if elapsed > 75 {
			t.Fatalf("%s 的 elapsed_ms = %v, 单组延迟只有 25ms——是不是打成了整轮累计耗时？", step, elapsed)
		}
	}
	// orders 一组同时喂两条指标——单数 metric_key 在这一组必然说谎，
	// 所以字段是复数 metric_keys，这里逐字对。
	for _, row := range rows {
		if row["read_step"] == "orders" {
			wantField(t, row, "metric_keys", newapi.MetricRechargeDaily+","+newapi.MetricSubscriptionDaily)
		}
	}
}

// TestNewAPIUpstreamReadStatusIsPerGroup：状态是逐组的，不是整轮的。
func TestNewAPIUpstreamReadStatusIsPerGroup(t *testing.T) {
	var logs bytes.Buffer
	factory := func(context.Context) (newapi.ReadClientV2, EffectiveConnectorConfig, error) {
		return newapiPartialFailClient{
			PaymentsReadClient: newapi.NewFake(newapi.FakeOptions{Now: func() time.Time { return fixedNow }}),
			usagesErr:          connector.NewError(connector.KindRateLimited, "newapi.models.usage_read", nil),
		}, newapiEnvFakeConfig, nil
	}
	if err := newTestNewAPISyncWorker(newMemoryStore(), factory, &logs).
		Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	for _, row := range logEvents(t, logs.String(), "upstream_read") {
		if row["read_step"] == "usages" {
			wantField(t, row, "status", "failed")
			wantField(t, row, "error_code", string(connector.KindRateLimited))
			continue
		}
		wantField(t, row, "status", "ok")
		wantField(t, row, "error_code", "")
	}
}

// TestUpstreamReadTellsSlowUpstreamFromFailingUpstream 是 2026-09-08 那道
// 「两说都自洽、分不清」的题的可执行判据（报告三·2）。
//
// 两个场景写进库的结果一模一样（六条指标全 failed），只有逐组耗时分得开：
//   - **上游变慢 / 预算耗尽**：每组的 elapsed_ms 顶到 group_budget_ms，
//     round_remaining_ms 一路被抽干、到最后一组趋近 0；
//   - **上游那几个接口自己坏了**：每组很快返回，elapsed_ms 远小于
//     group_budget_ms，round_remaining_ms 始终宽裕。
//
// 顺带钉住修复本身：慢的那一轮里**每一组都真的跑了**（elapsed 都顶到组预算），
// 而不是「前两组把 20 秒用光、后三组没轮到就被判超时」——旧实现的形态。
//
// 单次超时可注入（RequestTimeout）+ 预算是纯比例函数，这条才跑得起来：
// 否则要真等 100 秒。
func TestUpstreamReadTellsSlowUpstreamFromFailingUpstream(t *testing.T) {
	const perRequest = 40 * time.Millisecond
	groupBudget := newapiGroupBudget(perRequest) // 80ms
	runRound := func(latency time.Duration, failWith connector.ErrorKind) []map[string]any {
		t.Helper()
		var logs bytes.Buffer
		worker := NewNewAPISyncWorker(NewAPISyncOptions{
			Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
			Environment:    "staging",
			InstanceID:     DefaultNewAPIInstanceID,
			Store:          newMemoryStore(),
			RequestTimeout: perRequest,
			NewClient: newapiFakeFactory(newapi.FakeOptions{
				Latency: latency, FailWith: failWith,
			}),
			Now: func() time.Time { return fixedNow },
		})
		if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
			t.Fatal(err)
		}
		return logEvents(t, logs.String(), "upstream_read")
	}

	// A：每一组都慢过自己的组预算 → 每组烧满 80ms，整轮余量被抽干。
	slow := runRound(2*groupBudget, "")
	// B：每一组都很快地失败（上游 5xx 的形态）。
	broken := runRound(5*time.Millisecond, connector.KindUnavailable)

	steps := len(newapiReadStepNames)
	if len(slow) != steps || len(broken) != steps {
		t.Fatalf("两个场景都该打满 %d 条 upstream_read: %d / %d", steps, len(slow), len(broken))
	}
	ms := func(row map[string]any, key string) float64 { return row[key].(float64) }
	budgetMS := float64(groupBudget.Milliseconds())

	for i := range slow {
		// 每一组都真的跑了、并且各自烧满自己的组预算——这正是「一组慢不再
		// 牵连别组」的证据（旧实现里后三组的 elapsed 会是 0）。
		if got := ms(slow[i], "elapsed_ms"); got < budgetMS*0.7 {
			t.Fatalf("变慢场景第 %d 组 elapsed_ms = %v, 该顶到组预算 %v 附近", i+1, got, budgetMS)
		}
		wantField(t, slow[i], "status", "failed")
		wantField(t, slow[i], "group_budget_ms", budgetMS)
	}
	// 整轮余量被一路抽干：最后一组开始前基本没剩多少。
	if first, last := ms(slow[0], "round_remaining_ms"), ms(slow[steps-1], "round_remaining_ms"); last >= first*0.5 {
		t.Fatalf("变慢场景的整轮余量没有被抽干：第 1 组 %v ms，第 %d 组 %v ms", first, steps, last)
	}

	for i := range broken {
		if got := ms(broken[i], "elapsed_ms"); got > budgetMS*0.5 {
			t.Fatalf("故障场景第 %d 组 elapsed_ms = %v, 该远小于组预算 %v", i+1, got, budgetMS)
		}
		wantField(t, broken[i], "status", "failed")
		wantField(t, broken[i], "error_code", string(connector.KindUnavailable))
	}
	// 故障场景的整轮余量始终宽裕（超过一半）。
	roundMS := float64(newapiReadBudget(perRequest, steps).Milliseconds())
	for i, row := range broken {
		if got := ms(row, "round_remaining_ms"); got < roundMS*0.5 {
			t.Fatalf("故障场景第 %d 组 round_remaining_ms = %v, 预算不该被吃掉一半以上（整轮 %v）", i+1, got, roundMS)
		}
	}
	// 判据句：把「分得开」这件事本身写成断言，而不是靠人眼看。
	for i := 0; i < steps; i++ {
		if ms(slow[i], "elapsed_ms") <= ms(broken[i], "elapsed_ms") {
			t.Fatalf("第 %d 组：变慢(%v) 与故障(%v) 的耗时分不开",
				i+1, ms(slow[i], "elapsed_ms"), ms(broken[i], "elapsed_ms"))
		}
	}
}

// TestSub2APISyncLogsPerReadElapsed 是 sub2api 侧的同型断言（四组）。
func TestSub2APISyncLogsPerReadElapsed(t *testing.T) {
	var logs bytes.Buffer
	if err := newTestSyncWorker(newMemoryStore(),
		fakeFactory(sub2api.FakeOptions{Latency: 25 * time.Millisecond}), &logs).
		Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	rows := logEvents(t, logs.String(), "upstream_read")
	if len(rows) != len(sub2apiReadStepNames) {
		t.Fatalf("upstream_read 行数 = %d, want %d", len(rows), len(sub2apiReadStepNames))
	}
	for i, row := range rows {
		wantField(t, row, "job_kind", Sub2APISyncJobKind)
		wantField(t, row, "sequence", float64(i+1))
		wantField(t, row, "status", "ok")
		if got := row["elapsed_ms"].(float64); got < 20 {
			t.Fatalf("%v 的 elapsed_ms = %v, 注入 25ms 延迟后至少该有 20", row["read_step"], got)
		}
	}
	// balances 一组同时喂余额与渠道状态两条指标。
	for _, row := range rows {
		if row["read_step"] == "balances" {
			wantField(t, row, "metric_keys", sub2api.MetricChannelBalance+","+sub2api.MetricChannelsStatus)
		}
	}
}
