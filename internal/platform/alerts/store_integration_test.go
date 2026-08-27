package alerts_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

// 本文件跑在真库上。去重与状态转换有一半逻辑在 SQL 里（部分唯一索引、
// TouchAlert 的 CASE、ResolveAlert 的 WHERE），用内存假货复刻只会测到假货。

const intEnv = "production"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE alerts.alert, alerts.alert_silence"); err != nil {
		t.Fatalf("清空告警表失败: %v", err)
	}
	return pool
}

func testStore(t *testing.T) *alerts.Store {
	t.Helper()
	return alerts.NewStore(testPool(t))
}

func upsertInput(now time.Time) alerts.UpsertInput {
	return alerts.UpsertInput{
		RuleKey:         alerts.RuleMetricSyncFailed,
		DedupKey:        alerts.RuleMetricSyncFailed + ":" + intEnv + ":sub2api.revenue.daily",
		Severity:        alerts.SeverityCritical,
		Title:           "指标 sub2api.revenue.daily 同步失败",
		Detail:          "错误码 timeout",
		Environment:     intEnv,
		SourceMetricKey: "sub2api.revenue.daily",
		Now:             now,
	}
}

// TestUpsertDeduplicatesAndCountsFires 是 §9.3「去重」在库层的落点。
//
// 同一个问题在多轮评估里必须收敛成**一条**告警 + 递增的 fire_count，
// 而不是每分钟新开一条。
func TestUpsertDeduplicatesAndCountsFires(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	first, created, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("第一次 Upsert: %v", err)
	}
	if !created {
		t.Fatal("第一次应是新建")
	}
	if first.FireCount != 1 {
		t.Fatalf("fire_count = %d, want 1", first.FireCount)
	}
	if first.Status != alerts.StatusOpen {
		t.Fatalf("status = %s, want OPEN", first.Status)
	}
	if first.NotifyStatus != alerts.NotifyPending {
		t.Fatalf("新告警的投递状态 = %s, want pending", first.NotifyStatus)
	}
	if !first.OpenedAt.Equal(first.LastSeenAt) {
		t.Fatalf("新建时 opened_at 应等于 last_seen_at: %s vs %s", first.OpenedAt, first.LastSeenAt)
	}

	// 再命中三轮，每轮 detail 都在变（余额、错误码会变）。
	var latest alerts.Alert
	for i := 1; i <= 3; i++ {
		in := upsertInput(now.Add(time.Duration(i) * time.Minute))
		in.Detail = "错误码 timeout（第 " + strings.Repeat("I", i) + " 轮）"
		got, created, err := s.Upsert(ctx, in)
		if err != nil {
			t.Fatalf("第 %d 次 Upsert: %v", i+1, err)
		}
		if created {
			t.Fatalf("第 %d 次不该新建——去重失效了", i+1)
		}
		latest = got
	}

	if latest.ID != first.ID {
		t.Fatalf("去重应合并进同一行: %s vs %s", latest.ID, first.ID)
	}
	if latest.FireCount != 4 {
		t.Fatalf("fire_count = %d, want 4", latest.FireCount)
	}
	if !latest.OpenedAt.Equal(first.OpenedAt) {
		t.Fatalf("opened_at 不该被后续命中改写: %s vs %s", latest.OpenedAt, first.OpenedAt)
	}
	if !latest.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("last_seen_at 应推进: %s", latest.LastSeenAt)
	}
	if !strings.Contains(latest.Detail, "III") {
		t.Fatalf("detail 应刷新成最新状态: %q", latest.Detail)
	}
	// 投递状态不该被重复命中重置——否则每 60 秒重发一次同样的消息。
	if latest.NotifyStatus != alerts.NotifyPending {
		t.Fatalf("投递状态 = %s", latest.NotifyStatus)
	}

	active, err := s.ListActive(ctx, intEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("库里应只有一条活跃告警，实际 %d 条", len(active))
	}
}

// TestUpsertSeparatesEnvironments：去重键带环境，两个环境互不干扰。
//
// 库层那条部分唯一索引只建在 dedup_key 一列上，所以环境**必须**在键里；
// 不在的话，staging 的评估轮次会改写生产的告警行（宪法 15 条）。
func TestUpsertSeparatesEnvironments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	prod := upsertInput(now)
	staging := upsertInput(now)
	staging.Environment = "staging"
	staging.DedupKey = alerts.RuleMetricSyncFailed + ":staging:sub2api.revenue.daily"

	if _, created, err := s.Upsert(ctx, prod); err != nil || !created {
		t.Fatalf("生产告警: created=%v err=%v", created, err)
	}
	if _, created, err := s.Upsert(ctx, staging); err != nil || !created {
		t.Fatalf("staging 告警应是独立的一条: created=%v err=%v", created, err)
	}

	prodActive, err := s.ListActive(ctx, intEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(prodActive) != 1 || prodActive[0].Environment != intEnv {
		t.Fatalf("生产列表混入了别的环境: %+v", prodActive)
	}
}

// TestResolveThenRecurrenceReopens：解决之后再发生 → REOPENED
// （规格 §9.3「重新打开」），而且历史那条 RESOLVED 留着。
func TestResolveThenRecurrenceReopens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	first, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	resolved, err := s.Resolve(ctx, first.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != alerts.StatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("解决后应有 resolved_at: %+v", resolved)
	}

	again, created, err := s.Upsert(ctx, upsertInput(now.Add(2*time.Minute)))
	if err != nil {
		t.Fatalf("复发 Upsert: %v", err)
	}
	if !created {
		t.Fatal("已解决的告警不在唯一索引里，复发应新开一行")
	}
	if again.Status != alerts.StatusReopened {
		t.Fatalf("复发状态 = %s, want REOPENED", again.Status)
	}
	if again.ID == first.ID {
		t.Fatal("复发应是新的一行，历史那条要留着")
	}

	// 历史证据还在：「这周炸了七次」靠的就是这些 RESOLVED 行。
	recent, err := s.ListRecent(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("应能看到 2 条（1 条已解决 + 1 条复发），实际 %d 条", len(recent))
	}
}

// TestResolveIsIdempotentlyRejected：重复解决返回 ErrNotFound 而不是静默成功，
// 否则 resolved_at 会被往后推，抹掉「什么时候好的」。
func TestResolveIsIdempotentlyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	a, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	first, err := s.Resolve(ctx, a.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := s.Resolve(ctx, a.ID, now.Add(time.Hour)); err == nil {
		t.Fatal("重复解决应报错")
	}

	after, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.ResolvedAt.Equal(*first.ResolvedAt) {
		t.Fatalf("resolved_at 被改写了: %s vs %s", after.ResolvedAt, first.ResolvedAt)
	}
}

// TestSilenceWindowTwoStates 是任务书要求的「静默窗口两态」。
//
// 窗口内：SILENCED、不进待投递队列。
// 窗口过期后条件仍成立：转回 OPEN，投递状态推回 pending 重新排队。
func TestSilenceWindowTwoStates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// --- 第一态：窗口内 ---
	silenced := upsertInput(now)
	silenced.Silenced = true
	a, created, err := s.Upsert(ctx, silenced)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !created || a.Status != alerts.StatusSilenced {
		t.Fatalf("窗口内应新建为 SILENCED: created=%v status=%s", created, a.Status)
	}

	pending, err := s.ListPendingNotify(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListPendingNotify: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("SILENCED 的告警不该进待投递队列，实际 %d 条", len(pending))
	}

	// 窗口内再命中一轮：仍是 SILENCED，且仍然是同一行（SILENCED 是活跃态，
	// 必须被唯一索引与去重查询覆盖，否则每轮新插一行）。
	still, created, err := s.Upsert(ctx, silenced)
	if err != nil {
		t.Fatalf("窗口内第二轮: %v", err)
	}
	if created {
		t.Fatal("SILENCED 必须算活跃态——否则被静默的问题每轮都会新开一条")
	}
	if still.ID != a.ID || still.Status != alerts.StatusSilenced {
		t.Fatalf("窗口内第二轮应合并且保持 SILENCED: %+v", still)
	}
	if still.FireCount != 2 {
		t.Fatalf("静默期间也要计数（这是「持续了多久」的证据）: fire_count=%d", still.FireCount)
	}

	// --- 第二态：窗口过期，条件仍成立 ---
	reopened := upsertInput(now.Add(2 * time.Minute)) // Silenced 为 false
	back, created, err := s.Upsert(ctx, reopened)
	if err != nil {
		t.Fatalf("窗口过期后: %v", err)
	}
	if created {
		t.Fatal("窗口过期不该新开一条——它从头到尾就是同一个问题")
	}
	if back.ID != a.ID {
		t.Fatalf("应仍是同一行: %s vs %s", back.ID, a.ID)
	}
	if back.Status != alerts.StatusOpen {
		t.Fatalf("窗口过期后应转回 OPEN，实际 %s", back.Status)
	}
	if back.NotifyStatus != alerts.NotifyPending {
		t.Fatalf("转回 OPEN 后应重新排队投递，实际 %s", back.NotifyStatus)
	}

	pending, err = s.ListPendingNotify(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListPendingNotify: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != a.ID {
		t.Fatalf("转回 OPEN 后应进待投递队列，实际 %+v", pending)
	}
}

// TestSilenceWindowSuppressesAlreadyOpenAlert：给一条正在响的告警建窗口，
// 它要闭嘴——这正是创建静默窗口的目的。
func TestSilenceWindowSuppressesAlreadyOpenAlert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	open, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if open.Status != alerts.StatusOpen {
		t.Fatalf("status = %s", open.Status)
	}

	silenced := upsertInput(now.Add(time.Minute))
	silenced.Silenced = true
	got, _, err := s.Upsert(ctx, silenced)
	if err != nil {
		t.Fatalf("静默 Upsert: %v", err)
	}
	if got.Status != alerts.StatusSilenced {
		t.Fatalf("已 OPEN 的告警命中窗口后应转 SILENCED，实际 %s", got.Status)
	}
}

// TestAcknowledgeOnlyFromOpenOrReopened：确认只对 OPEN / REOPENED 生效。
func TestAcknowledgeOnlyFromOpenOrReopened(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	a, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	acked, err := s.Acknowledge(ctx, a.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if acked.Status != alerts.StatusAcknowledged || acked.AcknowledgedAt == nil {
		t.Fatalf("确认后应有 acknowledged_at: %+v", acked)
	}

	// 已确认的告警继续命中：保持 ACKNOWLEDGED，不回退成 OPEN，也不再投递。
	still, _, err := s.Upsert(ctx, upsertInput(now.Add(2*time.Minute)))
	if err != nil {
		t.Fatalf("确认后再命中: %v", err)
	}
	if still.Status != alerts.StatusAcknowledged {
		t.Fatalf("「有人接手了」不该被下一轮评估抹掉，实际 %s", still.Status)
	}
	pending, err := s.ListPendingNotify(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListPendingNotify: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("已确认的告警不该继续投递，实际 %d 条", len(pending))
	}

	// 重复确认：状态不对，返回 ErrNotAcknowledgeable（HTTP 层据此给 400 而非 404）。
	if _, err := s.Acknowledge(ctx, a.ID, now.Add(time.Hour)); err == nil {
		t.Fatal("重复确认应报错")
	} else if !strings.Contains(err.Error(), "OPEN") {
		t.Fatalf("错误应说清允许的状态: %v", err)
	}

	// 不存在的告警：ErrNotFound，与「状态不对」区分开。
	if _, err := s.Acknowledge(ctx, uuid.New(), now); err == nil {
		t.Fatal("确认不存在的告警应报错")
	}
}

// TestNotifyStatusTransitions：投递状态与告警状态正交，
// 失败必须带原因（库层 CHECK），成功必须带时刻。
func TestNotifyStatusTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	a, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.MarkNotifyFailed(ctx, a.ID, "telegram: HTTP 502"); err != nil {
		t.Fatalf("MarkNotifyFailed: %v", err)
	}
	failed, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if failed.NotifyStatus != alerts.NotifyFailed || failed.NotifyError == "" {
		t.Fatalf("失败必须带原因: %+v", failed)
	}
	if failed.NotifiedAt != nil {
		t.Fatalf("失败不该有投递时刻: %v", failed.NotifiedAt)
	}
	// 告警本身还是 OPEN——投递失败不改变告警状态。
	if failed.Status != alerts.StatusOpen {
		t.Fatalf("投递失败不该改变告警状态，实际 %s", failed.Status)
	}
	// 失败的仍在待投递队列里（§9.3「失败重试」）。
	pending, err := s.ListPendingNotify(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListPendingNotify: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("投递失败的告警下一轮应重试，实际队列 %d 条", len(pending))
	}

	if err := s.MarkDelivered(ctx, a.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	delivered, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if delivered.NotifyStatus != alerts.NotifyDelivered || delivered.NotifiedAt == nil {
		t.Fatalf("已投递必须带时刻: %+v", delivered)
	}
	if delivered.NotifyError != "" {
		t.Fatalf("投递成功后应清掉失败原因: %q", delivered.NotifyError)
	}
	pending, err = s.ListPendingNotify(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListPendingNotify: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("已投递的不该再排队，实际 %d 条", len(pending))
	}
}

// TestMarkNotifyFailedRejectsEmptyReason：空原因会被库层 CHECK 拒绝，
// 于是失败反而变成了静默失败。仓储必须兜住这个。
func TestMarkNotifyFailedRejectsEmptyReason(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	a, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.MarkNotifyFailed(ctx, a.ID, ""); err != nil {
		t.Fatalf("空原因应被兜底成一句话，而不是让写入失败: %v", err)
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.NotifyStatus != alerts.NotifyFailed || got.NotifyError == "" {
		t.Fatalf("失败必须落库且带原因: %+v", got)
	}
}

// TestSilenceStoreRoundTrip：静默窗口的读写与「此刻生效」判定。
func TestSilenceStoreRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	active, err := s.CreateSilence(ctx, alerts.Silence{
		RuleKey: alerts.RuleMetricSyncFailed, Environment: intEnv,
		Reason: "上游维护窗口", StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
		CreatedBy: "staff_alice",
	})
	if err != nil {
		t.Fatalf("CreateSilence: %v", err)
	}
	if active.ID == uuid.Nil {
		t.Fatal("应生成 ID")
	}
	if _, err := s.CreateSilence(ctx, alerts.Silence{
		Environment: intEnv, Reason: "已过期的旧窗口",
		StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour), CreatedBy: "staff_bob",
	}); err != nil {
		t.Fatalf("CreateSilence（过期）: %v", err)
	}

	got, err := s.ListActiveSilences(ctx, intEnv, now)
	if err != nil {
		t.Fatalf("ListActiveSilences: %v", err)
	}
	if len(got) != 1 || got[0].ID != active.ID {
		t.Fatalf("只应返回此刻生效的窗口: %+v", got)
	}
	if got[0].Reason != "上游维护窗口" || got[0].CreatedBy != "staff_alice" {
		t.Fatalf("窗口字段未如实读回: %+v", got[0])
	}

	all, err := s.ListSilences(ctx, intEnv, 10)
	if err != nil {
		t.Fatalf("ListSilences: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("含过期窗口应有 2 条，实际 %d 条", len(all))
	}
}

// TestCreateSilenceRejectsInvalidWindows：库层 CHECK 与领域校验同一套规则。
func TestCreateSilenceRejectsInvalidWindows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	base := alerts.Silence{
		Environment: intEnv, Reason: "理由", StartsAt: now, EndsAt: now.Add(time.Hour),
		CreatedBy: "staff_alice",
	}
	cases := map[string]func(s *alerts.Silence){
		"空理由":     func(s *alerts.Silence) { s.Reason = "  " },
		"空创建人":    func(s *alerts.Silence) { s.CreatedBy = "" },
		"窗口反向":    func(s *alerts.Silence) { s.EndsAt = s.StartsAt.Add(-time.Hour) },
		"零长度窗口":   func(s *alerts.Silence) { s.EndsAt = s.StartsAt },
		"空环境":     func(s *alerts.Silence) { s.Environment = "" },
		"规则键形态非法": func(s *alerts.Silence) { s.RuleKey = "Bad Key!" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			mutate(&in)
			if _, err := s.CreateSilence(ctx, in); err == nil {
				t.Fatal("应被拒绝")
			}
		})
	}
}

// TestListByStatusFiltersExactly：状态过滤必须精确，
// 空集合等价于「全部活跃状态」。
func TestListByStatusFiltersExactly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	open, _, err := s.Upsert(ctx, upsertInput(now))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	other := upsertInput(now)
	other.RuleKey = alerts.RuleChannelBalanceLow
	other.DedupKey = alerts.RuleChannelBalanceLow + ":" + intEnv + ":ch-a"
	other.Severity = alerts.SeverityWarning
	other.Title = "渠道甲余额不足"
	acked, _, err := s.Upsert(ctx, other)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := s.Acknowledge(ctx, acked.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	onlyOpen, err := s.ListByStatus(ctx, intEnv, []alerts.Status{alerts.StatusOpen}, 10)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(onlyOpen) != 1 || onlyOpen[0].ID != open.ID {
		t.Fatalf("只该返回 OPEN: %+v", onlyOpen)
	}

	allActive, err := s.ListByStatus(ctx, intEnv, nil, 10)
	if err != nil {
		t.Fatalf("ListByStatus(nil): %v", err)
	}
	if len(allActive) != 2 {
		t.Fatalf("空状态集合应等价于全部活跃状态，实际 %d 条", len(allActive))
	}
}

// TestUpsertRejectsInvalidInput：领域校验在写库之前。
func TestUpsertRejectsInvalidInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	cases := map[string]func(in *alerts.UpsertInput){
		"空规则键":    func(in *alerts.UpsertInput) { in.RuleKey = "" },
		"规则键形态非法": func(in *alerts.UpsertInput) { in.RuleKey = "Bad Key" },
		"空去重键":    func(in *alerts.UpsertInput) { in.DedupKey = "" },
		"空标题":     func(in *alerts.UpsertInput) { in.Title = "" },
		"空环境":     func(in *alerts.UpsertInput) { in.Environment = "" },
		"严重度非法":   func(in *alerts.UpsertInput) { in.Severity = "fatal" },
		"零值时钟":    func(in *alerts.UpsertInput) { in.Now = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := upsertInput(now)
			mutate(&in)
			if _, _, err := s.Upsert(ctx, in); err == nil {
				t.Fatal("应被拒绝")
			}
		})
	}
}

// TestUnknownEnvironmentIsRejectedByForeignKey：告警不能落在一个不存在的
// 环境上（宪法 15 条，库层外键）。
func TestUnknownEnvironmentIsRejectedByForeignKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	in := upsertInput(time.Now().UTC())
	in.Environment = "prod" // 不是三个合法值之一
	in.DedupKey = "x:prod:y"
	if _, _, err := s.Upsert(ctx, in); err == nil {
		t.Fatal("不存在的环境应被外键拒绝")
	}
}
