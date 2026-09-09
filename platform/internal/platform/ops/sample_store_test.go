package ops_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// samplePool 复用 testPool 的连接与跳过逻辑，额外清空样本表。
//
// 不改 testPool 本身：那个 helper 由最新态的用例共用，样本表对它们是无关的。
func samplePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE ops.metric_observation_sample"); err != nil {
		t.Fatalf("清空样本表失败: %v", err)
	}
	return pool
}

// sampleAt 造一条落在指定时刻的成功样本。
func sampleAt(key string, syncedAt time.Time) ops.Observation {
	observedAt := syncedAt
	return ops.Observation{
		MetricKey: key, Source: "sub2api-prod", Environment: "production",
		ObservedAt: &observedAt, SyncedAt: syncedAt,
		Watermark: "wm-" + syncedAt.Format(time.RFC3339), Status: ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

// TestInsertSampleReturnsSeriesInAscendingOrder：趋势图靠顺序吃饭，
// 乱序会画成一团麻线。写入顺序刻意打乱，验证排序来自库而不是写入顺序。
func TestInsertSampleReturnsSeriesInAscendingOrder(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-30 * time.Minute)

	// 乱序写入：t+10、t+0、t+20
	for _, offset := range []time.Duration{10 * time.Minute, 0, 20 * time.Minute} {
		if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", base.Add(offset))); err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("样本数 = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i].SyncedAt.After(got[i-1].SyncedAt) {
			t.Fatalf("样本未按 synced_at 升序: %v", []time.Time{
				got[0].SyncedAt, got[1].SyncedAt, got[2].SyncedAt})
		}
	}
	// 追加型：同一个 (metric_key, environment) 保留全部三条，不像最新态那样覆盖。
	if got[0].Value["currency"] != "CNY" || got[0].Watermark == "" {
		t.Fatalf("样本字段未如实读回: %+v", got[0])
	}
	if got[0].Environment != "production" || got[0].Source != "sub2api-prod" {
		t.Fatalf("样本来源字段未如实读回: %+v", got[0])
	}
}

// TestListSamplesFiltersByWindowAndKeyAndEnvironment：窗口外、别的指标、
// 别的环境的点一个都不许混进来。跨环境混入是最危险的一种——
// 生产数字出现在 staging 的图上（宪法 15 条）。
func TestListSamplesFiltersByWindowAndKeyAndEnvironment(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	inWindow := sampleAt("sub2api.revenue.daily", now.Add(-10*time.Minute))
	outOfWindow := sampleAt("sub2api.revenue.daily", now.Add(-3*time.Hour))
	otherMetric := sampleAt("sub2api.cost.daily", now.Add(-10*time.Minute))
	otherEnv := sampleAt("sub2api.revenue.daily", now.Add(-10*time.Minute))
	otherEnv.Environment = "development"

	for _, o := range []ops.Observation{inWindow, outOfWindow, otherMetric, otherEnv} {
		if err := s.InsertSample(ctx, o); err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	// 1 小时窗口：只该剩 inWindow 一条
	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("窗口过滤后样本数 = %d, want 1: %+v", len(got), got)
	}
	if !got[0].SyncedAt.Equal(inWindow.SyncedAt) {
		t.Fatalf("留下的不是窗口内那条: %v want %v", got[0].SyncedAt, inWindow.SyncedAt)
	}

	// 放宽到 4 小时：窗口外那条回来了，仍然不含别的指标/环境
	wide, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-4*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide) != 2 {
		t.Fatalf("放宽窗口后样本数 = %d, want 2", len(wide))
	}
}

// TestListSamplesLimitKeepsNewest：窗口内超量时丢掉的必须是**最旧**的。
//
// 若留下最旧的那批，图会在窗口中途断掉，看起来像同步早就死了——
// 那是假的故障信号，比数据少几个点糟得多。
func TestListSamplesLimitKeepsNewest(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", at)); err != nil {
			t.Fatal(err)
		}
	}

	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=2 应只返回 2 条, got %d", len(got))
	}
	// 保留的是 t+3、t+4，且仍是升序
	if !got[0].SyncedAt.Equal(base.Add(3*time.Minute)) || !got[1].SyncedAt.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("limit 应丢掉最旧的样本: got %v, %v", got[0].SyncedAt, got[1].SyncedAt)
	}

	// limit <= 0 与超上限都钳到 MaxSampleLimit，而不是变成全表扫描
	for _, limit := range []int32{0, -1, ops.MaxSampleLimit + 1} {
		all, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), limit)
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if len(all) != 5 {
			t.Fatalf("limit=%d 应钳到上限并返回全部 5 条, got %d", limit, len(all))
		}
	}
}

// TestListSamplesReportsTruncation 回归 Codex 冷审 PR #48 第 2 条：
// 「允许 168 小时，却静默截成最新 1000 点，响应没有任何『被截断』的事实」。
//
// 「前 3.5 天真的没有数据」与「服务端把它裁掉了」必须是两个可区分的事实，
// 否则前端会把一个不完整的窗口画成完整趋势（宪法 12 条）。
func TestListSamplesReportsTruncation(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", at)); err != nil {
			t.Fatal(err)
		}
	}
	since := base.Add(-time.Hour)

	// 窗口内 5 条、只要 2 条 → 截断
	got, truncated, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", since, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("窗口内还有更旧的样本没返回时必须报告截断")
	}
	if len(got) != 2 {
		t.Fatalf("截断时仍应返回恰好 limit 条, got %d", len(got))
	}
	// 截断保留的仍是最新的那批——多取的那一行是判据，不能混进结果
	if !got[1].SyncedAt.Equal(base.Add(4 * time.Minute)) {
		t.Fatalf("截断后最后一点 = %v, want %v", got[1].SyncedAt, base.Add(4*time.Minute))
	}

	// 边界：窗口内恰好 5 条、limit 也是 5 → **不算**截断。
	// 这是多取一行判据的关键边界：limit+1 只取到 5 条，说明没有更旧的了。
	exact, truncated, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", since, 5)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("窗口内条数恰好等于 limit 时不该报告截断")
	}
	if len(exact) != 5 {
		t.Fatalf("样本数 = %d, want 5", len(exact))
	}

	// limit 大于窗口内条数 → 不截断
	if _, truncated, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", since, 100); err != nil {
		t.Fatal(err)
	} else if truncated {
		t.Fatal("limit 富余时不该报告截断")
	}

	// 空窗口 → 不截断（「没有数据」不是「被裁掉了」）
	if items, truncated, err := s.ListSamples(
		ctx, "production", "sub2api.revenue.daily", time.Now().UTC().Add(time.Hour), 100,
	); err != nil {
		t.Fatal(err)
	} else if truncated || len(items) != 0 {
		t.Fatalf("空窗口应为 0 条且不截断: %d 条, truncated=%v", len(items), truncated)
	}
}

// TestListSamplesOrdersTiedTimestampsDeterministically 回归 Codex 冷审
// PR #48 第 7 条：「同时间戳样本没有确定排序或幂等键」。
//
// River 重试、多副本、同一秒内两次采集都能产生相同的 synced_at。只按
// synced_at 排序时，撞点的相对顺序由 PostgreSQL 自行决定，于是 limit 边界上
// 「留哪一条」在两次相同的查询之间可能不同——趋势图会莫名抖动且无法复现。
func TestListSamplesOrdersTiedTimestampsDeterministically(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	// 同一个 synced_at 上写 6 条，只有 watermark 不同——靠它认出是哪一条
	for i := 0; i < 6; i++ {
		o := sampleAt("sub2api.revenue.daily", at)
		o.Watermark = "wm-" + strconv.Itoa(i)
		if err := s.InsertSample(ctx, o); err != nil {
			t.Fatalf("InsertSample %d: %v", i, err)
		}
	}
	since := at.Add(-time.Hour)

	// 全量：顺序必须与写入顺序（= id 顺序）一致，且多次查询完全相同
	var first []string
	for round := 0; round < 3; round++ {
		got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", since, 100)
		if err != nil {
			t.Fatal(err)
		}
		marks := make([]string, 0, len(got))
		for _, o := range got {
			marks = append(marks, o.Watermark)
		}
		if round == 0 {
			first = marks
			want := []string{"wm-0", "wm-1", "wm-2", "wm-3", "wm-4", "wm-5"}
			if strings.Join(marks, ",") != strings.Join(want, ",") {
				t.Fatalf("同时间戳样本应按 id 升序: got %v, want %v", marks, want)
			}
			continue
		}
		if strings.Join(marks, ",") != strings.Join(first, ",") {
			t.Fatalf("第 %d 轮顺序与第一轮不同: %v vs %v", round, marks, first)
		}
	}

	// limit 边界：全部撞在同一时刻时，留下的必须**确定**是 id 最大的那两条
	for round := 0; round < 3; round++ {
		got, truncated, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", since, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Fatal("6 条取 2 条必须报告截断")
		}
		if len(got) != 2 || got[0].Watermark != "wm-4" || got[1].Watermark != "wm-5" {
			t.Fatalf("第 %d 轮 limit 边界选择不确定: %+v", round,
				[]string{got[0].Watermark, got[1].Watermark})
		}
	}
}

// TestInsertSampleKeepsFailedObservations：失败观测正是趋势图上「那段红」
// 的数据来源。它必须能落库，而且错误码必须留住。
func TestInsertSampleKeepsFailedObservations(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	failed := sampleAt("sub2api.revenue.daily", now.Add(-5*time.Minute))
	failed.Status = ops.SyncFailed
	failed.LastErrorCode = "unavailable"
	// 失败时上游没给新数据：observed_at 停在上一次成功的时刻
	previous := now.Add(-20 * time.Minute)
	failed.ObservedAt = &previous

	if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", now.Add(-20*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSample(ctx, failed); err != nil {
		t.Fatalf("失败样本必须写得进去: %v", err)
	}

	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("样本数 = %d, want 2", len(got))
	}
	if got[0].Status != ops.SyncOK {
		t.Fatalf("第一个点应为 ok: %+v", got[0])
	}
	if got[1].Status != ops.SyncFailed || got[1].LastErrorCode != "unavailable" {
		t.Fatalf("失败样本的状态与错误码必须留住: %+v", got[1])
	}
	// 失败点的 observed_at 停在旧时刻，但它在横轴上的位置由 synced_at 决定
	if got[1].ObservedAt == nil || !got[1].ObservedAt.Equal(previous) {
		t.Fatalf("失败样本的 observed_at = %v, want %v", got[1].ObservedAt, previous)
	}
}

// TestInsertSampleAllowsUninitializedObservation：从未成功采集时 observed_at
// 为空。历史上那一段「灰」同样要有点，不能整段消失。
func TestInsertSampleAllowsUninitializedObservation(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	o := sampleAt("sub2api.revenue.daily", now.Add(-5*time.Minute))
	o.ObservedAt = nil
	o.Status = ops.SyncFailed
	o.LastErrorCode = "never_synced"
	o.Value = nil

	if err := s.InsertSample(ctx, o); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ObservedAt != nil {
		t.Fatalf("observed_at 应保持为空: %+v", got)
	}
	// value 为 nil 时落成空对象，读回也是空 map——前端不必先判 null
	if got[0].Value == nil || len(got[0].Value) != 0 {
		t.Fatalf("空值应落成空对象, got %+v", got[0].Value)
	}
}

// TestInsertSampleRejectsSilentFailure：失败却没有错误码，在领域层就该被拒。
// 放行一条没有原因的失败样本，图上就会出现一段没人解释得了的红。
func TestInsertSampleRejectsSilentFailure(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	o := sampleAt("sub2api.revenue.daily", time.Now().UTC())
	o.Status = ops.SyncFailed // 没有 LastErrorCode
	if err := s.InsertSample(context.Background(), o); err == nil {
		t.Fatal("失败但无错误码必须被拒")
	}
}

// TestMoneyRoundTripKeepsPrecisionBeyondFloat64 回归 Codex 冷审 PR #48 第 4 条
// （PR #43 head `419ecf8` 同条）：「金额历史经 map[string]any 读回会退化为
// float64，超过 2^53 的 integer minor units 已经丢精度」。
//
// 9007199254740995 = 2^53 + 3。float64 的尾数只有 53 位，装不下它：默认
// json.Unmarshal 读回会得到 9007199254740996，静默差 1。这张表长期保存财务
// 趋势，一旦失真就是**永久**的——库里那条 jsonb 还是对的，但每次读取都返回
// 错的值。最新态与历史样本两条读回路径都要验。
func TestMoneyRoundTripKeepsPrecisionBeyondFloat64(t *testing.T) {
	pool := samplePool(t)
	s := ops.NewStore(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// 2^53 + 3：float64 表示不出来的最小一类整数
	const bigMinor = "9007199254740995"
	// 另取一个更大的、以及一个负的（透支余额），确保不是只对某个特例成立
	const hugeMinor = "9223372036854775807" // int64 上限
	const negMinor = "-9007199254740995"

	value := map[string]any{
		"amount_minor":          json.Number(bigMinor),
		"balance_minor_units":   json.Number(hugeMinor),
		"overdraft_minor_units": json.Number(negMinor),
		"currency":              "CNY",
	}

	// ---- 历史样本路径（ListSamples）----
	o := sampleAt("sub2api.revenue.daily", now.Add(-5*time.Minute))
	o.Value = value
	if err := s.InsertSample(ctx, o); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	got, _, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("样本数 = %d, want 1", len(got))
	}
	assertExactMinor(t, "ListSamples", got[0].Value, bigMinor, hugeMinor, negMinor)

	// ---- 最新态路径（Upsert 读回 / Get / ListByEnvironment）----
	latest := sample("sub2api.revenue.daily")
	latest.Value = value
	back, err := s.Upsert(ctx, latest)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	assertExactMinor(t, "Upsert 读回", back.Value, bigMinor, hugeMinor, negMinor)

	fetched, err := s.Get(ctx, "sub2api.revenue.daily", "production")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertExactMinor(t, "Get", fetched.Value, bigMinor, hugeMinor, negMinor)

	listed, err := s.ListByEnvironment(ctx, "production")
	if err != nil {
		t.Fatalf("ListByEnvironment: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("最新态条数 = %d, want 1", len(listed))
	}
	assertExactMinor(t, "ListByEnvironment", listed[0].Value, bigMinor, hugeMinor, negMinor)

	// 再往前一步：值序列化回 JSON 时必须是**数字字面量**，不能是带引号的
	// 字符串——否则前端契约变了，图上会画不出来。
	encoded, err := json.Marshal(listed[0].Value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"amount_minor":`+bigMinor) {
		t.Fatalf("金额必须原样输出为数字字面量: %s", body)
	}
	if strings.Contains(body, `"`+bigMinor+`"`) {
		t.Fatalf("金额被序列化成了字符串，契约漂移: %s", body)
	}
}

// assertExactMinor 断言三个金额字段逐字精确，且类型是 json.Number
// （不是 float64——那正是被修的退化）。
func assertExactMinor(t *testing.T, path string, value map[string]any, want ...string) {
	t.Helper()
	keys := []string{"amount_minor", "balance_minor_units", "overdraft_minor_units"}
	for i, key := range keys {
		raw, ok := value[key]
		if !ok {
			t.Fatalf("%s: 缺少 %s", path, key)
		}
		num, ok := raw.(json.Number)
		if !ok {
			t.Fatalf("%s: %s 类型 = %T（值 %v），want json.Number——float64 会丢精度",
				path, key, raw, raw)
		}
		if num.String() != want[i] {
			t.Fatalf("%s: %s = %s, want %s（精度丢失）", path, key, num.String(), want[i])
		}
		// 能无损转回 int64 才算真的没丢
		n, err := num.Int64()
		if err != nil {
			t.Fatalf("%s: %s 不是整数: %v", path, key, err)
		}
		if strconv.FormatInt(n, 10) != want[i] {
			t.Fatalf("%s: %s 转 int64 后 = %d, want %s", path, key, n, want[i])
		}
	}
}

// TestDatabaseRejectsInconsistentSampleStatus：绕过领域校验直接插库，
// 验证数据库 CHECK 也拦得住（纵深防御，与最新态表同一条规则）。
func TestDatabaseRejectsInconsistentSampleStatus(t *testing.T) {
	pool := samplePool(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO ops.metric_observation_sample (
			metric_key, source, environment, synced_at, status, last_error_code
		) VALUES ('x.y.z', 'src', 'production', now(), 'failed', '')`)
	if err == nil {
		t.Fatal("failed 但无错误码必须被数据库拒绝")
	}
}
